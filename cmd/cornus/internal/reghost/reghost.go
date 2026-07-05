// Package reghost resolves the cornus registry host that built images are
// tagged with. Reference classification/rewriting (bare vs registry-qualified)
// lives in cornus/pkg/imageref, which this package's callers use alongside
// Resolve.
package reghost

import (
	"context"

	"cornus/pkg/client"
)

// Resolve returns the "host[:port]" that built images are tagged with, applying
// the precedence: an explicit override > the server-advertised host (GET
// /.cornus/v1/info) > the client endpoint host (single-node quick start, where the
// endpoint host doubles as the registry).
func Resolve(ctx context.Context, c *client.Client, override string) string {
	if override != "" {
		return override
	}
	if info, err := c.Info(ctx); err == nil && info.RegistryHost != "" {
		return info.RegistryHost
	}
	return c.Host()
}
