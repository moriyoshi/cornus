package buildwire

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"
	"github.com/moby/buildkit/session/secrets"
	"github.com/tonistiigi/fsutil"

	"cornus/pkg/build/buildprog"
	"cornus/pkg/wire"
)

// ServerSession is the server-side view of a remote build: the caller's spec,
// the caller's directories exposed as BuildKit local mounts, the caller's
// secrets, and a progress sink — all flowing over 9P/WebSocket.
type ServerSession struct {
	Spec BuildSpec

	client  *p9.Client
	control net.Conn
	sess    *yamux.Session

	encMu sync.Mutex
	enc   *json.Encoder
}

// Attach upgrades the HTTP request to the build WebSocket, reads the caller's
// BuildSpec, and connects a 9P client to the caller's exported tree.
func Attach(w http.ResponseWriter, r *http.Request) (*ServerSession, error) {
	sess, err := wire.Accept(w, r)
	if err != nil {
		return nil, err
	}
	var control, p9stream net.Conn
	for i := 0; i < 2; i++ {
		tag, s, err := wire.AcceptTagged(sess)
		if err != nil {
			sess.Close()
			return nil, err
		}
		switch tag {
		case wire.TagControl:
			control = s
		case tagP9:
			p9stream = s
		default:
			sess.Close()
			return nil, fmt.Errorf("buildwire: unknown stream tag %q", tag)
		}
	}
	if control == nil || p9stream == nil {
		sess.Close()
		return nil, fmt.Errorf("buildwire: missing control or 9p stream")
	}

	var spec BuildSpec
	if err := json.NewDecoder(control).Decode(&spec); err != nil {
		sess.Close()
		return nil, fmt.Errorf("buildwire: read spec: %w", err)
	}
	client, err := p9.NewClient(p9stream)
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("buildwire: 9p client: %w", err)
	}
	return &ServerSession{
		Spec:    spec,
		client:  client,
		control: control,
		sess:    sess,
		enc:     json.NewEncoder(control),
	}, nil
}

// Mounts returns the caller's directories as BuildKit local mounts: "context",
// "dockerfile", and one per named build context.
func (s *ServerSession) Mounts() map[string]fsutil.FS {
	m := map[string]fsutil.FS{
		"context":    &p9FS{client: s.client, root: []string{"context"}},
		"dockerfile": &p9FS{client: s.client, root: []string{"dockerfile"}},
	}
	for _, name := range s.Spec.NamedContexts {
		m[name] = &p9FS{client: s.client, root: []string{"ctx", name}}
	}
	return m
}

// Secrets returns a SecretStore backed by the caller's 9P /secrets tree, or nil
// when the build declared no secrets.
func (s *ServerSession) Secrets() secrets.SecretStore {
	if len(s.Spec.SecretIDs) == 0 {
		return nil
	}
	return p9SecretStore{client: s.client}
}

// Progress returns a Sink that streams each build-progress event to the caller
// as an event frame on the control stream.
func (s *ServerSession) Progress() buildprog.Sink {
	return func(e buildprog.Event) { _ = s.send(controlMsg{Event: &e}) }
}

// Done sends the final result (or error) to the caller and ends the session.
func (s *ServerSession) Done(res *Result, buildErr error) error {
	m := controlMsg{Done: true}
	if res != nil {
		m.Digest = res.ImageDigest
	}
	if buildErr != nil {
		m.Err = buildErr.Error()
	}
	return s.send(m)
}

func (s *ServerSession) send(m controlMsg) error {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	return s.enc.Encode(m)
}

// Close tears down the 9P client and the WebSocket session.
func (s *ServerSession) Close() {
	// Flush and half-close the control stream before tearing down the session. On
	// the batched-pipelined send path (wire's default) a control write — the final
	// Done frame from Done() — is only queued by the send loop, not yet written to
	// the wire, when send() returns. Closing the yamux session immediately would
	// close the underlying WebSocket before that frame is flushed, and the caller's
	// decoder reads EOF instead of the result ("buildwire: control stream: EOF").
	// Closing the control stream sends its FIN in the stream's own send class —
	// strictly ordered behind the queued Done frame in the same per-class FIFO — and
	// blocks until the send loop has written it, so by the time it returns the Done
	// frame is guaranteed on the wire.
	if s.control != nil {
		_ = s.control.Close()
	}
	if s.client != nil {
		_ = s.client.Close()
	}
	_ = s.sess.Close()
}
