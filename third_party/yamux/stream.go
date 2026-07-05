// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package yamux

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type streamState int

const (
	streamInit streamState = iota
	streamSYNSent
	streamSYNReceived
	streamEstablished
	streamLocalClose
	streamRemoteClose
	streamClosed
	streamReset
)

// Stream is used to represent a logical stream
// within a session.
type Stream struct {
	recvWindow uint32
	sendWindow uint32

	// maxWindow is this stream's own receive-window ceiling (cornus fork): the
	// recv window grows via window updates up to here instead of the session-wide
	// config.MaxStreamWindowSize, so a bulk mount can get a large window without
	// forcing it on every stream. sendClass is this stream's QoS send class for
	// DATA frames (see priority.go). Both are set right after open via SetMaxWindow
	// / SetPriority, before traffic flows.
	maxWindow uint32
	sendClass uint8

	id      uint32
	session *Session

	state     streamState
	stateLock sync.Mutex

	recvBuf  *bytes.Buffer
	recvLock sync.Mutex

	controlHdr     header
	controlErr     chan error
	controlHdrLock sync.Mutex

	sendHdr  header
	sendErr  chan error
	sendLock sync.Mutex

	recvNotifyCh chan struct{}
	sendNotifyCh chan struct{}

	// sendInflight counts DATA frames queued but not yet written on the
	// batched-pipelined send path (cornus fork); sendPipeCh wakes a Write blocked on
	// the per-stream PipelineDepth cap when the send loop writes one of this stream's
	// frames. Unused on the synchronous path.
	sendInflight int32
	sendPipeCh   chan struct{}

	readDeadline  atomic.Value // time.Time
	writeDeadline atomic.Value // time.Time

	// establishCh is notified if the stream is established or being closed.
	establishCh chan struct{}

	// closeTimer is set with stateLock held to honor the StreamCloseTimeout
	// setting on Session.
	closeTimer *time.Timer
}

// newStream is used to construct a new stream within
// a given session for an ID
func newStream(session *Session, id uint32, state streamState) *Stream {
	s := &Stream{
		id:           id,
		session:      session,
		state:        state,
		controlHdr:   header(make([]byte, headerSize)),
		controlErr:   make(chan error, 1),
		sendHdr:      header(make([]byte, headerSize)),
		sendErr:      make(chan error, 1),
		recvWindow:   initialStreamWindow,
		sendWindow:   initialStreamWindow,
		maxWindow:    session.config.MaxStreamWindowSize,
		sendClass:    ClassNormal,
		recvNotifyCh: make(chan struct{}, 1),
		sendNotifyCh: make(chan struct{}, 1),
		sendPipeCh:   make(chan struct{}, 1),
		establishCh:  make(chan struct{}, 1),
	}
	s.readDeadline.Store(time.Time{})
	s.writeDeadline.Store(time.Time{})
	return s
}

// Session returns the associated stream session
func (s *Stream) Session() *Session {
	return s.session
}

// StreamID returns the ID of this stream
func (s *Stream) StreamID() uint32 {
	return s.id
}

// SetPriority sets this stream's QoS send class for DATA frames (cornus fork):
// one of ClassBulk, ClassNormal (default), or ClassHigh (ClassUrgent is reserved
// for session control frames, so it is clamped to ClassHigh). Session control
// frames (window updates, FIN, RST) are always ClassUrgent regardless. Call it
// right after opening/accepting the stream, before any Write — the field is not
// synchronized, on the assumption it is fixed during stream setup.
func (s *Stream) SetPriority(class uint8) {
	if class >= ClassUrgent {
		class = ClassHigh
	}
	s.sendClass = class
}

// Priority returns this stream's QoS send class (cornus fork); the getter paired
// with SetPriority, for tests and observability.
func (s *Stream) Priority() uint8 { return s.sendClass }

// SetMaxWindow sets this stream's receive-window ceiling (cornus fork): the recv
// window grows via window updates up to n bytes as data is consumed, instead of
// the session-wide config.MaxStreamWindowSize. n below the initial 256 KiB window
// is clamped up to it. Call it right after open, before traffic; it takes effect
// as the window next grows. Both peers must set it for a stream to actually carry
// a large window (each side advertises its own recv ceiling to the other).
func (s *Stream) SetMaxWindow(n uint32) {
	if n < initialStreamWindow {
		n = initialStreamWindow
	}
	s.maxWindow = n
}

