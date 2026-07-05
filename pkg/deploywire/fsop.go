package deploywire

// The caller side of the caretaker's filesystem-operation stream.
//
// pkg/wire owns the framing and deliberately knows nothing of pkg/api (its package
// invariant), so the JSON shapes live here — one level up, where both the deploy backends
// and pkg/server already speak api. A backend that has a caretaker session for an
// instance runs an operation by calling FSOp; everything about which paths are servable
// is the caretaker's answer, not the backend's guess.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/wire"
)

// FSOp runs one filesystem operation over a caretaker's session and returns the
// caretaker's answer.
//
// The returned error is a TRANSPORT failure only. An operation the caretaker refused —
// a missing path, a read-only mount, a path it cannot see — comes back in the response's
// Error and Code with a nil error, because the caller's next move depends on which
// refusal it was and an error string cannot carry that.
//
// body is the tar stream for api.FSOpPut and is ignored otherwise; out receives the tar
// stream for api.FSOpGet and may be nil for every other op.
func FSOp(ctx context.Context, sess *yamux.Session, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return api.FSOpResponse{}, err
	}
	stream, err := wire.OpenFSOp(sess, raw)
	if err != nil {
		return api.FSOpResponse{}, fmt.Errorf("fsop: open stream: %w", err)
	}
	defer stream.Close()
	// The stream is the only thing a cancelled context can interrupt: a caretaker that
	// stops answering mid-op would otherwise wedge this call for the life of the session.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			stream.Close()
		case <-done:
		}
	}()

	if req.Op == api.FSOpPut {
		if body == nil {
			body = emptyReader{}
		}
		if err := wire.WriteFSOpBody(stream, body); err != nil {
			return api.FSOpResponse{}, fmt.Errorf("fsop: sending body: %w", err)
		}
	}

	replyRaw, err := wire.ReadFSOpFrame(stream)
	if err != nil {
		return api.FSOpResponse{}, fmt.Errorf("fsop: reading reply: %w", err)
	}
	var resp api.FSOpResponse
	if err := json.Unmarshal(replyRaw, &resp); err != nil {
		return api.FSOpResponse{}, fmt.Errorf("fsop: malformed reply: %w", err)
	}
	if !resp.Body {
		return resp, nil
	}
	if out == nil {
		// Nobody wants the bytes, but they are already on their way; draining keeps a
		// stream from being torn down under a writer that is mid-archive.
		out = io.Discard
	}
	if err := wire.ReadFSOpBody(stream, out); err != nil {
		return resp, fmt.Errorf("fsop: reading body: %w", err)
	}
	return resp, nil
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
