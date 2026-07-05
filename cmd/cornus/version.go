package main

import (
	"fmt"
	"net/http"
	"time"

	"cornus/pkg/obsstore"
	"cornus/pkg/otelcollector"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

// VersionCmd prints the cornus version.
type VersionCmd struct {
	Features bool `kong:"name='features',help='Also report which optional, build-tagged features are compiled into this binary.'"`
}

// Run prints the version, optionally with the compiled-in feature set.
func (c *VersionCmd) Run(cli *CLI) error {
	if !c.Features {
		cli.out().Item("%s", version)
		return nil
	}
	// Renders aligned "key: value" text, or one JSON object under --output json,
	// so release pipelines and support requests can both read it. The names match
	// the build tags that switch each feature on, because the answer to "why is
	// this no?" is always "that tag was not in the build".
	return cli.out().KV().
		Add("version", version).
		Add("obsstore", yesNo(obsstore.Compiled())).
		Add("otelcollector", yesNo(otelcollector.Compiled())).
		Flush()
}

// yesNo renders a compiled-in flag as the same yes/no vocabulary the rest of the
// CLI's key/value output uses.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// HealthCmd probes a cornus server's /healthz endpoint and exits non-zero if
// it is not healthy. Used as a container healthcheck so no extra tools (curl)
// are needed in the image.
type HealthCmd struct {
	Addr string `kong:"name='addr',default='127.0.0.1:5000',help='Server address to probe.'"`
}

// Run performs the health probe.
func (c *HealthCmd) Run(cli *CLI) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + c.Addr + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
