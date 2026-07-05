//go:build unix

package conduithost

import "syscall"

// syscallConnRefused is the error a connect(2) to a socket path with no listener
// returns. It distinguishes a definitively dead host — whose advertisement may be
// reaped — from an ambiguous failure, which must not license stealing its address.
var syscallConnRefused error = syscall.ECONNREFUSED