// Read is used to read from the stream
func (s *Stream) Read(b []byte) (n int, err error) {
	defer asyncNotify(s.recvNotifyCh)
START:

	// If the stream is closed and there's no data buffered, return EOF
	s.stateLock.Lock()
	switch s.state {
	case streamLocalClose:
		// LocalClose only prohibits further local writes. Handle reads normally.
	case streamRemoteClose:
		fallthrough
	case streamClosed:
		s.recvLock.Lock()
		if s.recvBuf == nil || s.recvBuf.Len() == 0 {
			s.recvLock.Unlock()
			s.stateLock.Unlock()
			return 0, io.EOF
		}
		s.recvLock.Unlock()
	case streamReset:
		s.stateLock.Unlock()
		return 0, ErrConnectionReset
	}
	s.stateLock.Unlock()

	// If there is no data available, block
	s.recvLock.Lock()
	if s.recvBuf == nil || s.recvBuf.Len() == 0 {
		s.recvLock.Unlock()
		goto WAIT
	}

	// Read any bytes
	n, _ = s.recvBuf.Read(b)
	s.recvLock.Unlock()

	// Send a window update potentially
	err = s.sendWindowUpdate()
	if err == ErrSessionShutdown {
		err = nil
	}
	return n, err

WAIT:
	var timeout <-chan time.Time
	var timer *time.Timer
	readDeadline := s.readDeadline.Load().(time.Time)
	if !readDeadline.IsZero() {
		delay := time.Until(readDeadline)
		timer = time.NewTimer(delay)
		timeout = timer.C
	}
	select {
	case <-s.session.shutdownCh:
	case <-s.recvNotifyCh:
	case <-timeout:
		return 0, ErrTimeout
	}
	if timer != nil {
		if !timer.Stop() {
			<-timeout
		}
	}
	goto START
}

// Write is used to write to the stream
func (s *Stream) Write(b []byte) (n int, err error) {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	total := 0
	for total < len(b) {
		n, err := s.write(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// write is used to write to the stream, may return on
// a short write.
func (s *Stream) write(b []byte) (n int, err error) {
	var flags uint16
	var max uint32
	var body []byte
START:
	s.stateLock.Lock()
	switch s.state {
	case streamLocalClose:
		fallthrough
	case streamClosed:
		s.stateLock.Unlock()
		return 0, ErrStreamClosed
	case streamReset:
		s.stateLock.Unlock()
		return 0, ErrConnectionReset
	}
	s.stateLock.Unlock()

	// If there is no data available, block
	window := atomic.LoadUint32(&s.sendWindow)
	if window == 0 {
		goto WAIT
	}

	// Determine the flags if any
	flags = s.sendFlags()

	// Send up to our send window, but cap a single frame (cornus fork) so the
	// send loop cannot spend a long uninterruptible write on one frame — larger
	// writes fragment, letting urgent control frames interleave sooner. The cap is
	// per-session (Config.MaxDataFrame; 0 => the default). The cap variable is
	// block-scoped so `goto WAIT` above does not jump over a declaration.
	max = min(window, uint32(len(b)))
	{
		frameCap := s.session.config.MaxDataFrame
		if frameCap == 0 {
			frameCap = defaultMaxDataFrame
		}
		if max > frameCap {
			max = frameCap
		}
	}
	body = b[:max]

	// Send the frame. On the batched-pipelined path (cornus fork) the frame is
	// copied into a pooled contiguous buffer and queued without waiting for the
	// wire, so a single stream keeps multiple frames in flight; on the classic path
	// the writer blocks until the send loop has written this one frame.
	if s.session.config.SendMode == SendBatchedPipelined {
		if err = s.session.sendDataAsync(s, flags, body); err != nil {
			return 0, err
		}
	} else {
		s.sendHdr.encode(typeData, flags, s.id, max)
		if err = s.session.waitForSendErr(s.sendHdr, body, s.sendErr, s.sendClass); err != nil {
			if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrConnectionWriteTimeout) {
				// Message left in ready queue, header re-use is unsafe.
				s.sendHdr = header(make([]byte, headerSize))
			}
			return 0, err
		}
	}

	// Reduce our send window
	atomic.AddUint32(&s.sendWindow, ^uint32(max-1))

	// Unlock
	return int(max), err

WAIT:
	var timeout <-chan time.Time
	var timer *time.Timer
	writeDeadline := s.writeDeadline.Load().(time.Time)
	if !writeDeadline.IsZero() {
		delay := time.Until(writeDeadline)
		timer = time.NewTimer(delay)
		timeout = timer.C
	}
	select {
	case <-s.session.shutdownCh:
	case <-s.sendNotifyCh:
	case <-timeout:
		return 0, ErrTimeout
	}
	if timer != nil {
		if !timer.Stop() {
			<-timeout
		}
	}
	goto START
}

// sendFlags determines any flags that are appropriate
// based on the current stream state
func (s *Stream) sendFlags() uint16 {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	var flags uint16
	switch s.state {
	case streamInit:
		flags |= flagSYN
		s.state = streamSYNSent
	case streamSYNReceived:
		flags |= flagACK
		s.state = streamEstablished
	}
	return flags
}

// sendWindowUpdate potentially sends a window update enabling
// further writes to take place. Must be invoked with the lock.
func (s *Stream) sendWindowUpdate() error {
	s.controlHdrLock.Lock()
	defer s.controlHdrLock.Unlock()

	// Determine the delta update (grow up to this stream's own window ceiling)
	max := s.maxWindow
	var bufLen uint32
	s.recvLock.Lock()
	if s.recvBuf != nil {
		bufLen = uint32(s.recvBuf.Len())
	}
	delta := (max - bufLen) - s.recvWindow

	// Determine the flags if any
	flags := s.sendFlags()

	// Check if we can omit the update
	if delta < (max/2) && flags == 0 {
		s.recvLock.Unlock()
		return nil
	}

	// Update our window
	s.recvWindow += delta
	s.recvLock.Unlock()

	// Send the header
	s.controlHdr.encode(typeWindowUpdate, flags, s.id, delta)
	if err := s.session.waitForSendErr(s.controlHdr, nil, s.controlErr, ClassUrgent); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrConnectionWriteTimeout) {
			// Message left in ready queue, header re-use is unsafe.
			s.controlHdr = header(make([]byte, headerSize))
		}
		return err
	}
	return nil
}

