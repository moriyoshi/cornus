package composecli

import (
	"encoding/json"
	"fmt"
	"sort"

	"sigs.k8s.io/yaml"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
)

// Version is the Compose CLI version string. It is overridable at build time and
// propagated from the main package's build version (see main.go, which assigns
// composecli.Version = version before command dispatch).
var Version = "dev"

// ConfigCmd renders the resolved/merged Compose model, mirroring
// `docker compose config`.
//
// NOTE: cornus's compose types implement custom UnmarshalJSON decoders but no
// custom marshalers, so the dumped output is cornus's PARSED/MERGED view of the
// model, not a byte-faithful reserialization of the input file (and not docker's
// normalized output). Fields decoded through custom logic may render in their
// canonical Go-struct shape rather than their original file spelling.
type ConfigCmd struct {
	Services bool   `kong:"name='services',help='Print service names, one per line, in dependency order.'"`
	Volumes  bool   `kong:"name='volumes',help='Print top-level volume names, one per line, sorted.'"`
	Images   bool   `kong:"name='images',help='Print each service image, one per line, in dependency order.'"`
	Format   string `kong:"name='format',default='yaml',help='Output format for the full dump: yaml or json.'"`
	Quiet    bool   `kong:"name='quiet',short='q',help='Validate the model only; print nothing.'"`
}

// Run parses and validates the project (a parse error surfaces naturally), then
// renders the requested view.
func (c *ConfigCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	rt, err := cli.load(r, d)
	if err != nil {
		return err
	}
	defer rt.cleanup()

	switch {
	case c.Quiet:
		// Validation-only: load already parsed and validated the project.
		return nil
	case c.Services:
		for _, name := range rt.order {
			d.Item("%s", name)
		}
		return nil
	case c.Volumes:
		names := make([]string, 0, len(rt.project.Project().Volumes()))
		for name := range rt.project.Project().Volumes() {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			d.Item("%s", name)
		}
		return nil
	case c.Images:
		for _, name := range rt.order {
			img := rt.plans[name].Spec.Image
			if img == "" {
				continue
			}
			d.Item("%s", img)
		}
		return nil
	}

	// Render the active-profile shape (Services narrowed to what this run
	// selected), not the complete unfiltered model: `compose config` mirrors
	// `docker compose config`, which reflects the active --profile /
	// COMPOSE_PROFILES set.
	filtered := rt.project.Document()
	switch c.Format {
	case "yaml":
		out, err := yaml.Marshal(filtered)
		if err != nil {
			return err
		}
		_, err = d.Out().Write(out)
		return err
	case "json":
		out, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return err
		}
		if _, err := d.Out().Write(out); err != nil {
			return err
		}
		_, err = d.Out().Write([]byte("\n"))
		return err
	default:
		return fmt.Errorf("unsupported format %q (want yaml or json)", c.Format)
	}
}

// VersionCmd prints the Compose CLI version, mirroring
// `docker compose version`. It needs no server connection.
type VersionCmd struct {
	Short  bool   `kong:"name='short',help='Print just the bare version string.'"`
	Format string `kong:"name='format',default='pretty',help='Output format: pretty or json.'"`
}

// Run prints the version. The resolver and load path are intentionally unused;
// the signature matches the other subcommands for kong consistency.
func (c *VersionCmd) Run(cli *Cmd, r *clientconn.Resolver, d *cliout.Driver) error {
	if c.Short {
		d.Item("%s", Version)
		return nil
	}
	switch c.Format {
	case "pretty":
		d.Item("Cornus Compose version %s", Version)
		return nil
	case "json":
		out, err := json.Marshal(map[string]string{"version": Version})
		if err != nil {
			return err
		}
		if _, err := d.Out().Write(out); err != nil {
			return err
		}
		_, err = d.Out().Write([]byte("\n"))
		return err
	default:
		return fmt.Errorf("unsupported format %q (want pretty or json)", c.Format)
	}
}
