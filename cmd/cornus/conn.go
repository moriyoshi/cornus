package main

import (
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/clientconfig"
)

// resolver builds a clientconn.Resolver from the global --config-file / --context
// flags. One is also bound into kong (see main) so every command's Run can receive
// a *clientconn.Resolver; these helpers are for the top-level commands that already
// hold the *CLI.
func (c *CLI) resolver() *clientconn.Resolver {
	return &clientconn.Resolver{
		ConfigFile:          c.Config,
		Context:             c.Context,
		ProjectContextFile:  c.ContextFile,
		NoProjectContext:    c.NoContextFile,
		TrustProjectContext: c.TrustContextFile,
	}
}

func (c *CLI) resolveConn(explicitServer string) (*clientconn.Conn, error) {
	return c.resolver().Resolve(explicitServer)
}

func (c *CLI) requireConn(explicitServer string) (*clientconn.Conn, error) {
	return c.resolver().Require(explicitServer)
}

func (c *CLI) configPath() (string, error) { return c.resolver().ConfigPath() }

func (c *CLI) loadConfig() (*clientconfig.File, error) { return c.resolver().LoadConfig() }