// sendClose is used to send a FIN
func (s *Stream) sendClose() error {
	s.controlHdrLock.Lock()
	defer s.controlHdrLock.Unlock()

	flags := s.sendFlags()
	flags |= flagFIN
	s.controlHdr.encode(typeWindowUpdate, flags, s.id, 0)
	// The FIN is order-sensitive: it must not overtake this stream's own DATA. On
	// the synchronous path a Write is already on the wire before Close runs, so the
	// FIN can go out strict-urgent. On the batched-pipelined path a stream's DATA
	// frames may still be queued when Close runs, so send the FIN in the stream's
	// OWN send class — same per-class FIFO as its data — instead of ClassUrgent, so
	// it stays ordered behind them (cornus fork). RST (forceClose) intentionally
	// stays urgent: it is a hard drop of any pending data.
	finClass := uint8(ClassUrgent)
	if s.session.config.SendMode == SendBatchedPipelined {
		finClass = s.sendClass
	}
	if err := s.session.waitForSendErr(s.controlHdr, nil, s.controlErr, finClass); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrConnectionWriteTimeout) {
			// Message left in ready queue, header re-use is unsafe.
			s.controlHdr = header(make([]byte, headerSize))
		}
		return err
	}
	return nil
}

// Close is used to close the stream
func (s *Stream) Close() error {
	closeStream := false
	s.stateLock.Lock()
	switch s.state {
	// Opened means we need to signal a close
	case streamSYNSent:
		fallthrough
	case streamSYNReceived:
		fallthrough
	case streamEstablished:
		s.state = streamLocalClose
		goto SEND_CLOSE

	case streamLocalClose:
	case streamRemoteClose:
		s.state = streamClosed
		closeStream = true
		goto SEND_CLOSE

	case streamClosed:
	case streamReset:
	default:
		panic("unhandled state")
	}
	s.stateLock.Unlock()
	return nil
SEND_CLOSE:
	// This shouldn't happen (the more realistic scenario to cancel the
	// timer is via processFlags) but just in case this ever happens, we
	// cancel the timer to prevent dangling timers.
	if s.closeTimer != nil {
		s.closeTimer.Stop()
		s.closeTimer = nil
	}

	// If we have a StreamCloseTimeout set we start the timeout timer.
	// We do this only if we're not already closing the stream since that
	// means this was a graceful close.
	//
	// This prevents memory leaks if one side (this side) closes and the
	// remote side poorly behaves and never responds with a FIN to complete
	// the close. After the specified timeout, we clean our resources up no
	// matter what.
	if !closeStream && s.session.config.StreamCloseTimeout > 0 {
		s.closeTimer = time.AfterFunc(
			s.session.config.StreamCloseTimeout, s.closeTimeout)
	}

	s.stateLock.Unlock()
	s.sendClose()
	s.notifyWaiting()
	if closeStream {
		s.session.closeStream(s.id)
	}
	return nil
}

