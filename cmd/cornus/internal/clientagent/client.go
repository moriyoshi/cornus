package clientagent

import (
	"fmt"

	"cornus/cmd/cornus/internal/agentproc"
)

// EnsureRunning ensures the single background agent is running (spawning it,
// flock-serialized, if not) and returns its control socket path. Clients then
// Send up/down/docker-serve/etc. to that socket.
func EnsureRunning() (string, error) {
	sp := procSpec()
	if err := agentproc.EnsureRunning(sp, pingProto); err != nil {
		return "", err
	}
	return sp.Socket, nil
}

// RunProcess is the `cornus daemon agent` entry point: it runs the agent until
// SIGTERM/SIGINT or an idle-exit.
func RunProcess() error {
	return New(nil).Serve(procSpec())
}

// Stop stops a running agent (graceful stop request, falling back to a signal on
// the state-file pid). A no-op when none is running.
func Stop() error {
	return agentproc.Stop(procSpec(), func(socket string) error {
		resp, err := Send(socket, Request{Action: "stop"})
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("agent stop: %s", resp.Error)
		}
		return nil
	})
}

// Status returns the running agent's inventory, or (nil, nil) when none is
// running.
func Status() (*Inventory, error) {
	sock := Socket()
	if Ping(sock) == nil {
		return nil, nil
	}
	resp, err := Send(sock, Request{Action: "status"})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("agent status: %s", resp.Error)
	}
	return resp.Inventory, nil
}

// pingProto adapts Ping to agentproc.PingFunc.
func pingProto(socket string) (int, bool) {
	r := Ping(socket)
	if r == nil {
		return 0, false
	}
	return r.Protocol, true
}
