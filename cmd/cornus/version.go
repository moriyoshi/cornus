package main

import (
	"fmt"
	"net/http"
	"time"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

// VersionCmd prints the cornus version.
type VersionCmd struct{}

// Run prints the version.
func (c *VersionCmd) Run(cli *CLI) error {
	cli.out().Item("%s", version)
	return nil
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
