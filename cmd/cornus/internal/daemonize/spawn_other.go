//go:build !unix

package daemonize

import "errors"

func Spawn(args []string, logPath string) (int, error) {
	return 0, errors.New("running as a background daemon requires a Unix host")
}

func SpawnAt(args []string, logPath, dir string, env []string) (int, error) {
	return 0, errors.New("running as a background daemon requires a Unix host")
}
