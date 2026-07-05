package attachsession

import (
	"context"
	"errors"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
)

// fakeAttacher drives DeployAttach for the tests: it optionally emits a Ready
// event carrying status, then either returns failErr immediately (failNow, a
// pre-ready failure) or blocks until the session context is cancelled or selfExit
// fires (a self-exit).
type fakeAttacher struct {
	status   *api.DeployStatus
	failErr  error
	failNow  bool
	selfExit chan struct{}
}

func (f *fakeAttacher) DeployAttach(ctx context.Context, _ api.DeploySpec, events func(deploywire.Event)) error {
	if f.failNow {
		return f.failErr
	}
	if f.status != nil {
		events(deploywire.Event{Ready: true, Status: f.status})
	}
	if f.selfExit != nil {
		select {
		case <-ctx.Done():
		case <-f.selfExit:
		}
	} else {
		<-ctx.Done()
	}
	if f.failErr != nil {
		return f.failErr
	}
	return ctx.Err()
}

func TestOpenReadyStop(t *testing.T) {
	st := api.DeployStatus{Name: "web"}
	s := Open(&fakeAttacher{status: &st}, api.DeploySpec{})

	if err := s.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady = %v, want nil", err)
	}
	if got, ok := s.Status(); !ok || got.Name != "web" {
		t.Fatalf("Status = %+v, %v; want {web}, true", got, ok)
	}
	select {
	case <-s.Done():
		t.Fatal("Done closed before Stop")
	default:
	}

	s.Stop()
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	if s.Context().Err() == nil {
		t.Fatal("Context not cancelled after Stop")
	}
}

func TestWaitReadyPreReadyError(t *testing.T) {
	wantErr := errors.New("read-write mount rejected")
	s := Open(&fakeAttacher{failErr: wantErr, failNow: true}, api.DeploySpec{})

	if err := s.WaitReady(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("WaitReady = %v, want %v", err, wantErr)
	}
	// The goroutine returned on its own, so Done is closed and the context is
	// self-cancelled without a Stop.
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after a pre-ready failure")
	}
	if s.Context().Err() == nil {
		t.Fatal("Context not cancelled after the attach returned")
	}
}

// A workload that exits on its own must close Done() AND cancel Context(), so a
// resource parented on Context() (the reconcile engine's exposure) is withdrawn
// with the session — without anyone calling Stop.
func TestSelfExitCancelsContext(t *testing.T) {
	st := api.DeployStatus{Name: "web"}
	exit := make(chan struct{})
	s := Open(&fakeAttacher{status: &st, selfExit: exit}, api.DeploySpec{})

	if err := s.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady = %v, want nil", err)
	}
	close(exit) // the workload exits on its own

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after self-exit")
	}
	if s.Context().Err() == nil {
		t.Fatal("self-exit did not cancel Context (exposure would leak)")
	}
}

// WaitReady honours its own ctx: a caller (foreground Ctrl-C / start timeout) that
// cancels while the attach is still pre-ready gets ctx.Err(), and the session is
// left for the caller to Stop.
func TestWaitReadyContextCancel(t *testing.T) {
	s := Open(&fakeAttacher{}, api.DeploySpec{}) // never becomes ready on its own

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if err := s.WaitReady(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady = %v, want context.Canceled", err)
	}
	s.Stop() // the session outlived the wait; the caller tears it down
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
}
