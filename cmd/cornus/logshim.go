package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/containerd/containerd/runtime/v2/logging"

	"cornus/pkg/deploy/containerdhost/logfmt"
)

// LogShimCmd is the hidden containerd binary logging driver. Its canonical home
// is `cornus daemon containerd-log-shim` (registered in daemon.go), but it also
// keeps a hidden top-level alias `cornus containerd-log-shim` (in main.go) that
// containerd actually invokes: containerd's process.NewBinaryCmd turns the
// binary log URI's single query param into a `key value` argv pair
// (binary:///path/to/cornus?containerd-log-shim=<path> -> argv
// ["containerd-log-shim", "<path>"]), and cannot address the nested `daemon`
// subcommand, so the top-level spelling must exist. The containerd shim execs it
// per task with the task's stdout/stderr FIFOs on fds 3/4 and a readiness pipe
// on fd 5 (the runtime/v2/logging protocol); the log file path arrives as the
// positional argument. The name must stay in sync with logShimArg in
// pkg/deploy/containerdhost.
type LogShimCmd struct {
	Path string `kong:"arg,required,help='Log file to append logfmt records to.'"`
}

// Run wires the task FIFOs into the logfmt JSON-lines file and never returns
// (logging.Run owns the process lifetime and exits itself).
func (c *LogShimCmd) Run(cli *CLI) error {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
			return fmt.Errorf("log shim: %w", err)
		}
		f, err := os.OpenFile(c.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("log shim: %w", err)
		}
		w := logfmt.NewWriter(f)
		var (
			wg             sync.WaitGroup
			outErr, errErr error
		)
		wg.Add(2)
		go func() { defer wg.Done(); outErr = w.Copy("stdout", cfg.Stdout, time.Now) }()
		go func() { defer wg.Done(); errErr = w.Copy("stderr", cfg.Stderr, time.Now) }()
		if err := ready(); err != nil {
			f.Close()
			return fmt.Errorf("log shim: signal ready: %w", err)
		}
		wg.Wait()
		return errors.Join(outErr, errErr, f.Sync(), f.Close())
	})
	return nil
}
