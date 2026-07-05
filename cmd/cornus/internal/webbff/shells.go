package webbff

// Auto shell exec discovery: which interactive shell should a terminal on this
// workload actually launch?
//
// Before this, every terminal guessed /bin/sh. That is wrong at both ends of the
// range — an image that ships bash drops you into sh anyway, and a distroless
// image has no /bin/sh at all, so ExecStart fails and the browser shows a generic
// "Failed to start session." with no hint that the image simply has no shell.
//
// The answer is assembled from four sources and then MEASURED inside the running
// container, because a declared shell that is not in the image is worth exactly as
// little as a guessed one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"cornus/pkg/shells"
)

const (
	// shellProbeMarker is printed by shellProbeScript before anything else. It is
	// what separates "a shell ran my script" from "something exited 0" — a candidate
	// that is not a shell at all can still exit 0 (a wrapper, a no-op binary), and
	// without the marker its stdout would be read as a shell list.
	shellProbeMarker = "cornus-shells"

	// maxShellCandidates bounds the list a caller may post. Each candidate that is
	// NOT present costs one exec round trip, so an unbounded list is an unbounded
	// amount of work asked for by an unauthenticated browser.
	maxShellCandidates = 64

	// maxShellProbeCapture bounds one probe's captured output. The script prints one
	// short line per present candidate, so this is orders of magnitude more than any
	// honest answer needs.
	maxShellProbeCapture = 16 << 10

	// shellProbeBudget bounds the whole walk. A distroless image misses on every
	// candidate, and each miss is a round trip; without this a single request could
	// hold an exec-shaped conversation with the backend for as long as the list is.
	shellProbeBudget = 15 * time.Second

	// shellProbeTTL bounds how long a probe result is reused, matching fsopProbeTTL:
	// long enough that opening several panes on one workload does not re-probe,
	// short enough that a redeploy onto a different image is not written off.
	shellProbeTTL = 30 * time.Second
)

// shellProbeScript reports which of its arguments are executable files. It is run
// BY a candidate shell, so the first candidate that actually exists answers for
// every candidate in one exec rather than costing one round trip each.
//
// The trailing `exit 0` is load-bearing: `[ -x ]` failing on the last iteration
// would otherwise leave a non-zero $?, and a non-zero exit is how the caller
// recognises a candidate that did not run.
const shellProbeScript = `printf '` + shellProbeMarker + `\n'
for c in "$@"; do
  [ -x "$c" ] && printf '%s\n' "$c"
done
exit 0`

// shellProbe is one memoized probe result. found is the answer; an empty found
// with a fresh timestamp is a real answer ("this image has no shell"), not a miss.
type shellProbe struct {
	found [][]string
	at    time.Time
}

// shellsResult is what the discovery operation returns: the resolved probe order
// and the subset of it that the workload actually has, both as argv.
//
// Candidates travels with Found on purpose. The list is merged from four places,
// so "why did my terminal pick that?" is otherwise unanswerable from the browser;
// per-source attribution stays in the log, where it does not become API.
type shellsResult struct {
	Candidates [][]string `json:"candidates"`
	Found      [][]string `json:"found"`
}

// specShells returns the workload's own declared candidates and the shell implied
// by its entrypoint/command, from the LOADED compose plan.
//
// Both are available only for a workload in the loaded project: the server hands
// back no spec (api.DeployStatus carries no argv, and pkg/client has no inspect
// call), so for anything else this correctly answers nothing rather than guessing.
func (s *Server) specShells(workload string) (fromArgv []string, declared []string) {
	svc, ok := s.serviceByResource()[workload]
	if !ok {
		return nil, nil
	}
	plan := s.plans[svc]
	spec := plan.Spec
	if sh, ok := shells.FromArgv(spec.Entrypoint); ok {
		fromArgv = sh
	} else if sh, ok := shells.FromArgv(spec.Command); ok {
		fromArgv = sh
	}
	return fromArgv, plan.Shells
}

