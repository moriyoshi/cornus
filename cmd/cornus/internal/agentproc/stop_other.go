//go:build !unix

package agentproc

import (
	"errors"
	"time"
)

func signalAndWait(_ int, _ time.Duration) error {
	return errors.New("stopping a background daemon requires a Unix host")
}
