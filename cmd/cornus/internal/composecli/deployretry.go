package composecli

import (
	"context"
	"errors"
	"strings"
	"time"

	"cornus/pkg/client"
)

// A `compose up` deploys every selected service concurrently in one fail-fast
// errgroup (see runForeground / upDetached): the first service whose deploy
// returns an error cancels the shared context, which tears down the session
// conduit and every OTHER service's client-local 9P mount along with it (see
// runForeground.teardown). That is the right response to a terminal failure — a
// bad spec, a missing image — but the wrong one for a transient, self-clearing
// server-side condition. The prime example is a one-shot (Job) service being
// re-applied while its previous Job is still terminating: the backend deletes the
// old Job foreground and recreates it, and for a moment the old object lingers.
// Retrying such an error a few times before letting it escape keeps one service's
// recoverable hiccup from collapsing the whole up and dropping healthy peers'
// mounts.
//
// This wraps ONLY the stateless mount-free Deploy POST. The mounted (deploy-attach)
// path is deliberately excluded: retrying an attach re-runs the client reconciler,
// which mints a fresh mount session id and can re-apply the workload — itself a
// "connection replaced under a fresh id" event that orphans the id baked into the
// running pod (the stale-mount-session failure mode). The server-side waitJobGone
// fix already prevents the still-terminating race on that path.

// deployRetryAttempts bounds the total tries (the first plus retries) of a
// transient deploy error. Deliberately small: the server itself already waits the
// common case (a still-terminating Job replace) out, so this is defense in depth,
// not the primary mechanism.
const deployRetryAttempts = 4

// deployRetryBackoff is the first inter-try pause, doubled each round. A var only
// so tests can shrink it; production never mutates it.
var deployRetryBackoff = 500 * time.Millisecond

// transientDeploy reports whether a deploy error is a transient server-side
// condition a bounded retry may clear, rather than a terminal one. The stateless
// Deploy POST returns a typed *client.APIError, so the primary signal is its HTTP
// status (5xx / 429 retryable, 4xx terminal). The "still terminating" string match
// is a belt-and-suspenders for the Job-replace race in case the message ever
// arrives without a typed status.
func transientDeploy(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Transient()
	}
	return strings.Contains(err.Error(), "still terminating")
}

// retryTransientDeploy runs deploy, retrying on a transient error (transientDeploy)
// with a bounded, doubling, ctx-aware backoff. A success, a terminal error, or the
// final attempt returns as-is; a cancelled ctx (e.g. another service's genuine
// failure already cancelled the shared errgroup) stops immediately without a
// further try. It never masks the failure — the last observed error is returned.
func retryTransientDeploy(ctx context.Context, deploy func() error) error {
	backoff := deployRetryBackoff
	for attempt := 1; ; attempt++ {
		err := deploy()
		if err == nil || attempt >= deployRetryAttempts || !transientDeploy(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}
