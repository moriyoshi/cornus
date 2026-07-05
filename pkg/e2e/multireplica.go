package e2e

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.starlark.net/starlark"
)

// This file adds the few builtins a MULTI-REPLICA scenario needs and the single
// harness cannot otherwise express: extra `cornus serve` replicas, a shared store
// they can agree through, long-lived spoke processes, and a TCP echo target.
//
// It exists because the hub's cross-replica plane was previously validated by two
// shell scripts outside the Starlark suite, and that cost real defects: the two
// drifted apart (one gained a JWT-only posture, the other did not) with nothing to
// catch it, because a script is not parse-checked, not resolve-checked, and not
// covered by TestPredeclaredNamesInSync or TestScenarioSubsetsInSync. Everything
// here is deliberately small — the point is to bring that plane under the same
// checks as the other 140 scenarios, not to grow a second harness.

// handle is a tiny attribute-only Starlark value. Builtins here return one instead
// of a bare string because a replica or a store has several coordinates worth
// naming (addr, url, data_dir) and positional tuples read terribly in a scenario.
type handle struct {
	kind  string
	attrs map[string]string
}

func (v *handle) String() string {
	// The most useful single coordinate, so `print(h)` and string interpolation
	// do something sensible rather than printing a Go pointer.
	for _, k := range []string{"addr", "url"} {
		if s, ok := v.attrs[k]; ok {
			return s
		}
	}
	return v.kind
}
func (v *handle) Type() string          { return v.kind }
func (v *handle) Freeze()               {}
func (v *handle) Truth() starlark.Bool  { return starlark.True }
func (v *handle) Hash() (uint32, error) { return starlark.String(v.String()).Hash() }

func (v *handle) AttrNames() []string {
	names := make([]string, 0, len(v.attrs))
	for k := range v.attrs {
		names = append(names, k)
	}
	return names
}

func (v *handle) Attr(name string) (starlark.Value, error) {
	if s, ok := v.attrs[name]; ok {
		return starlark.String(s), nil
	}
	return nil, nil // nil, nil means "no such attribute" to starlark-go
}

