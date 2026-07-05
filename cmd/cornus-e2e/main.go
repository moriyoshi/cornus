// Command cornus-e2e runs Starlark E2E scenarios against a Docker host, a
// containerd host, or a kind-managed Kubernetes cluster.
//
//	cornus-e2e --target docker     e2e/scenarios/deploy.star
//	cornus-e2e --target containerd e2e/scenarios/deploy.star
//	cornus-e2e --target bare       e2e/scenarios/deploy.star
//	cornus-e2e --target kube       e2e/scenarios/*.star
//	cornus-e2e --check             e2e/scenarios/*.star   # parse only
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"cornus/pkg/e2e"
	"cornus/pkg/logging"
)

// CLI is the runner's command line.
type CLI struct {
	Target              string   `kong:"name='target',default='docker',enum='docker,containerd,bare,kube,local',help='Deployment target: docker, containerd, bare (daemonless OCI runtime), kube (kind), or local (build-only, no external env).'"`
	Cornus              string   `kong:"name='cornus',default='cornus',help='Path to the cornus binary (also provides the compose client and Docker API proxy).',env='CORNUS_BIN'"`
	Storage             string   `kong:"name='storage',default='mem://',help='Registry storage backend for the test server.'"`
	Cluster             string   `kong:"name='cluster',default='cornus-e2e',help='kind cluster name (kube target).'"`
	Namespace           string   `kong:"name='namespace',default='cornus-e2e',help='Kubernetes namespace (kube target).'"`
	ContainerdAddress   string   `kong:"name='containerd-address',default='/run/containerd/containerd.sock',env='CORNUS_CONTAINERD_ADDRESS',help='containerd socket (containerd target).'"`
	ContainerdNamespace string   `kong:"name='containerd-namespace',default='cornus-e2e',help='containerd namespace (containerd target).'"`
	BareRuntime         string   `kong:"name='bare-runtime',env='CORNUS_BARE_RUNTIME',help='OCI runtime binary for the bare target (runc/crun/youki; default runc).'"`
	BareSnapshotter     string   `kong:"name='bare-snapshotter',env='CORNUS_BARE_SNAPSHOTTER',help='Snapshotter for the bare target (overlayfs/native; default auto).'"`
	Keep                bool     `kong:"name='keep',help='Do not tear down the kind cluster after running.'"`
	Check               bool     `kong:"name='check',help='Parse scenarios for syntax only; do not execute.'"`
	Preflight           bool     `kong:"name='preflight',help='Probe the environment for the tools/privileges the target + scenarios need, print a report, and exit.'"`
	SkipPreflight       bool     `kong:"name='skip-preflight',help='Skip the automatic preflight check before executing scenarios.'"`
	Scenarios           []string `kong:"arg,name='scenario',help='Scenario .star files.'"`
}

func (c *CLI) Run() error {
	if len(c.Scenarios) == 0 {
		return fmt.Errorf("no scenario files given")
	}

	if c.Check {
		// Parse-only path runs before the signal context exists; use background.
		ctx := context.Background()
		log := logging.FromContext(ctx)
		var bad int
		for _, s := range c.Scenarios {
			if err := e2e.Check(s); err != nil {
				log.ErrorContext(ctx, "scenario parse failed", "scenario", s, "error", err)
				bad++
			} else {
				fmt.Printf("✓ %s parses\n", s)
			}
		}
		if bad > 0 {
			return fmt.Errorf("%d scenario(s) failed to parse", bad)
		}
		return nil
	}

	target, err := buildTarget(c)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Preflight: verify the tools/privileges this target + these scenarios need
	// before provisioning anything. --preflight reports and exits; otherwise it
	// runs as a fail-fast gate (skippable with --skip-preflight).
	if c.Preflight || !c.SkipPreflight {
		results, perr := e2e.Preflight(ctx, target, c.Scenarios)
		if perr != nil {
			return fmt.Errorf("preflight: %w", perr)
		}
		fmt.Printf("== preflight (%s target, %d scenario(s)) ==\n%s\n", c.Target, len(c.Scenarios), e2e.FormatPreflight(results))
		if fail := e2e.FirstFailure(results); fail != nil {
			return fmt.Errorf("preflight failed: %s unavailable (%s) — %s", fail.Cap.Name(), fail.Detail, e2e.CapHint(fail.Cap))
		}
		if c.Preflight {
			fmt.Println("preflight OK")
			return nil
		}
	}

	fmt.Printf("== setting up %s target ==\n", c.Target)
	if err := target.Setup(ctx); err != nil {
		return fmt.Errorf("target setup: %w", err)
	}
	defer func() {
		fmt.Printf("== tearing down %s target ==\n", c.Target)
		teardownCtx := context.Background()
		if err := target.Teardown(teardownCtx); err != nil {
			logging.FromContext(teardownCtx).ErrorContext(teardownCtx, "teardown failed", "error", err)
		}
	}()

	var failures int
	log := logging.FromContext(ctx)
	for _, scenario := range c.Scenarios {
		fmt.Printf("\n== %s (%s) ==\n", scenario, c.Target)
		h := e2e.New(target, c.Cornus, c.Storage, os.Stdout)
		if err := h.RunFile(ctx, scenario); err != nil {
			log.ErrorContext(ctx, "scenario failed", "scenario", scenario, "error", err)
			failures++
			continue
		}
		fmt.Printf("✓ %s passed\n", scenario)
	}
	if failures > 0 {
		return fmt.Errorf("%d scenario(s) failed", failures)
	}
	fmt.Printf("\nall %d scenario(s) passed\n", len(c.Scenarios))
	return nil
}

func buildTarget(c *CLI) (e2e.Target, error) {
	switch c.Target {
	case "docker":
		return &e2e.DockerTarget{}, nil
	case "containerd":
		return &e2e.ContainerdTarget{Address: c.ContainerdAddress, Namespace: c.ContainerdNamespace}, nil
	case "bare":
		return &e2e.BareTarget{Runtime: c.BareRuntime, Snapshotter: c.BareSnapshotter}, nil
	case "kube":
		return &e2e.KubeTarget{Cluster: c.Cluster, Namespace: c.Namespace, Keep: c.Keep}, nil
	case "local":
		return &e2e.LocalTarget{}, nil
	default:
		return nil, fmt.Errorf("unknown target %q", c.Target)
	}
}

func main() {
	logging.Init()
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("cornus-e2e"),
		kong.Description("Starlark-powered E2E harness for cornus (Docker host + containerd host + kind)."),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(cli.Run())
}
