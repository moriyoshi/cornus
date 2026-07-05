package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/activity"
)

// The keep-alive is what lets a mostly-idle stream survive a proxy, so a reader
// that mistook one for a record — or choked on it — would break the feature it
// exists to protect. It must be discarded silently.
func TestReadSSEDiscardsKeepAlives(t *testing.T) {
	stream := ": keep-alive\n\n" +
		"event: activity\ndata: {\"id\":\"a\"}\n\n" +
		": keep-alive\n\n" +
		"event: activity\ndata: {\"id\":\"b\"}\n\n"
	var got []string
	err := readSSE(strings.NewReader(stream), func(event string, data []byte) error {
		got = append(got, event+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`activity:{"id":"a"}`, `activity:{"id":"b"}`}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("readSSE = %v, want %v", got, want)
	}
}

// SSE splits a payload across one data line per newline. The server never emits
// such a record, but a reader that dropped the continuation would corrupt
// precisely the longest records — so rejoin them per the spec.
func TestReadSSERejoinsMultiLineData(t *testing.T) {
	var got string
	err := readSSE(strings.NewReader("event: activity\ndata: {\"a\":1,\ndata: \"b\":2}\n\n"),
		func(_ string, data []byte) error { got = string(data); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":1,\n\"b\":2}"; got != want {
		t.Fatalf("readSSE = %q, want %q", got, want)
	}
}

// A frame with no blank line after it has not been dispatched: the writer may
// still be mid-message. Delivering it would hand the caller a truncated record.
func TestReadSSEIgnoresAnUndispatchedFrame(t *testing.T) {
	var n int
	err := readSSE(strings.NewReader("event: activity\ndata: {\"id\":\"a\"}\n\nevent: activity\ndata: {\"id\":\"trunc"),
		func(string, []byte) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dispatched %d frames, want only the one terminated by a blank line", n)
	}
}

// ActivityFollow delivers records as they arrive and surfaces the live instance
// from the response headers — before any record, which is when a caller needs it
// to label the running incarnation.
func TestActivityFollowStreamsRecords(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("follow") != "1" {
			t.Errorf("follow not requested: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cornus-Activity-Live-Instance", "inst-live")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		f.Flush()
		_, _ = w.Write([]byte("event: activity\ndata: {\"kind\":\"server\",\"id\":\"a\"}\n\n"))
		f.Flush()
		<-release
		_, _ = w.Write([]byte(": keep-alive\n\nevent: activity\ndata: {\"kind\":\"9p-mount\",\"id\":\"b\"}\n\n"))
		f.Flush()
	}))
	defer ts.Close()

	seen := make(chan activity.Event, 4)
	done := make(chan error, 1)
	var live string
	go func() {
		var err error
		live, err = New(ts.URL).ActivityFollow(context.Background(), time.Time{}, "", func(e activity.Event) error {
			seen <- e
			return nil
		})
		done <- err
	}()

	first := <-seen
	if first.Kind != activity.KindServer {
		t.Fatalf("first record = %v", first.Kind)
	}
	close(release)
	second := <-seen
	if second.Kind != activity.KindMount9P {
		t.Fatalf("second record = %v, want the mount past the keep-alive", second.Kind)
	}
	if err := <-done; err != nil {
		t.Fatalf("ActivityFollow = %v", err)
	}
	if live != "inst-live" {
		t.Errorf("live instance = %q, want it read off the response headers", live)
	}
}

// Cancelling is the normal way a follow ends. It must report context.Canceled
// rather than whatever read error the torn-down transport happened to produce,
// or every Ctrl-C would look like a failure.
func TestActivityFollowReportsCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := New(ts.URL).ActivityFollow(ctx, time.Time{}, "", func(activity.Event) error { return nil })
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ActivityFollow after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ActivityFollow did not return after its context was cancelled")
	}
}

// Cancellation reaches this client as whatever the transport underneath says a
// torn-down stream looks like. Over plain HTTP that is already context.Canceled,
// but the same command runs over a yamux-tunneled connection where it is not —
// and there a plain Ctrl-C would otherwise print an unrelated transport error
// and exit non-zero. The caller's context is the authority on why the read
// stopped.
func TestFollowErrPrefersTheContextReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tunnelErr := errors.New("yamux: stream closed")
	if got := followErr(ctx, tunnelErr); !errors.Is(got, context.Canceled) {
		t.Errorf("followErr on a cancelled follow = %v, want context.Canceled", got)
	}
	// A genuine failure on a live context must NOT be relabelled as a cancel:
	// that would turn a broken stream into a clean exit.
	if got := followErr(context.Background(), tunnelErr); !errors.Is(got, tunnelErr) {
		t.Errorf("followErr on a live context = %v, want the underlying error", got)
	}
	if got := followErr(ctx, nil); got != nil {
		t.Errorf("followErr with no error = %v, want nil", got)
	}
}

// A refused follow (a 4xx, e.g. --follow with --unfinished) must surface as an
// error, not as an empty stream that reads like "nothing is happening".
func TestActivityFollowSurfacesAnErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"follow and unfinished are incompatible"}`))
	}))
	defer ts.Close()

	_, err := New(ts.URL).ActivityFollow(context.Background(), time.Time{}, "", func(activity.Event) error { return nil })
	if err == nil {
		t.Fatal("a 400 must be reported, not swallowed into an empty stream")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Errorf("error = %v, want the server's message", err)
	}
}
