package composecli

import "fmt"

// MountsCmd is retired. The per-project background mounts daemon was replaced by
// the single client agent (`cornus daemon agent`), which `cornus compose up -d`
// drives transparently. Kept as a hidden stub for one release so an old detached
// spawn (or muscle memory) gets a clear message instead of a missing command.
// The session machinery now lives in cmd/cornus/internal/clientagent.
type MountsCmd struct {
	ProjectName string `kong:"name='project-name',short='p',help='(obsolete)'"`
	Host        string `kong:"name='host',short='H',help='(obsolete)'"`
	Socket      string `kong:"name='socket',help='(obsolete)'"`
	Daemon      bool   `kong:"name='daemon',short='d',help='(obsolete)'"`
}

// Run reports that the command is obsolete.
func (c *MountsCmd) Run() error {
	return fmt.Errorf("`cornus daemon mounts` is obsolete: `cornus compose up -d` now uses the unified agent (`cornus daemon agent`); use `cornus daemon status` / `cornus daemon stop`")
}
