package deploywire

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"cornus/pkg/credential"
)

// The credential request/response protocol rides one backing stream per fetch
// (one stream per credential retrieval, like exec/port-forward opening one stream
// per invocation). The requester (a pod's caretaker, relayed through the server)
// writes a CredentialRequest; the caller (which holds the source) writes a
// CredentialResponse. The stream is closed after the single exchange.

// CredentialRequest is written by the requester to ask for a fresh credential.
type CredentialRequest struct {
	Params map[string]string `json:"params,omitempty"`
}

// CredentialResponse is written by the caller with the minted credential, or an
// Error if the source failed.
type CredentialResponse struct {
	Values     map[string]string `json:"values,omitempty"`
	Expiration time.Time         `json:"expiration,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// FetchCredential performs one credential exchange over conn (an already-opened
// stream on which the session/name lines have been sent): it writes the request
// and decodes the response. Used by the caretaker credential role.
func FetchCredential(conn net.Conn, params map[string]string) (credential.Credential, error) {
	if err := json.NewEncoder(conn).Encode(CredentialRequest{Params: params}); err != nil {
		return credential.Credential{}, fmt.Errorf("deploywire: send credential request: %w", err)
	}
	var resp CredentialResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return credential.Credential{}, fmt.Errorf("deploywire: read credential response: %w", err)
	}
	if resp.Error != "" {
		return credential.Credential{}, fmt.Errorf("credential source: %s", resp.Error)
	}
	return credential.Credential{Values: resp.Values, Expiration: resp.Expiration}, nil
}

// serveCredential handles one credential backing request on the caller side: it
// reads the request, runs the client-side source backend, and writes the
// response. Errors are reported in the response so the requester surfaces them.
func serveCredential(ctx context.Context, conn net.Conn, cb CredentialBacking) {
	var req CredentialRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeCredError(conn, fmt.Errorf("read request: %w", err))
		return
	}
	src, err := credential.Open(cb.Backend, cb.Config)
	if err != nil {
		writeCredError(conn, err)
		return
	}
	cred, err := src.Fetch(ctx, req.Params)
	if err != nil {
		writeCredError(conn, err)
		return
	}
	_ = json.NewEncoder(conn).Encode(CredentialResponse{Values: cred.Values, Expiration: cred.Expiration})
}

func writeCredError(conn net.Conn, err error) {
	_ = json.NewEncoder(conn).Encode(CredentialResponse{Error: err.Error()})
}

// credHandler builds the ServeBackings credential callback for a session's
// declared sources: it dispatches an incoming backing name to its backend, or
// drops the stream if the name was not declared (defense in depth beyond the
// server's AllowsCredential check).
func credHandler(ctx context.Context, sources []CredentialBacking) func(name string, conn net.Conn) {
	if len(sources) == 0 {
		return nil
	}
	byName := make(map[string]CredentialBacking, len(sources))
	for _, cb := range sources {
		byName[cb.Name] = cb
	}
	return func(name string, conn net.Conn) {
		cb, ok := byName[name]
		if !ok {
			return
		}
		serveCredential(ctx, conn, cb)
	}
}
