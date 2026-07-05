// Package lineage gathers the client-side provenance of a deploy — the machine,
// OS user, directory, and git repository the deploy was spawned from — so the
// cornus server can record where each workload came from. Every field is
// best-effort: a value that cannot be determined is simply left empty and never
// blocks a deploy. The authenticated Subject is deliberately NOT set here; the
// server stamps it from the request identity.
package lineage

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"cornus/pkg/api"
)

// gitTimeout bounds each git subprocess so a slow or wedged repository never
// stalls a deploy.
const gitTimeout = 3 * time.Second

// Collect returns the client-attested origin for a deploy launched from dir
// (the Compose file's directory, or the working directory for a raw
// `deploy -f`). It never returns nil: at minimum the host is reported. dir is
// resolved to an absolute path; an empty dir falls back to the process working
// directory. Subject is left empty for the server to stamp.
func Collect(dir string) *api.Origin {
	o := &api.Origin{}
	if h, err := os.Hostname(); err == nil {
		o.Host = h
	}
	o.User = currentUser()
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		o.Directory = dir
		o.Git = gitOrigin(dir)
	}
	return o
}

// currentUser reports the OS user running the client, preferring the resolved
// account name and falling back to the USER/USERNAME environment.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return os.Getenv("USERNAME")
}

// gitOrigin returns the git provenance of dir, or nil when dir is not inside a
// git working tree (or git is unavailable). All probes are best-effort.
func gitOrigin(dir string) *api.GitOrigin {
	if strings.TrimSpace(git(dir, "rev-parse", "--is-inside-work-tree")) != "true" {
		return nil
	}
	g := &api.GitOrigin{
		Remote: git(dir, "config", "--get", "remote.origin.url"),
		Commit: git(dir, "rev-parse", "HEAD"),
	}
	// --abbrev-ref prints "HEAD" on a detached HEAD; treat that as no branch.
	if b := git(dir, "rev-parse", "--abbrev-ref", "HEAD"); b != "HEAD" {
		g.Branch = b
	}
	// Any porcelain output means the working tree has uncommitted changes.
	g.Dirty = git(dir, "status", "--porcelain") != ""
	if g.Remote == "" && g.Branch == "" && g.Commit == "" && !g.Dirty {
		return nil
	}
	return g
}

// git runs `git -C dir <args...>` with a bounded timeout and returns trimmed
// stdout, or "" on any error. stderr is discarded — a failed probe is not an
// error the caller cares about.
func git(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
