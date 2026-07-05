package sshclient

import (
	"crypto/ed25519"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSSHServer is an in-process SSH server for tests. It accepts a single
// authorized public key, services "direct-tcpip" channels by dialing the address
// the client requested (used to reach an echo listener), and counts accepted
// connections so tests can assert reconnect/single-flight behavior.
type fakeSSHServer struct {
	ln         net.Listener
	cfg        *ssh.ServerConfig
	accepts    atomic.Int64
	t          *testing.T
	mu         sync.Mutex
	conns      []net.Conn // accepted transport conns, for forced-drop tests
	directOnly bool       // if true, only serve direct-tcpip (reject sessions)
}

// newFakeSSHServer starts a fake SSH server authorizing clientPub, listening on
// 127.0.0.1:0. It returns the server and its host:port address.
func newFakeSSHServer(t *testing.T, clientPub ssh.PublicKey) (*fakeSSHServer, string) {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromSigner(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unauthorized key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSSHServer{ln: ln, cfg: cfg, t: t}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close(); s.closeConns() })
	return s, ln.Addr().String()
}

func (s *fakeSSHServer) serve() {
	for {
		nConn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.accepts.Add(1)
		s.mu.Lock()
		s.conns = append(s.conns, nConn)
		s.mu.Unlock()
		go s.handleConn(nConn)
	}
}

func (s *fakeSSHServer) handleConn(nConn net.Conn) {
	sConn, chans, reqs, err := ssh.NewServerConn(nConn, s.cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		_ = sConn.Wait()
	}()
	for newCh := range chans {
		if newCh.ChannelType() != "direct-tcpip" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		go s.handleDirectTCPIP(newCh)
	}
}

// directTCPIPExtra is the RFC 4254 §7.2 direct-tcpip channel-open payload.
type directTCPIPExtra struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

func (s *fakeSSHServer) handleDirectTCPIP(newCh ssh.NewChannel) {
	var req directTCPIPExtra
	if err := ssh.Unmarshal(newCh.ExtraData(), &req); err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	dest := net.JoinHostPort(req.DestHost, itoa(req.DestPort))
	upstream, err := net.Dial("tcp", dest)
	if err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, chReqs, err := newCh.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	go ssh.DiscardRequests(chReqs)
	go func() { _, _ = io.Copy(upstream, ch); _ = upstream.(*net.TCPConn).CloseWrite() }()
	go func() { _, _ = io.Copy(ch, upstream); _ = ch.Close() }()
}

// dropAll forcibly closes every accepted transport connection, simulating a tunnel
// drop. The client's Wait() then fires.
func (s *fakeSSHServer) dropAll() {
	s.closeConns()
}

func (s *fakeSSHServer) closeConns() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func itoa(p uint32) string {
	// small helper to avoid strconv import churn in tests
	if p == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for p > 0 {
		i--
		b[i] = byte('0' + p%10)
		p /= 10
	}
	return string(b[i:])
}

// startEcho starts a TCP echo listener and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(c, c); _ = c.Close() }(c)
		}
	}()
	return ln.Addr().String()
}
