//go:build !linux

package caretaker

import "fmt"

// ensureLocalAddr is linux-only (it manipulates the loopback interface via
// netlink). The caretaker only runs in a linux pod; this stub keeps the package
// cross-compilable.
func ensureLocalAddr(ip string) error {
	return fmt.Errorf("well-known credential address binding is only supported on linux")
}