// serveReplica starts an ADDITIONAL cornus server beside the scenario's primary
// one. It deliberately does NOT touch h.server / h.client / h.registryHost: those
// singular fields are what every other builtin means by "the server", so a replica
// claiming them would silently retarget the whole scenario. The replica therefore
// gets its own data dir (keyed by name) and is reachable only through the returned
// handle, and it is stopped on teardown rather than by stop_server().
// addr may be empty to allocate one; a caller passes an explicit address when the
// replica's OWN url has to appear in its environment before it starts, which is
// exactly the hub case — CORNUS_HUB_FORWARD_URL is the address peers dial back on,
// so it cannot be discovered after the fact. Pair it with free_port().
func (h *Harness) serveReplica(name, storage, addr string, extraServeEnv map[string]string) (starlark.Value, error) {
	if name == "" {
		return nil, fmt.Errorf("serve: name must be non-empty for a replica")
	}
	if strings.ContainsAny(name, `/\ `) {
		return nil, fmt.Errorf("serve: replica name %q must be a single path element", name)
	}
	if addr == "" {
		var err error
		if addr, err = freePort(); err != nil {
			return nil, err
		}
	}
	data, err := h.dataDir("server-" + name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(h.ctx, h.cornusBin, "serve", "--addr", addr, "--storage", storage)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	cmd.Env = append(cmd.Env, "CORNUS_DATA="+data)
	// Scenario-supplied env wins, appended last — same precedence rule as serve().
	for k, v := range extraServeEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = h.out, h.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("serve replica %s: %w", name, err)
	}
	h.upstreamCleanups = append(h.upstreamCleanups, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if err := h.waitHealthy("http://" + addr + "/healthz"); err != nil {
		return nil, fmt.Errorf("serve replica %s: %w", name, err)
	}
	h.logf("✓ replica %s serving on %s", name, addr)
	return &handle{kind: "replica", attrs: map[string]string{
		"addr": addr, "name": name, "data_dir": data,
	}}, nil
}

// bRedis runs a real Redis in a container and returns a handle carrying the URL a
// cornus replica takes in CORNUS_HUB_REDIS. Real, not miniredis: the point of a
// multi-replica scenario is that two independent PROCESSES agree through a store
// neither of them owns, which an in-process fake cannot demonstrate.
func (h *Harness) bRedis(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	image := "redis:7-alpine"
	if err := starlark.UnpackArgs("redis", args, kwargs, "image?", &image); err != nil {
		return nil, err
	}
	addr, err := freePort()
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	name := "cornus-e2e-redis-" + port
	_ = exec.Command("docker", "rm", "-f", name).Run()
	run := exec.Command("docker", "run", "-d", "--name", name, "-p", port+":6379", image)
	if out, err := run.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("redis: docker run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	h.upstreamCleanups = append(h.upstreamCleanups, func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	// Wait for it to actually answer, not merely to have been created: a container
	// that exists but is not yet listening produces a replica that fails its first
	// write and a scenario that fails somewhere far away from the cause.
	ready := false
	for i := 0; i < 30; i++ {
		out, err := exec.Command("docker", "exec", name, "redis-cli", "ping").CombinedOutput()
		if err == nil && strings.Contains(string(out), "PONG") {
			ready = true
			break
		}
		time.Sleep(time.Second)
	}
	if !ready {
		return nil, fmt.Errorf("redis: %s did not answer PING within 30s", name)
	}
	h.logf("✓ redis on %s", addr)
	return &handle{kind: "redis", attrs: map[string]string{
		"addr": addr, "url": "redis://" + addr, "container": name,
	}}, nil
}

// bCornusBG runs a long-lived `cornus` process in the background and returns a
// handle. It is distinct from cornus() (which waits for exit) and cornus_stream()
// (which waits for a line then interrupts): a hub spoke has to stay up for the
// whole scenario, holding the connection the cross-replica forward routes to.
func (h *Harness) bCornusBG(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var cmdArgs []string
	for _, a := range args {
		s, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("cornus_bg: arguments must be strings")
		}
		cmdArgs = append(cmdArgs, s)
	}
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("cornus_bg: at least one argument is required")
	}
	var envv starlark.Value
	logName := "cornus-bg"
	if err := starlark.UnpackArgs("cornus_bg", nil, kwargs, "env?", &envv, "log?", &logName); err != nil {
		return nil, err
	}
	extraEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("cornus_bg: env: %w", err)
	}
	cmd := exec.CommandContext(h.ctx, h.cornusBin, cmdArgs...)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	logPath := filepath.Join(h.dataRoot, logName+".log")
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("cornus_bg: log: %w", err)
	}
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, fmt.Errorf("cornus_bg: %w", err)
	}
	h.upstreamCleanups = append(h.upstreamCleanups, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		lf.Close()
	})
	return &handle{kind: "process", attrs: map[string]string{"log": logPath}}, nil
}

// bTCPEcho listens on loopback and echoes every byte back. It is the delivery
// target a hub spoke registers, kept in-process so a multi-replica scenario needs
// no mounted helper binary and no socat.
func (h *Harness) bTCPEcho(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("tcp_echo", args, kwargs); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("tcp_echo: %w", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	h.upstreamCleanups = append(h.upstreamCleanups, func() { _ = ln.Close() })
	addr := ln.Addr().String()
	h.logf("✓ tcp echo on %s", addr)
	return &handle{kind: "echo", attrs: map[string]string{"addr": addr}}, nil
}

// dialEcho writes line to addr and returns the echoed reply. Scenarios use it to
// prove a cross-replica forward actually round-trips to the hosting spoke, rather
// than merely that a listener accepted the connection.
func (h *Harness) bDialEcho(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var addr, line string
	retry := 20
	if err := starlark.UnpackArgs("dial_echo", args, kwargs, "addr", &addr, "line", &line, "retry?", &retry); err != nil {
		return nil, err
	}
	var last error
	for i := 0; i < retry; i++ {
		got, err := dialEchoOnce(addr, line)
		if err == nil && got != "" {
			return starlark.String(got), nil
		}
		last = err
		time.Sleep(time.Second)
	}
	if last == nil {
		last = fmt.Errorf("empty reply")
	}
	return nil, fmt.Errorf("dial_echo %s: %w", addr, last)
}

func dialEchoOnce(addr, line string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, line+"\n"); err != nil {
		return "", err
	}
	buf := make([]byte, len(line)+8)
	n, err := c.Read(buf)
	if n > 0 {
		return strings.TrimSpace(string(buf[:n])), nil
	}
	return "", err
}
