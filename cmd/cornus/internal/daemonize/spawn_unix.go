//go:build unix

package daemonize

import (
	"os"
	"os/exec"
	"syscall"
)

// Spawn re-execs this binary with args as a session-leader background
// process (setsid: survives the parent shell), with stdio redirected to
// logPath. Resolves the executable path explicitly rather than trusting
// os.Args[0]. Returns the child PID.
func Spawn(args []string, logPath string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