// contextShells returns the selected connection profile's candidate list. A
// failure to read the config is not an error here: the config is optional, and a
// terminal must still open from the browser's own list.
func (s *Server) contextShells() []string {
	if s.resolver == nil {
		return nil
	}
	f, err := s.resolver.LoadConfig()
	if err != nil || f == nil {
		return nil
	}
	_, ctx, err := f.Resolve(s.cfg.Context)
	if err != nil || ctx == nil {
		return nil
	}
	return ctx.Shells
}

// resolveShellCandidates builds the probe order: most specific source first, then
// dedupe keeping the first occurrence.
//
//  1. the shell the workload's own entrypoint/command names — the image author's
//     choice, and the one candidate we already have evidence for
//  2. the workload's declared `shells` (compose x-cornus-shells / deploy spec)
//  3. the selected connection context's `shells`
//  4. whatever the caller (the browser's settings) posted
//
// Concatenating rather than letting a specific source REPLACE the others is
// deliberate: a service that names one shell should rank it first, not strand the
// terminal when that shell turns out not to be in the image.
func (s *Server) resolveShellCandidates(workload string, client []string) [][]string {
	fromArgv, declared := s.specShells(workload)

	var out [][]string
	seen := map[string]bool{}
	add := func(argv []string) {
		if len(argv) == 0 {
			return
		}
		key := strings.Join(argv, "\x00")
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, argv)
	}
	addAll := func(list []string) {
		for _, c := range list {
			add(shells.Split(c))
		}
	}

	add(fromArgv)
	addAll(declared)
	addAll(s.contextShells())
	addAll(client)

	if len(out) > maxShellCandidates {
		out = out[:maxShellCandidates]
	}
	return out
}

// shellProbeCmd builds the argv that runs shellProbeScript under cand, testing
// each of argv0s.
//
// `sh -c SCRIPT NAME ARG...` puts NAME in $0 and the rest in "$@", so every
// candidate path travels as an ARGUMENT and is never part of the script text — a
// candidate spelled `"; rm -rf /` is a filename that does not exist, not shell
// source. Part of this list comes straight from a browser, so that is a
// requirement rather than a style choice (fs.go's listScriptCmd does the same).
func shellProbeCmd(cand []string, argv0s []string) []string {
	out := make([]string, 0, len(cand)+2+len(argv0s))
	out = append(out, cand...)
	out = append(out, "-c", shellProbeScript, "sh")
	return append(out, argv0s...)
}

// probeArgv0s is the path each candidate is TESTED at: argv[0], which for
// "/bin/busybox sh" is /bin/busybox.
func probeArgv0s(candidates [][]string) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c[0])
	}
	return out
}