// closeTimeout is called after StreamCloseTimeout during a close to
// close this stream.
func (s *Stream) closeTimeout() {
	// Close our side forcibly
	s.forceClose()

	// Free the stream from the session map
	s.session.closeStream(s.id)

	// Send a RST so the remote side closes too.
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeWindowUpdate, flagRST, s.id, 0)
	_ = s.session.sendNoWait(hdr)
}

// forceClose is used for when the session is exiting
func (s *Stream) forceClose() {
	s.stateLock.Lock()
	s.state = streamClosed
	s.stateLock.Unlock()
	s.notifyWaiting()
}

// processFlags is used to update the state of the stream
// based on set flags, if any. Lock must be held
func (s *Stream) processFlags(flags uint16) error {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	// Close the stream without holding the state lock
	closeStream := false
	defer func() {
		if closeStream {
			if s.closeTimer != nil {
				// Stop our close timeout timer since we gracefully closed
				s.closeTimer.Stop()
			}

			s.session.closeStream(s.id)
		}
	}()

	if flags&flagACK == flagACK {
		if s.state == streamSYNSent {
			s.state = streamEstablished
		}
		asyncNotify(s.establishCh)
		s.session.establishStream(s.id)
	}
	if flags&flagFIN == flagFIN {
		switch s.state {
		case streamSYNSent:
			fallthrough
		case streamSYNReceived:
			fallthrough
		case streamEstablished:
			s.state = streamRemoteClose
			s.notifyWaiting()
		case streamLocalClose:
			s.state = streamClosed
			closeStream = true
			s.notifyWaiting()
		default:
			s.session.logger.Printf("[ERR] yamux: unexpected FIN flag in state %d", s.state)
			return ErrUnexpectedFlag
		}
	}
	if flags&flagRST == flagRST {
		s.state = streamReset
		closeStream = true
		s.notifyWaiting()
	}
	return nil
}

// notifyWaiting notifies all the waiting channels
func (s *Stream) notifyWaiting() {
	asyncNotify(s.recvNotifyCh)
	asyncNotify(s.sendNotifyCh)
	asyncNotify(s.establishCh)
}

// incrSendWindow updates the size of our send window
func (s *Stream) incrSendWindow(hdr header, flags uint16) error {
	if err := s.processFlags(flags); err != nil {
		return err
	}

	// Increase window, unblock a sender
	atomic.AddUint32(&s.sendWindow, hdr.Length())
	asyncNotify(s.sendNotifyCh)
	return nil
}

// readData is used to handle a data frame
func (s *Stream) readData(hdr header, flags uint16, conn io.Reader) error {
	if err := s.processFlags(flags); err != nil {
		return err
	}

	// Check that our recv window is not exceeded
	length := hdr.Length()
	if length == 0 {
		return nil
	}

	// Wrap in a limited reader
	conn = &io.LimitedReader{R: conn, N: int64(length)}

	// Copy into buffer
	s.recvLock.Lock()

	if length > s.recvWindow {
		s.session.logger.Printf("[ERR] yamux: receive window exceeded (stream: %d, remain: %d, recv: %d)", s.id, s.recvWindow, length)
		s.recvLock.Unlock()
		return ErrRecvWindowExceeded
	}

	if s.recvBuf == nil {
		// Allocate the receive buffer just-in-time to fit the full data frame.
		// This way we can read in the whole packet without further allocations.
		s.recvBuf = bytes.NewBuffer(make([]byte, 0, length))
	}
	copiedLength, err := io.Copy(s.recvBuf, conn)
	if err != nil {
		s.session.logger.Printf("[ERR] yamux: Failed to read stream data: %v", err)
		s.recvLock.Unlock()
		return err
	}

	// Decrement the receive window
	s.recvWindow -= uint32(copiedLength)
	s.recvLock.Unlock()

	// Unblock any readers
	asyncNotify(s.recvNotifyCh)
	return nil
}

// SetDeadline sets the read and write deadlines
func (s *Stream) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	if err := s.SetWriteDeadline(t); err != nil {
		return err
	}
	return nil
}

// SetReadDeadline sets the deadline for blocked and future Read calls.
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.readDeadline.Store(t)
	asyncNotify(s.recvNotifyCh)
	return nil
}

// SetWriteDeadline sets the deadline for blocked and future Write calls
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Store(t)
	asyncNotify(s.sendNotifyCh)
	return nil
}

// Shrink is used to compact the amount of buffers utilized
// This is useful when using Yamux in a connection pool to reduce
// the idle memory utilization.
func (s *Stream) Shrink() {
	s.recvLock.Lock()
	if s.recvBuf != nil && s.recvBuf.Len() == 0 {
		s.recvBuf = nil
	}
	s.recvLock.Unlock()
}
