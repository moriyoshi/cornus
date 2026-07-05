package conduithost

import "encoding/json"

// ProtocolVersion is the control-socket contract version. A joiner that speaks a
// different major version is refused at hello rather than allowed to register
// frames the host will misread.
const ProtocolVersion = 1

// Frame is one request on the control connection. Requests and replies are
// newline-delimited JSON, matching the agent's control socket
// (cmd/cornus/internal/clientagent/protocol.go) so there is one wire idiom in the
// tree rather than two.
//
// ID correlates a reply with its request. It is chosen by the joiner and doubles
// as the registration handle for OpWithdraw, so the host never has to mint one and
// hand it back.
type Frame struct {
	ID      string          `json:"id,omitempty"`
	Op      string          `json:"op"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Reply answers exactly one Frame, carrying its ID back.
type Reply struct {
	ID      string          `json:"id,omitempty"`
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Control operations.
const (
	// OpHello must be the first frame on a connection. It negotiates the version
	// and returns the conduit's CANONICAL settings — which may not be the ones the
	// joiner asked for, since the incumbent's settings win.
	OpHello = "hello"
	// OpRegister applies one registration, scoped to this connection.
	OpRegister = "register"
	// OpWithdraw drops one registration early. Dropping the connection withdraws
	// everything on it, so this is only for a caller that wants a partial teardown.
	OpWithdraw = "withdraw"
	// OpPing asks only "are you answering?". It is valid as a first frame, takes no
	// payload, registers nothing, and the host hangs up after replying.
	//
	// It exists because CONNECTING to a control socket proves almost nothing. The
	// kernel completes a unix-socket connection into the listen backlog whether or
	// not the owning process is still servicing it, so a wedged host looks perfectly
	// healthy to a dial-only probe — and the next participant then blocks in the
	// hello handshake, holding the port lock, until its timeout expires. Requiring a
	// REPLY is what tells "the socket is bound" apart from "somebody is home".
	OpPing = "ping"
	// OpAdopt asks the host to replicate its listener onto THIS connection, which is
	// then closed. It gets a connection of its own, used for nothing else.
	//
	// That is not tidiness. On unix the descriptor rides as ancillary data attached
	// to specific bytes, so any buffering reader that has read ahead past them
	// consumes the message and drops the descriptor on the floor — and the main
	// control connection has a json.Decoder on it by construction. Interleaving the
	// two on one connection would work until two frames arrived back-to-back, i.e.
	// under load and never in a test. See pkg/listenerpass.
	OpAdopt = "adopt"
)

// AdoptRequest identifies the process that will receive the listener replica.
// Pid is required on Windows (WSADuplicateSocket names its target) and ignored on
// unix.
type AdoptRequest struct {
	Pid int `json:"pid"`
}

// HelloRequest identifies the joining process.
//
// Pid is not a security boundary — the rendezvous directory is 0700 and every
// participant already runs as this user. It is here because Windows needs it:
// WSADuplicateSocket is target-scoped and must name the receiving process before
// it can produce a usable WSAPROTOCOL_INFOW, where SCM_RIGHTS needs nothing. See
// the JOURNAL entry "the conduit rendezvous design, as built".
type HelloRequest struct {
	Version int `json:"version"`
	Pid     int `json:"pid"`
}

// HelloResponse describes the conduit the joiner actually landed in.
type HelloResponse struct {
	Version int `json:"version"`
	// Bind is the conduit's normalized bind address. A joiner reports THIS, never
	// the address it requested: consolidation means the two legitimately differ, and
	// telling a user the address they typed rather than the one their browser must
	// point at is the whole failure this design exists to remove.
	Bind string `json:"bind"`
	// Settings is opaque here on purpose. This package is about rendezvous and
	// transport; what a conduit's settings MEAN belongs to pkg/clientconduit, which
	// decodes this and can report a mismatch against what the joiner asked for.
	Settings json.RawMessage `json:"settings,omitempty"`
	// Banner is the host's session-level description, so a joiner prints the same
	// line the host would.
	Banner []string `json:"banner,omitempty"`
	// HostPid names the process to blame when this conduit dies.
	HostPid int `json:"hostPid"`
}

// RegisterRequest carries one opaque registration. Kind selects which of the
// caller's registration shapes Payload holds; conduithost never decodes it.
type RegisterRequest struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
	// Seq is the claim's precedence, zero to have the host assign one.
	//
	// It is quoted back when REPLAYING a registration into a host that replaced the
	// one which assigned it. Without that, precedence after a takeover is decided by
	// the order survivors happen to reconnect in — so the same crash routes
	// differently each time, and nothing can detect it, because every participant
	// replays its own claims in its own original order.
	Seq uint64 `json:"seq,omitempty"`
}

// RegisterResponse returns the precedence the host assigned, so the registrant can
// quote it on a later replay.
type RegisterResponse struct {
	Seq uint64 `json:"seq"`
}