// parseShellProbe turns one probe's stdout into the found subset, in the
// candidates' order. ok is false when the output did not come from the script —
// which is how a candidate that exited 0 without being a shell is rejected.
//
// Order is taken from candidates, never from the output: the resolved list IS the
// preference ranking, and a shell is free to print in whatever order it walks
// "$@".
func parseShellProbe(stdout string, candidates [][]string) ([][]string, bool) {
	lines := strings.Split(stdout, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != shellProbeMarker {
		return nil, false
	}
	present := map[string]bool{}
	for _, ln := range lines[1:] {
		if ln = strings.TrimRight(ln, "\r"); ln != "" {
			present[ln] = true
		}
	}
	found := [][]string{}
	for _, c := range candidates {
		if present[c[0]] {
			found = append(found, c)
		}
	}
	return found, true
}

// DiscoverShells reports which of the resolved candidates the workload actually
// has, most preferred first.
//
// An empty Found is a SUCCESSFUL answer — "this image has no shell" is a fact the
// caller must be able to act on (it prompts for a command instead), and it is not
// the same as "the probe could not run", which is an error.
func (s *Server) DiscoverShells(ctx context.Context, workload string, client []string) (shellsResult, error) {
	if workload == "" {
		return shellsResult{}, statusErr(http.StatusBadRequest, "workload is required")
	}
	if len(client) > maxShellCandidates {
		return shellsResult{}, statusErr(http.StatusBadRequest,
			"too many shell candidates (%d, max %d)", len(client), maxShellCandidates)
	}
	if err := s.ensureRunning(ctx, workload); err != nil {
		return shellsResult{}, err
	}
	candidates := s.resolveShellCandidates(workload, client)
	if len(candidates) == 0 {
		return shellsResult{Candidates: [][]string{}, Found: [][]string{}}, nil
	}

	key := shellProbeKey(workload, candidates)
	if found, ok := s.cachedShells(key); ok {
		return shellsResult{Candidates: candidates, Found: found}, nil
	}

	found, err := s.probeShells(ctx, workload, candidates)
	if err != nil {
		return shellsResult{}, err
	}
	s.rememberShells(key, found)
	return shellsResult{Candidates: candidates, Found: found}, nil
}

// probeShells walks the candidates until one runs the script, and returns what it
// reported. Nothing running is not an error: it is the distroless answer.
func (s *Server) probeShells(ctx context.Context, workload string, candidates [][]string) ([][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, shellProbeBudget)
	defer cancel()

	argv0s := probeArgv0s(candidates)
	for _, cand := range candidates {
		res, err := s.cfs.Exec(ctx, workload, "", shellProbeCmd(cand, argv0s), maxShellProbeCapture)
		if ctx.Err() != nil {
			// Out of budget with candidates still unprobed. Reporting an empty list
			// here would claim the image has no shell, which is a different fact and
			// the one the UI words as a dead end.
			return nil, statusErr(http.StatusGatewayTimeout,
				"shell discovery timed out after %s", shellProbeBudget)
		}
		if err != nil {
			continue // this candidate is not in the image, or would not start
		}
		// ExitKnown is not a formality: docker reports an exec Running for a moment
		// after its stdio closes and ExecInspect can fail outright, so an unknown
		// status must not be read as success (see ExecResult's contract).
		if !res.ExitKnown || res.ExitCode != 0 {
			continue
		}
		if found, ok := parseShellProbe(res.Stdout, candidates); ok {
			return found, nil
		}
	}
	return [][]string{}, nil
}

// shellProbeKey keys the cache by workload AND the resolved list. Keying on the
// workload alone would reuse an answer computed for a different set of candidates
// — silently, since both answers are well-formed.
func shellProbeKey(workload string, candidates [][]string) string {
	h := sha256.New()
	for _, c := range candidates {
		for _, w := range c {
			_, _ = io.WriteString(h, w)
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte{1})
	}
	return workload + "\x00" + hex.EncodeToString(h.Sum(nil))
}

func (s *Server) cachedShells(key string) ([][]string, bool) {
	s.shellsMu.Lock()
	defer s.shellsMu.Unlock()
	p, ok := s.shellsKnown[key]
	if !ok || time.Since(p.at) >= shellProbeTTL {
		return nil, false
	}
	return p.found, true
}

func (s *Server) rememberShells(key string, found [][]string) {
	s.shellsMu.Lock()
	defer s.shellsMu.Unlock()
	if s.shellsKnown == nil {
		s.shellsKnown = map[string]shellProbe{}
	}
	s.shellsKnown[key] = shellProbe{found: found, at: time.Now()}
}

// ---- HTTP surface -----------------------------------------------------------

// discoverShellsRequest carries the caller's own candidate list. Entries are
// command STRINGS ("/bin/busybox sh"), split server-side.
type discoverShellsRequest struct {
	Candidates []string `json:"candidates"`
}

func (s *Server) handleShellsDiscover(w http.ResponseWriter, r *http.Request) {
	var req discoverShellsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	out, err := s.DiscoverShells(r.Context(), r.PathValue("name"), req.Candidates)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}
