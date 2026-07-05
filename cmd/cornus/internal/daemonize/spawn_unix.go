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

// SpawnAt re-execs this binary like Spawn, but runs the child in working
// directory dir with environment env (both as captured on the ORIGINAL client),
// and APPENDS its stdio to logPath rather than truncating it. It exists for the
// `up -d --watch` reload: the background agent re-invokes the compose CLI to
// re-plan an edited project, and that re-invocation must see the same cwd and
// environment the developer originally ran in — ${VAR} interpolation, relative
// --env-file paths, KUBECONFIG, and CORNUS_AGENT_DIR (which selects WHICH agent
// is targeted) all depend on it, whereas the agent's own cwd/env are frozen at
// its first spawn. An empty dir keeps the inherited cwd; a nil env keeps the
// inherited environment. Returns the child PID.
func SpawnAt(args []string, logPath, dir string, env []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
