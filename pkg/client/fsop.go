package client

// The client side of POST /.cornus/v1/deploy/{name}/fsop.
//
// The archive calls next door can pack a path and unpack a tar; this is everything else —
// readdir, delete, rename, and an in-place copy that never brings the bytes here at all.
// Whether a given path can be served is the SERVER's answer: a 501 means "do it the long
// way round", and callers are expected to fall back rather than surface it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"cornus/pkg/api"
)

// ErrFSOpUnsupported reports that the deployment's backend cannot serve a structured
// filesystem operation on this path — no operator, no caretaker connected yet, or a path
// outside what the operator can see.
//
// It is a distinct sentinel because it is not a failure: the caller is meant to relay the
// bytes itself instead. Collapsing it into a generic error is how a working (if slower)
// operation turns into an error toast.
var ErrFSOpUnsupported = errors.New("structured filesystem operations are unsupported here")

func fsopPath(name string, req api.FSOpRequest) string {
	q := url.Values{}
	q.Set("op", string(req.Op))
	q.Set("path", req.Path)
	if req.To != "" {
		q.Set("to", req.To)
	}
	if req.Recursive {
		q.Set("recursive", "1")
	}
	if req.NoOverwriteDirNonDir {
		q.Set("noOverwriteDirNonDir", "1")
	}
	if req.CopyUIDGID {
		q.Set("copyUIDGID", "1")
	}
	return "/.cornus/v1/deploy/" + url.PathEscape(name) + "/fsop?" + q.Encode()
}

// FSOp runs one structured filesystem operation against a deployment.
//
// body is the tar for api.FSOpPut and ignored otherwise; out receives the tar for
// api.FSOpGet and may be nil elsewhere. A 501 comes back as ErrFSOpUnsupported so the
// caller can branch on it without parsing a message.
func (c *Client) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+fsopPath(name, req), body)
	if err != nil {
		return api.FSOpResponse{}, err
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/x-tar")
	}
	c.setAuth(httpReq)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return api.FSOpResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotImplemented {
		return api.FSOpResponse{Error: apiError(resp).Error(), Code: api.FSErrUnsupported},
			fmt.Errorf("%w: %s", ErrFSOpUnsupported, name)
	}
	if resp.StatusCode != http.StatusOK {
		// The body is an api.FSOpResponse on a refusal the operator classified, and a
		// plain {"error": ...} on the generic paths; both decode into the same struct,
		// so the Code survives whichever one it was.
		var refusal api.FSOpResponse
		if err := json.NewDecoder(resp.Body).Decode(&refusal); err == nil && refusal.Error != "" {
			return refusal, fmt.Errorf("fsop %s %s: %s", req.Op, req.Path, refusal.Error)
		}
		return api.FSOpResponse{}, apiError(resp)
	}

	if req.Op != api.FSOpGet {
		var ok api.FSOpResponse
		if err := json.NewDecoder(resp.Body).Decode(&ok); err != nil {
			return api.FSOpResponse{}, fmt.Errorf("fsop %s: malformed reply: %w", req.Op, err)
		}
		return ok, nil
	}

	if out == nil {
		out = io.Discard
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		return api.FSOpResponse{}, err
	}
	// A mid-stream failure can no longer change the status, so it rides the trailer —
	// exactly as CopyFrom's does. Without this check a truncated tar reads as a
	// complete one.
	if err := streamError(resp); err != nil {
		return api.FSOpResponse{}, err
	}
	return api.FSOpResponse{Body: true}, nil
}
