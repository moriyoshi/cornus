//go:build windows

package conduithost

import "golang.org/x/sys/windows"

// syscallConnRefused is the error a connect to a socket with no listener returns.
//
// It must be WSAECONNREFUSED, not syscall.ECONNREFUSED. Go defines the latter on
// Windows only as a synthetic APPLICATION_ERROR constant that no socket call ever
// returns, and syscall.Errno.Is maps ErrPermission, ErrExist, ErrNotExist and
// ErrUnsupported but nothing for connection refusal — so matching on the portable
// spelling silently never fires here, and every dead host would be classified
// ambiguous. That failure is quiet in exactly the wrong way: the address stays
// blocked by a corpse until someone deletes the file by hand.
var syscallConnRefused error = windows.WSAECONNREFUSED
