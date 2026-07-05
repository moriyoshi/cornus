package caretaker

// One accept loop per session.
//
// yamux hands each accepted stream to exactly ONE AcceptStream caller, and the caretaker
// had two: runPortForwardAccept and serveIngress, each closing any tag it did not
// recognize. A pod carrying both a hub role with delivery targets and a PortForward role
// therefore raced for every inbound stream, and the loser's tag was closed on arrival —
// an ingress delivery answered as if the spoke had gone away, or a port-forward that
// vanished, depending on which goroutine happened to win. Nothing logged it, because from
// each loop's point of view closing a foreign tag is the correct thing to do.
//
// tagDispatch is that one loop. Roles register a handler for their tag instead of
// accepting for themselves, so an unrecognized tag means what it says.

import (
	"context"
	"net"
	"sync"

	"cornus/pkg/wire"

	"github.com/hashicorp/yamux"
)

type tagDispatch struct {
	mu       sync.RWMutex
	handlers map[byte]func(net.Conn)
}

func newTagDispatch() *tagDispatch {
	return &tagDispatch{handlers: map[byte]func(net.Conn){}}
}

// handle registers fn as the handler for tag. Registration happens before run starts, so
// a stream can never arrive for a tag whose role has not been wired yet.
func (d *tagDispatch) handle(tag byte, fn func(net.Conn)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[tag] = fn
}

func (d *tagDispatch) empty() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers) == 0
}

// run accepts tagged streams until ctx is done or the session fails, handing each to its
// registered handler. It relies on its caller closing sess once ctx is done — AcceptTagged
// has nothing else to interrupt it — which is what runCaretakerConn does.
func (d *tagDispatch) run(ctx context.Context, sess *yamux.Session) error {
	for {
		tag, stream, err := wire.AcceptTagged(sess)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		d.mu.RLock()
		fn := d.handlers[tag]
		d.mu.RUnlock()
		if fn == nil {
			stream.Close()
			continue
		}
		go fn(stream)
	}
}
