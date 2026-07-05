//go:build !linux

package netredirect

import "errors"

// Setup is unsupported off Linux (nftables is a Linux subsystem); the caretaker
// and the net-redirect subcommand only ever run inside a Linux pod/host netns.
func Setup(toPort, exemptUID, exemptMark int) error {
	return errors.New("netredirect: transparent redirect is only supported on linux")
}
