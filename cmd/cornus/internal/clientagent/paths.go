package clientagent

import (
	"os"
	"path/filepath"

	"cornus/cmd/cornus/internal/agentproc"
	"cornus/pkg/conduithost"
)

// agentDir is where the single agent's socket/state/log live (per user,
// ephemeral). CORNUS_AGENT_DIR overrides it — used to isolate the agent (e.g. the
// E2E harness gives each scenario its own agent, and a user can run separate
// agents per project this way).
func agentDir() string {
	if d := os.Getenv("CORNUS_AGENT_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "cornus")
	}
	return filepath.Join(os.TempDir(), "cornus")
}

// Socket is the fixed control-socket path of the single agent.
func Socket() string { return filepath.Join(agentDir(), "agent.sock") }

// ConduitRegistry is the rendezvous directory for SOCKS5 conduits, beside the
// agent's own state so it inherits the same isolation: CORNUS_AGENT_DIR separates
// agents, and it must separate their conduits too, or two "isolated" agents would
// contend for one address.
//
// It is deliberately NOT under the agent's control socket path. A conduit outlives
// any one process that hosts it, agent or foreground session, so its rendezvous
// cannot be an agent-private detail.
func ConduitRegistry() *conduithost.Registry {
	return conduithost.NewRegistry(filepath.Join(agentDir(), "conduit"))
}

func statePath() string { return filepath.Join(agentDir(), "agent.json") }
func logPath() string   { return filepath.Join(agentDir(), "agent.log") }

// procSpec describes the agent instance for agentproc.
func procSpec() agentproc.Spec {
	return agentproc.Spec{
		Socket:    Socket(),
		StatePath: statePath(),
		LogPath:   logPath(),
		SpawnArgs: []string{"daemon", "agent"},
	}
}
