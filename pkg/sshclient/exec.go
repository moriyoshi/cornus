package sshclient

import (
	"context"
	"fmt"
	"os/exec"
)

// Output runs one command on an SSH destination and returns its stdout. The
// pure-Go path uses a one-shot session on the same resolved Options as a tunnel;
// the binary path uses BatchMode and therefore honors the full ssh_config,
// including ProxyCommand and Match, without risking a background prompt.
func Output(ctx context.Context, destination string, opts Options, useBinary bool, command string) ([]byte, error) {
	if useBinary || opts.ProxyCommand != "" {
		cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", destination, command)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("ssh: run %q on %s: %w", command, destination, err)
		}
		return out, nil
	}
	dialer, err := Dial(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer dialer.Close()
	return dialer.output(ctx, command)
}

func (d *Dialer) output(ctx context.Context, command string) ([]byte, error) {
	client, _, err := d.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh: open command session: %w", err)
	}
	defer session.Close()
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := session.Output(command)
		done <- result{output: output, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return nil, fmt.Errorf("ssh: run %q: %w", command, result.err)
		}
		return result.output, nil
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	}
}
