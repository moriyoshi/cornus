package main

import (
	"reflect"
	"testing"

	"github.com/alecthomas/kong"
)

// newTestParser builds a kong parser over a fresh CLI, as main() does.
func newTestParser(t *testing.T, cli *CLI) *kong.Kong {
	t.Helper()
	parser, err := kong.New(cli, kong.Name("cornus"))
	if err != nil {
		t.Fatal(err)
	}
	return parser
}

// TestDaemonSidecarAliases confirms the pod-facing sidecar commands parse
// identically under both spellings: the canonical `cornus daemon <cmd>` and
// the hidden top-level alias baked into generated pod specs
// (pkg/deploy/kubernetes). Both must dispatch to the same command struct
// (hence the same Run method) with the same flag values.
func TestDaemonSidecarAliases(t *testing.T) {
	cases := []struct {
		name string
		old  []string // top-level alias spelling (pod-spec compatible)
		new  []string // canonical `daemon`-grouped spelling
	}{
		{
			name: "caretaker",
			old:  []string{"caretaker", "--config", `{"mounts":[]}`},
			new:  []string{"daemon", "caretaker", "--config", `{"mounts":[]}`},
		},
		{
			name: "caretaker-check",
			old:  []string{"caretaker-check", "--config", `{"mounts":[]}`},
			new:  []string{"daemon", "caretaker-check", "--config", `{"mounts":[]}`},
		},
		{
			name: "net-redirect",
			old:  []string{"net-redirect", "--to-port", "15001", "--exempt-uid", "1337"},
			new:  []string{"daemon", "net-redirect", "--to-port", "15001", "--exempt-uid", "1337"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var oldCLI, newCLI CLI
			oldCtx, err := newTestParser(t, &oldCLI).Parse(tc.old)
			if err != nil {
				t.Fatalf("parse %v: %v", tc.old, err)
			}
			newCtx, err := newTestParser(t, &newCLI).Parse(tc.new)
			if err != nil {
				t.Fatalf("parse %v: %v", tc.new, err)
			}

			// Both spellings must select the same command type, i.e. the same
			// struct and the same Run method.
			oldTarget := oldCtx.Selected().Target
			newTarget := newCtx.Selected().Target
			if oldTarget.Type() != newTarget.Type() {
				t.Fatalf("command types differ: %v vs %v", oldTarget.Type(), newTarget.Type())
			}
			// And the parsed flag values must be identical.
			if !reflect.DeepEqual(oldTarget.Interface(), newTarget.Interface()) {
				t.Errorf("parsed values differ:\n old: %+v\n new: %+v",
					oldTarget.Interface(), newTarget.Interface())
			}
		})
	}
}

// TestSidecarAliasVisibility confirms the top-level sidecar aliases are hidden
// from `cornus --help` while the `daemon`-grouped commands are visible.
func TestSidecarAliasVisibility(t *testing.T) {
	var cli CLI
	parser := newTestParser(t, &cli)

	nodes := map[string]*kong.Node{}
	var daemon *kong.Node
	for _, n := range parser.Model.Children {
		nodes[n.Name] = n
		if n.Name == "daemon" {
			daemon = n
		}
	}
	for _, name := range []string{"caretaker", "caretaker-check", "net-redirect"} {
		n, ok := nodes[name]
		if !ok {
			t.Errorf("top-level alias %q missing", name)
			continue
		}
		if !n.Hidden {
			t.Errorf("top-level alias %q is visible in help; want hidden", name)
		}
	}

	if daemon == nil {
		t.Fatal("daemon command missing")
	}
	sub := map[string]*kong.Node{}
	for _, n := range daemon.Children {
		sub[n.Name] = n
	}
	for _, name := range []string{"caretaker", "caretaker-check", "net-redirect"} {
		n, ok := sub[name]
		if !ok {
			t.Errorf("daemon %s missing", name)
			continue
		}
		if n.Hidden {
			t.Errorf("daemon %s is hidden; want visible", name)
		}
	}
}
