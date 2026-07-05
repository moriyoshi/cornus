package wire

import "net"

// MeteredConn wraps a net.Conn and invokes OnRead / OnWrite with the byte count
// of each successful Read / Write. Either callback may be nil. It is kept
// callback-based (not tied to any metrics library) so the transport layer stays
// dependency-light: an OTel-aware caller supplies callbacks that record to a
// counter, while the wire package carries no telemetry dependency.
//
// It embeds the net.Conn interface rather than a concrete type, so io.Copy (used
// by pipe) cannot shortcut through ReadFrom / WriteTo — the Read / Write paths,
// and therefore the counters, always run.
type MeteredConn struct {
	net.Conn
	OnRead  func(int)
	OnWrite func(int)
}

func (m *MeteredConn) Read(p []byte) (int, error) {
	n, err := m.Conn.Read(p)
	if n > 0 && m.OnRead != nil {
		m.OnRead(n)
	}
	return n, err
}

func (m *MeteredConn) Write(p []byte) (int, error) {
	n, err := m.Conn.Write(p)
	if n > 0 && m.OnWrite != nil {
		m.OnWrite(n)
	}
	return n, err
}
