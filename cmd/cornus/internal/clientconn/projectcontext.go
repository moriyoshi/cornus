package clientconn

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"cornus/pkg/clientconfig"
)

// projectOverride resolves the per-project context override to merge onto the
// selected context, or nil when there is none. It enforces the trust model that
// keeps an attacker-influenced file (a cloned repo, a shared/temp directory) from
// silently redirecting the endpoint, exfiltrating credentials, or weakening TLS:
//
//   - Provenance: a file with a tampering signal (a world-writable directory or a
//     foreign owner) is skipped unless --trust-context-file is set.
//   - Opt-in: an auto-discovered file may only contribute non-sensitive fields; its
//     credential/endpoint/TLS fields are dropped unless it was named explicitly
//     (--context-file) or --trust-context-file is set.
//   - Notice: whatever is applied is logged, naming the file and the fields.
//
// It returns the (possibly field-stripped) override and its path.
func (r *Resolver) projectOverride() (*clientconfig.Context, string, error) {
	path, explicit, err := r.locateProjectContext()
	if err != nil || path == "" {
		return nil, "", err
	}
	if !r.TrustProjectContext {
		if reason := untrustedProvenance(path); reason != "" {
			slog.Warn("ignoring project context file with untrusted provenance; re-run with --trust-context-file / CORNUS_TRUST_CONTEXT_FILE=1 to override",
				"path", path, "reason", reason)
			return nil, "", nil
		}
	}
	ov, err := clientconfig.LoadContextFile(path)
	if err != nil {
		return nil, "", err
	}
	all, sensitive := clientconfig.FieldNames(ov)
	if len(all) == 0 {
		return nil, "", nil
	}
	// Opt-in gate: a merely-discovered file cannot set credential/endpoint/TLS fields.
	if trusted := explicit || r.TrustProjectContext; !trusted && len(sensitive) > 0 {
		dropped := clientconfig.StripSensitive(ov)
		slog.Warn("ignoring sensitive fields from an auto-discovered project context file; re-run with --trust-context-file / CORNUS_TRUST_CONTEXT_FILE=1 to honor them",
			"path", path, "ignored", dropped)
		if all, _ = clientconfig.FieldNames(ov); len(all) == 0 {
			return nil, "", nil
		}
	}
	slog.Info("applying project context override", "path", path, "fields", all)
	return ov, path, nil
}

// locateProjectContext returns the override file path and whether the user named it
// explicitly (--context-file / CORNUS_CONTEXT_FILE) rather than it being auto-
// discovered. It resolves the --no-context-file toggle and its conflict with an
// explicit path here so projectOverride only deals with a concrete path.
func (r *Resolver) locateProjectContext() (path string, explicit bool, err error) {
	if r.NoProjectContext {
		if r.ProjectContextFile != "" {
			return "", false, fmt.Errorf("--context-file and --no-context-file are mutually exclusive")
		}
		return "", false, nil
	}
	if r.ProjectContextFile != "" {
		return r.ProjectContextFile, true, nil
	}
	p, err := r.discoverProjectContext()
	return p, false, err
}

// discoverProjectContext walks up from the working directory (workDir, or the
// process cwd) looking for one of clientconfig.ProjectContextNames, returning the
// first match. The walk is bounded: it stops after examining the user's home
// directory or a repository root (a directory holding .git), so a file dropped high
// in the filesystem cannot silently govern unrelated trees. It returns "" with no
// error when nothing is found.
func (r *Resolver) discoverProjectContext() (string, error) {
	dir := r.workDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	for {
		for _, name := range clientconfig.ProjectContextNames {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
		// Trust boundary: include the home dir and the repo root, but stop before
		// ascending past either. (The context file is checked above first, so one
		// sitting at the boundary directory is still found.)
		if dir == home {
			return "", nil
		}
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
