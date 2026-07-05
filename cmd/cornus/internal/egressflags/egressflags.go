// Package egressflags is the shared client-side-egress CLI surface, embedded by
// the commands that build a DeploySpec (cornus deploy and cornus compose up). It
// turns the flags into an api.EgressSpec and validates the resulting routing
// policy before it reaches a backend.
package egressflags

import (
	"fmt"
	"os"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/egresspolicy"
)

// Flags is the client-side-egress CLI surface. The zero value means "no egress
// flags given" and leaves any spec-file egress untouched.
type Flags struct {
	Mode    string   `kong:"name='egress',help='Route container egress through the client-side network: env (propagate proxy vars) | proxy (caretaker forward proxy) | transparent (nftables + relay).'"`
	Route   []string `kong:"name='egress-route',sep='none',help='Egress routing rule PATTERN=ROUTE (route: client|gateway|cluster|deny), first match wins. Repeatable.'"`
	Default string   `kong:"name='egress-default',help='Egress route for unmatched destinations: cluster (default) | client | gateway | deny.'"`
	PAC     string   `kong:"name='egress-pac',help='Path to a PAC-style JS file (FindProxyForURL) that decides egress routing; supersedes --egress-route.'"`
}

// Set reports whether any egress flag was provided.
func (f Flags) Set() bool {
	return f.Mode != "" || len(f.Route) > 0 || f.Default != "" || f.PAC != ""
}

// Apply merges the flags onto spec.Egress (creating it when any flag is set),
// validating modes/routes, reading a PAC file into Script, and confirming the
// resulting policy compiles. Flags override values already present from a spec
// file.
func (f Flags) Apply(spec *api.DeploySpec) error {
	if !f.Set() {
		return nil
	}
	if spec.Egress == nil {
		spec.Egress = &api.EgressSpec{}
	}
	e := spec.Egress
	if f.Mode != "" {
		switch f.Mode {
		case "env", "proxy", "transparent":
			e.Mode = f.Mode
		default:
			return fmt.Errorf("--egress: unknown mode %q (want env|proxy|transparent)", f.Mode)
		}
	}
	if f.Default != "" {
		if !egresspolicy.ValidRoute(f.Default) {
			return fmt.Errorf("--egress-default: invalid route %q", f.Default)
		}
		e.Default = f.Default
	}
	for _, r := range f.Route {
		pattern, routeName, ok := strings.Cut(r, "=")
		if !ok || pattern == "" {
			return fmt.Errorf("--egress-route: want PATTERN=ROUTE, got %q", r)
		}
		if !egresspolicy.ValidRoute(routeName) {
			return fmt.Errorf("--egress-route %q: invalid route %q", r, routeName)
		}
		e.Rules = append(e.Rules, api.EgressRule{Pattern: pattern, Route: routeName})
	}
	if f.PAC != "" {
		data, err := os.ReadFile(f.PAC)
		if err != nil {
			return fmt.Errorf("--egress-pac: %w", err)
		}
		e.Script = string(data)
	}
	// Reject values accepted syntactically but not yet implemented (e.g. a distinct
	// gateway URL), whether they came from a flag or the spec file.
	if err := e.Validate(); err != nil {
		return err
	}
	// Validate that the routing policy compiles (bad patterns / conflicting script
	// fail here, not at a backend). A script with no evaluator linked is allowed —
	// the backend/caretaker builds it.
	if _, err := egresspolicy.Compile(*e); err != nil && err != egresspolicy.ErrScriptUnsupported {
		return err
	}
	return nil
}
