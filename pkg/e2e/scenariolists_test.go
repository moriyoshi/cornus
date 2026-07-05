package e2e

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The per-target scenario subsets are declared TWICE — once in the Makefile (for
// `make e2e-containerd` / `e2e-bare` / `e2e-incus` on a host) and once in
// e2e/container/entrypoint.sh (for the containerized runner, which is what CI
// actually executes). Nothing tied the two together, and they had silently
// drifted: registry-host-native-containerd.star was in the Makefile's
// SCENARIOS_CONTAINERD but missing from the entrypoint's CONTAINERD_SCENARIOS,
// so it never once ran in CI.
//
// This guards all three pairs the same way TestPredeclaredNamesInSync guards the
// harness's builtin sets: a plain Go test, so it runs in `go test ./...` on every
// PR with no new CI wiring.
var scenarioListPairs = []struct {
	makeVar  string // Makefile variable (`NAME := \` continuation list)
	shellVar string // entrypoint.sh bash array (`NAME=( ... )`)
}{
	{"SCENARIOS_PODMAN", "PODMAN_SCENARIOS"},
	{"SCENARIOS_PODMAN_ROOTLESS", "PODMAN_ROOTLESS_SCENARIOS"},
	{"SCENARIOS_CONTAINERD", "CONTAINERD_SCENARIOS"},
	{"SCENARIOS_BARE", "BARE_SCENARIOS"},
	{"SCENARIOS_INCUS", "INCUS_SCENARIOS"},
}

func TestScenarioSubsetsInSync(t *testing.T) {
	root := filepath.Join("..", "..")
	makefile := filepath.Join(root, "Makefile")
	entrypoint := filepath.Join(root, "e2e", "container", "entrypoint.sh")

	for _, pair := range scenarioListPairs {
		t.Run(pair.makeVar, func(t *testing.T) {
			want, err := parseMakeList(makefile, pair.makeVar)
			if err != nil {
				t.Fatalf("Makefile %s: %v", pair.makeVar, err)
			}
			got, err := parseShellArray(entrypoint, pair.shellVar)
			if err != nil {
				t.Fatalf("entrypoint.sh %s: %v", pair.shellVar, err)
			}

			for _, s := range diffSlices(want, got) {
				t.Errorf("Makefile %s lists %q but entrypoint.sh %s omits it "+
					"(CI runs the entrypoint list, so that scenario never runs)",
					pair.makeVar, s, pair.shellVar)
			}
			for _, s := range diffSlices(got, want) {
				t.Errorf("entrypoint.sh %s lists %q but Makefile %s omits it",
					pair.shellVar, s, pair.makeVar)
			}
			if !t.Failed() && strings.Join(want, "\n") != strings.Join(got, "\n") {
				t.Errorf("Makefile %s and entrypoint.sh %s hold the same scenarios in a different order:\nMakefile:\n  %s\nentrypoint.sh:\n  %s",
					pair.makeVar, pair.shellVar,
					strings.Join(want, "\n  "), strings.Join(got, "\n  "))
			}

			// A subset that parsed as empty means the parser lost track of the
			// declaration (renamed variable, reformatted list); that must fail
			// loudly rather than pass vacuously.
			if len(want) == 0 {
				t.Errorf("Makefile %s parsed as empty", pair.makeVar)
			}
			if len(got) == 0 {
				t.Errorf("entrypoint.sh %s parsed as empty", pair.shellVar)
			}

			// Every listed scenario must exist, in either list.
			for _, s := range append(append([]string{}, want...), got...) {
				if _, err := os.Stat(filepath.Join(root, s)); err != nil {
					t.Errorf("listed scenario %q does not exist: %v", s, err)
				}
			}
		})
	}
}

// diffSlices returns the elements of a that are absent from b.
func diffSlices(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}

// parseMakeList extracts a backslash-continued Makefile list variable, e.g.
//
//	SCENARIOS_CONTAINERD := \
//		e2e/scenarios/deploy.star \
//		e2e/scenarios/exec.star
func parseMakeList(path, name string) ([]string, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	for i, line := range lines {
		rest, ok := cutAssignment(line, name, ":=")
		if !ok {
			continue
		}
		var out []string
		for {
			cont := strings.HasSuffix(strings.TrimRight(rest, " \t"), `\`)
			out = append(out, strings.Fields(strings.TrimSuffix(strings.TrimRight(rest, " \t"), `\`))...)
			if !cont {
				break
			}
			i++
			if i >= len(lines) {
				break
			}
			rest = lines[i]
		}
		return out, nil
	}
	return nil, os.ErrNotExist
}

// parseShellArray extracts a multi-line bash array literal, e.g.
//
//	CONTAINERD_SCENARIOS=(
//	    e2e/scenarios/deploy.star
//	)
func parseShellArray(path, name string) ([]string, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	for i, line := range lines {
		rest, ok := cutAssignment(line, name, "=")
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "(") {
			continue
		}
		rest = strings.TrimSpace(rest)[1:]
		var out []string
		for {
			if idx := strings.Index(rest, "#"); idx >= 0 {
				rest = rest[:idx]
			}
			if idx := strings.Index(rest, ")"); idx >= 0 {
				out = append(out, strings.Fields(rest[:idx])...)
				return out, nil
			}
			out = append(out, strings.Fields(rest)...)
			i++
			if i >= len(lines) {
				return out, nil
			}
			rest = lines[i]
		}
	}
	return nil, os.ErrNotExist
}

// cutAssignment reports whether line assigns to name with the given operator
// (tolerating whitespace around it) and returns the right-hand side.
func cutAssignment(line, name, op string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, name) {
		return "", false
	}
	rest := strings.TrimLeft(trimmed[len(name):], " \t")
	if !strings.HasPrefix(rest, op) {
		return "", false
	}
	return rest[len(op):], true
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}
