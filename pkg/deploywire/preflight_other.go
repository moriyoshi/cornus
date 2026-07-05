//go:build !linux

package deploywire

import "errors"

func canMountLocal() error {
	return errors.New("deploywire: client-local mounts require Linux (kernel 9p)")
}
