//go:build !linux

package caretaker

import (
	"net"
	"syscall"
)

// markControl / markDialer are no-ops off linux: SO_MARK is a Linux socket
// option, and the caretaker only ever runs inside a linux pod. These keep the
// package cross-compilable.
func markControl(int) func(network, address string, c syscall.RawConn) error { return nil }

func markDialer(int) *net.Dialer { return &net.Dialer{} }
