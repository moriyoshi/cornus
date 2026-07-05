package deploywire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"cornus/pkg/clientproxy"
	"cornus/pkg/wire"
)

// Serve opens a deploy-attach session at url, sends the spec, serves the
// caller-local mount directories over 9P (read-only, confined) for the lifetime
// of the session, and invokes events for each server frame until the session
// ends. localDirs maps a LocalMount.Name to the caller's absolute directory.
//
// Serve blocks until the server sends a terminal (Done) event or ctx is
// cancelled. On ctx cancel it sends a graceful "down" command and closes the
// session; the server tears the deployment down either way. header carries extra
// WebSocket-handshake headers (e.g. Authorization); nil sends none. ct carries the
// optional client transport customizations for an https/wss endpoint (custom CA /
// mTLS, and/or a custom dial function such as an SSH tunnel); the zero value uses
// the system defaults.
func Serve(ctx context.Context, url string, spec DeployAttachSpec, localDirs map[string]string, events func(Event), header http.Header, ct wire.ClientTransport) error {
	sess, err := wire.DialControlHeaderCT(ctx, url, nil, header, ct)
	if err != nil {
		return err
	}
	defer sess.Close()

	control, err := wire.OpenTagged(sess, wire.TagControl)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(control).Encode(spec); err != nil {
		return fmt.Errorf("deploywire: send spec: %w", err)
	}

	// Serve the caller-local mount dirs on demand over 9P for the session's life;
	// non-read-only mounts are served read-write (writable confined export). The
	// same accept loop also answers credential backings, so a creds-only session
	// (no mounts) still serves them.
	writable := map[string]bool{}
	for _, lm := range spec.LocalMounts {
		if !lm.ReadOnly {
			writable[lm.Name] = true
		}
	}
	var reads atomic.Int64
	// The egress backing dials each relayed destination through the CALLER's own
	// resolved proxy (corporate HTTP/SOCKS, SASE) when one applies — so client-side
	// egress reaches destinations the caller can only reach via its sanctioned proxy,
	// not merely direct from the caller's host.
	go wire.ServeBackings(sess, localDirs, nil, writable, &reads, credHandler(ctx, spec.CredentialSources), egressHandlerFor(ctx, spec.Spec.Egress, clientproxy.Dialer()))

	// Read control events in a goroutine so the main flow can select on "server
	// finished" vs "caller cancelled" without depending on whether the control
	// read honors ctx (it does not — the conn uses context.Background()). res is
	// buffered so the reader never blocks, and the deferred sess.Close() unblocks
	// its Decode on return.
	dec := json.NewDecoder(control)
	res := make(chan error, 1)
	go func() {
		for {
			var e Event
			if err := dec.Decode(&e); err != nil {
				if errors.Is(err, io.EOF) {
					res <- nil
				} else {
					res <- fmt.Errorf("deploywire: control stream: %w", err)
				}
				return
			}
			if events != nil {
				events(e)
			}
			if e.Done {
				if e.Err != "" {
					res <- errors.New(e.Err)
				} else {
					res <- nil
				}
				return
			}
		}
	}()

	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		// Graceful teardown: ask the server to tear down, then wait (bounded) for
		// its terminal Done so we do not return before the workload is actually
		// removed. sess.Close() (deferred) then unblocks the reader if it is still
		// decoding.
		_ = json.NewEncoder(control).Encode(command{Action: "down"})
		select {
		case <-res:
		case <-time.After(30 * time.Second):
		}
		return ctx.Err()
	}
}
