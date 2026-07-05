//go:build !linux

package caretaker

import (
	"errors"
	"net"
)

// originalDst is unavailable off Linux (SO_ORIGINAL_DST is a Linux socket
// option). The enforcing proxy only runs inside linux pods; this stub keeps the
// package cross-compilable.
func originalDst(*net.TCPConn) (string, error) {
	return "", errors.New("SO_ORIGINAL_DST not supported on this platform")
}
