package composecli

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cornus/pkg/client"
)

// TestTransientDeploy pins the classifier: a 5xx/429 API status (the stateless
// Deploy POST) and a bare "still terminating" message (the deploy-attach path)
// are retryable; a 4xx, an unrelated error, and nil are terminal.
func TestTransientDeploy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"api-500", &client.APIError{StatusCode: 500, Status: "500 Internal Server Error", Message: "boom"}, true},
		{"api-503", &client.APIError{StatusCode: 503}, true},
		{"api-429", &client.APIError{StatusCode: 429}, true},
		{"api-400", &client.APIError{StatusCode: 400, Message: "bad spec"}, false},
		{"api-404", &client.APIError{StatusCode: 404}, false},
		{"api-409", &client.APIError{StatusCode: 409}, false},
		{"wrapped-api-500", fmt.Errorf("deploy web: %w", &client.APIError{StatusCode: 500}), true},
		{"still-terminating", errors.New(`job "web" still terminating`), true},
		{"wrapped-still-terminating", fmt.Errorf("deploy web: %w", errors.New("api: job \"web\" still terminating")), true},
		{"unrelated", errors.New("no such image"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientDeploy(tc.err); got != tc.want {
				t.Fatalf("transientDeploy(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func withFastBackoff(t *testing.T) {
	t.Helper()
	prev := deployRetryBackoff
	deployRetryBackoff = time.Millisecond
	t.Cleanup(func() { deployRetryBackoff = prev })
}

// TestRetryTransientDeploySucceedsAfterTransient proves a transient error is
// retried until it clears — the whole point, so one Job-replace race does not
// fail-fast the up and drop healthy peers' mounts.
func TestRetryTransientDeploySucceedsAfterTransient(t *testing.T) {
	withFastBackoff(t)
	calls := 0
	err := retryTransientDeploy(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New(`job "web" still terminating`)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want success after the condition cleared, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("want 3 tries (2 transient + success), got %d", calls)
	}
}

// TestRetryTransientDeployStopsAtCap bounds the retries: a persistently transient
// error is tried exactly deployRetryAttempts times and then surfaced, not looped
// forever.
func TestRetryTransientDeployStopsAtCap(t *testing.T) {
	withFastBackoff(t)
	calls := 0
	sentinel := errors.New(`job "web" still terminating`)
	err := retryTransientDeploy(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want the last transient error surfaced, got %v", err)
	}
	if calls != deployRetryAttempts {
		t.Fatalf("want exactly %d tries, got %d", deployRetryAttempts, calls)
	}
}

// TestRetryTransientDeployTerminalNoRetry proves a terminal error is returned on
// the first try, without burning retries on something a retry cannot fix.
func TestRetryTransientDeployTerminalNoRetry(t *testing.T) {
	withFastBackoff(t)
	calls := 0
	terminal := &client.APIError{StatusCode: 400, Message: "bad spec"}
	err := retryTransientDeploy(context.Background(), func() error {
		calls++
		return terminal
	})
	if !errors.Is(err, terminal) {
		t.Fatalf("want the terminal error returned as-is, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("a terminal error must not be retried, got %d tries", calls)
	}
}

// TestRetryTransientDeployHonorsCancelledCtx proves a cancelled shared context
// (e.g. another service's genuine failure already cancelled the errgroup) stops
// the retry immediately rather than sleeping out the backoff on a doomed up.
func TestRetryTransientDeployHonorsCancelledCtx(t *testing.T) {
	withFastBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryTransientDeploy(ctx, func() error {
		calls++
		return errors.New(`job "web" still terminating`)
	})
	if err == nil {
		t.Fatal("want the transient error returned, not nil")
	}
	if calls != 1 {
		t.Fatalf("a cancelled ctx must stop after the first try, got %d", calls)
	}
}
