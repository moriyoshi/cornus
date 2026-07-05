package e2e

// remove_all: the harness replacement for `sh(cmd = "rm -rf ...")` in scenarios.
//
// SAFETY MODEL — read before changing anything here.
//
// An earlier attempt at this builtin guarded only against MALFORMED ARGUMENTS: it
// refused "", "/", ".", and bare relative names. That is the wrong model, and it
// failed catastrophically. Under that design the guard is the only thing standing
// between the harness and the entire filesystem, so any defect in the guard — or
// any test that exercises the guard by removing it — deletes everything the user
// can write. That is not hypothetical: neutralizing that guard while its tests fed
// it "/" and "." destroyed a developer's home directory and both project trees.
//
// This version is a SANDBOX instead. A path is deletable only if it resolves
// INSIDE a directory this harness itself created via temp_dir(), and those roots
// are recognized structurally — each must be an absolute path whose base name
// carries tempDirPrefix, the prefix only os.MkdirTemp in bTempDir produces. So the
// worst case of a guard defect is the loss of a scenario's own scratch directory
// under TMPDIR, not the user's data. The blast radius, not the guard's
// correctness, is what makes it safe.
//
// Two rules follow, and both are load-bearing:
//
//  1. resolveRemovable is PURE. It performs no deletion, so its tests can feed it
//     "/", "..", "$HOME" and symlink escapes with no consequence, and it can be
//     neutralized freely — the repo's testing rules require breaking a fix to
//     prove the tests observe it, and that is only safe when the code under test
//     has no irreversible effect.
//  2. NEVER neutralize bRemoveAll, and never write a test that drives it with a
//     path outside a t.TempDir(). Removing the guard IS the disaster; there is
//     nothing left to observe afterwards. Test the predicate, not the deleter.
//
// bRemoveAll deletes the path resolveRemovable RETURNS, never the caller's raw
// string, so a future bypass in argument handling cannot route around the check.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"
)

// tempDirPrefix is the os.MkdirTemp pattern bTempDir uses. It doubles as the
// sandbox's structural marker: resolveRemovable accepts a root ONLY if its base
// name carries this prefix, so a directory the harness did not mint can never
// become a deletion root — including through a future bug that registers one.
const tempDirPrefix = "cornus-e2e-scenario-"

// errNoRoots distinguishes "this scenario never called temp_dir()" from a path
// that merely fell outside the roots, because the remedy differs.
var errNoRoots = errors.New("no temp_dir() has been created in this scenario, so nothing is removable")

// resolveRemovable is the ENTIRE deletion decision, and it is deliberately pure:
// it returns the absolute path that may be removed, or an error explaining the
// refusal. It never removes anything.
//
// A path qualifies when, after symlink resolution, it lies AT or UNDER one of
// roots. Symlinks are resolved on the deepest EXISTING ancestor (the path itself
// need not exist — removing a missing path is a no-op, but it must still be a
// path the scenario would have been allowed to remove). Resolving matters: a
// scenario can create a symlink inside its own temp dir pointing anywhere, and a
// purely lexical check would happily accept temp/link/... while the real target
// is the user's home. Roots are resolved the same way, so a symlinked TMPDIR
// (/tmp -> /private/tmp on darwin) does not cause a spurious refusal.
//
// A symlink whose target lies outside is refused even when the link itself sits
// inside the sandbox, although removing it would only unlink the link. The
// resolution is uniform and the bias is toward refusal; a scenario needing that
// can fall back to sh().
func resolveRemovable(roots []string, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is empty")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q is not absolute; pass a path under a temp_dir()", path)
	}
	clean := filepath.Clean(path)

	resolvedRoots, err := sandboxRoots(roots)
	if err != nil {
		return "", err
	}

	target, err := resolveExisting(clean)
	if err != nil {
		return "", err
	}
	for _, root := range resolvedRoots {
		rel, err := filepath.Rel(root, target)
		if err != nil {
			continue
		}
		// rel == "." is the root itself, which the harness created and may
		// discard. Anything starting with ".." escaped the root.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return target, nil
	}
	return "", fmt.Errorf("path %q resolves to %q, which is outside every temp_dir() of this scenario", path, target)
}

// sandboxRoots validates and resolves the deletion roots. A root is accepted only
// if it is absolute and its base name carries tempDirPrefix — the structural
// guarantee that only temp_dir() output can ever authorize a deletion. Invalid
// roots are DROPPED rather than failing the call, so one bad entry cannot be used
// to deny service; if none survive, that is reported.
func sandboxRoots(roots []string) ([]string, error) {
	var out []string
	for _, r := range roots {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		clean := filepath.Clean(r)
		if !strings.HasPrefix(filepath.Base(clean), tempDirPrefix) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(clean)
		if err != nil {
			// A root that no longer exists cannot contain anything; skip it.
			continue
		}
		out = append(out, resolved)
	}
	if len(out) == 0 {
		return nil, errNoRoots
	}
	return out, nil
}

// resolveExisting resolves symlinks as far down path as actually exists, then
// re-appends the components that do not. filepath.EvalSymlinks fails outright on
// a missing path, which would make removing an already-absent path an error
// instead of the no-op it should be.
func resolveExisting(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	dir, base := filepath.Split(path)
	dir = filepath.Clean(dir)
	if dir == path || base == "" {
		// Reached the filesystem root without finding anything that exists.
		return path, nil
	}
	resolvedDir, err := resolveExisting(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedDir, base), nil
}

// bRemoveAll implements remove_all(path = ...). It removes the path
// resolveRemovable returned — never the caller's raw argument.
func (h *Harness) bRemoveAll(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackArgs("remove_all", args, kwargs, "path", &path); err != nil {
		return nil, err
	}
	target, err := resolveRemovable(h.tempRoots(), path)
	if err != nil {
		return nil, fmt.Errorf("remove_all: %w", err)
	}
	if err := os.RemoveAll(target); err != nil {
		return nil, fmt.Errorf("remove_all %s: %w", target, err)
	}
	return starlark.None, nil
}

// tempRoots returns the directories temp_dir() created for this scenario, which
// are the only places remove_all may delete.
func (h *Harness) tempRoots() []string {
	h.tempDirsMu.Lock()
	defer h.tempDirsMu.Unlock()
	return append([]string(nil), h.tempDirs...)
}
