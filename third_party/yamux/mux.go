package yamux

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Config is used to tune the Yamux session
type Config struct {
	// AcceptBacklog is used to limit how many streams may be
	// waiting an accept.
	AcceptBacklog int

	// EnableKeepalive is used to do a period keep alive
	// messages using a ping.
	EnableKeepAlive bool

	// KeepAliveInterval is how often to perform the keep alive
	KeepAliveInterval time.Duration

	// ConnectionWriteTimeout is meant to be a "safety valve" timeout after
	// we which will suspect a problem with the underlying connection and
	// close it. This is only applied to writes, where's there's generally
	// an expectation that things will move along quickly.
	ConnectionWriteTimeout time.Duration

	// MaxStreamWindowSize is used to control the maximum
	// window size that we allow for a stream.
	MaxStreamWindowSize uint32

	// StreamOpenTimeout is the maximum amount of time that a stream will
	// be allowed to remain in pending state while waiting for an ack from the peer.
	// Once the timeout is reached the session will be gracefully closed.
	// A zero value disables the StreamOpenTimeout allowing unbounded
	// blocking on OpenStream calls.
	StreamOpenTimeout time.Duration

	// StreamCloseTimeout is the maximum time that a stream will allowed to
	// be in a half-closed state when `Close` is called before forcibly
	// closing the connection. Forcibly closed connections will empty the
	// receive buffer, drop any future packets received for that stream,
	// and send a RST to the remote side.
	StreamCloseTimeout time.Duration

	// LogOutput is used to control the log destination. Either Logger or
	// LogOutput can be set, not both.
	LogOutput io.Writer

	// Logger is used to pass in the logger to be used. Either Logger or
	// LogOutput can be set, not both.
	Logger Logger

	// MaxDataFrame caps a single DATA frame's payload (cornus fork), independent
	// of the (possibly large) send window, so the send loop never spends a long
	// uninterruptible write on one frame — bounding head-of-line delay for the
	// urgent control frames that must interleave. A larger Write fragments into
	// several frames. 0 means the default (defaultMaxDataFrame, 128 KiB).
	MaxDataFrame uint32

	// SchedulerMode selects the send scheduler (cornus fork): the default
	// strict-urgent + data-class WRR, urgent-only, or plain FIFO. See priority.go.
	SchedulerMode SchedulerMode

	// SendMode selects how frames are serialized to the wire (cornus fork): the
	// classic synchronous two-writes-per-frame path (SendSync), or the batched,
	// pipelined, single-write path (SendBatchedPipelined). See sendLoopBatched in
	// session.go. 0 == SendSync.
	SendMode SendMode

	// PipelineDepth bounds how many DATA frames a single stream may have queued but
	// not yet written on the batched-pipelined path (cornus fork), before its Write
	// blocks. It is the bufferbloat/burst guard: a small depth keeps the QoS
	// scheduler — not a fat downstream buffer — the place frames queue, so control
	// frames keep interleaving and bulk streams stay fair, while still hiding the
	// per-frame wire round-trip. Only used when SendMode == SendBatchedPipelined.
	// 0 == defaultPipelineDepth. Ignored on the synchronous path (implicitly depth 1).
	PipelineDepth int
}

func (c *Config) Clone() *Config {
	c2 := *c
	return &c2
}

// DefaultConfig is used to return a default configuration
func DefaultConfig() *Config {
	return &Config{
		AcceptBacklog:          256,
		EnableKeepAlive:        true,
		KeepAliveInterval:      30 * time.Second,
		ConnectionWriteTimeout: 10 * time.Second,
		MaxStreamWindowSize:    initialStreamWindow,
		StreamCloseTimeout:     5 * time.Minute,
		StreamOpenTimeout:      75 * time.Second,
		LogOutput:              os.Stderr,
		MaxDataFrame:           defaultMaxDataFrame,
		SchedulerMode:          SchedPriorityWRR,
		SendMode:               SendSync,
		PipelineDepth:          defaultPipelineDepth,
	}
}

// VerifyConfig is used to verify the sanity of configuration
func VerifyConfig(config *Config) error {
	if config.AcceptBacklog <= 0 {
		return fmt.Errorf("backlog must be positive")
	}
	if config.KeepAliveInterval == 0 {
		return fmt.Errorf("keep-alive interval must be positive")
	}
	if config.MaxStreamWindowSize < initialStreamWindow {
		return fmt.Errorf("MaxStreamWindowSize must be larger than %d", initialStreamWindow)
	}
	if config.LogOutput != nil && config.Logger != nil {
		return fmt.Errorf("both Logger and LogOutput may not be set, select one")
	} else if config.LogOutput == nil && config.Logger == nil {
		return fmt.Errorf("one of Logger or LogOutput must be set, select one")
	}
	return nil
}

// Server is used to initialize a new server-side connection.
// There must be at most one server-side connection. If a nil config is
// provided, the DefaultConfiguration will be used.
func Server(conn io.ReadWriteCloser, config *Config) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := VerifyConfig(config); err != nil {
		return nil, err
	}
	return newSession(config, conn, false), nil
}

// Client is used to initialize a new client-side connection.
// There must be at most one client-side connection.
func Client(conn io.ReadWriteCloser, config *Config) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if err := VerifyConfig(config); err != nil {
		return nil, err
	}
	return newSession(config, conn, true), nil
}
