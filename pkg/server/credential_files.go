package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/logging"
)

// credFileMode is the permission every rendered credential file lands with,
// matching creddelivery.WriteFile — which the caretaker uses for the same
// delivery on kubernetes. The two paths must not disagree about how exposed a
// credential file is merely because a different process wrote it.
const credFileMode = 0o600

// dataLink is the indirection every credential file points through, and
// versionPrefix names the directories it points AT.
//
// The layout is Kubernetes' atomic-writer shape, for its reason: a bind pins an
// inode, so replacing a file by rename — the only safe way to replace one — would
// leave the workload looking at the dead inode forever. Instead each refresh
// writes a whole new version directory and swaps this ONE symlink, which rename
// makes atomic. A reader sees either every old file or every new one, never a
// half-written byte.
//
// Both names begin with a dot-dot prefix so a workload listing its credential
// directory sees only the files it asked for.
const (
	dataLink      = "..data"
	versionPrefix = "..v"
)

// credentialFiles is one session's materialized credential files: the directory
// tree on the server's disk and the mounts that expose it to the workload.
//
// It lives under the server's MOUNTS dir, and that placement does the work of
// three separate mechanisms. hostpolicy's bind allow-list already covers that
// prefix (mountBindPrefixes), so no carve-out is needed and no other deployment's
// directory becomes reachable. hostVisibleMountSources already translates sources
// under it, so a containerized cornus hands the runtime the path the RUNTIME
// resolves rather than its own. And the session id in the name is the same
// unguessable capability the 9P mountpoints beside it rely on.
type credentialFiles struct {
	dir     string      // <MountsDir>/creds-<session>
	mounts  []api.Mount // one per distinct container directory, read-only
	groups  []credFileGroup
	version int
	// uid/gid are the HOST ids the workload's own user corresponds to, already
	// translated through the backend's id map. They are kept on the set because
	// the DIRECTORIES need them too: a runtime that remaps ids has to traverse
	// this tree as the workload, and a directory it cannot enter is as fatal as
	// a file it cannot read — the rootless-podman failure that produced the
	// original refusal was `statfs` on the directory, before any file was
	// opened.
	uid, gid int
}

// credFileGroup is the files destined for one container directory, which becomes
// one bind. Grouping by directory is what lets several credentials share a target
// without one mount hiding another.
type credFileGroup struct {
	containerDir string
	hostDir      string
	files        []deploy.CredentialFile
}

// prepareCredentialFiles renders every file delivery in spec and materializes it,
// returning the set (whose mounts the caller adds to the spec) and a teardown
// that removes the tree. Nil, with a no-op teardown, when spec declares none.
func (s *Server) prepareCredentialFiles(ctx context.Context, sess *deploywire.ServerSession, spec api.DeploySpec, session string, backend deploy.Backend) (*credentialFiles, func(), error) {
	if !specHasFileDelivery(spec) {
		return nil, func() {}, nil
	}
	uid, gid, translated, err := initialCredentialFileOwner(ctx, spec, backend)
	if err != nil {
		return nil, nil, err
	}
	byDir := map[string][]deploy.CredentialFile{}
	for _, src := range spec.Credentials.Sources {
		deliveries := fileDeliveries(src)
		if len(deliveries) == 0 {
			continue
		}
		cred, ferr := fetchCredentialValue(sess, src.Name)
		if ferr != nil {
			return nil, nil, fmt.Errorf("fetch credential %q: %w", src.Name, ferr)
		}
		files, rerr := renderCredentialFiles(src.Name, cred, deliveries, uid, gid)
		if rerr != nil {
			return nil, nil, rerr
		}
		for _, f := range files {
			d := filepath.Dir(f.Path)
			byDir[d] = append(byDir[d], f)
		}
	}

	cf := &credentialFiles{dir: filepath.Join(s.cfg.MountsDir(), "creds-"+session), uid: uid, gid: gid}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs) // deterministic group indices across re-applies
	for i, d := range dirs {
		host := filepath.Join(cf.dir, fmt.Sprint(i))
		cf.groups = append(cf.groups, credFileGroup{containerDir: d, hostDir: host, files: byDir[d]})
		cf.mounts = append(cf.mounts, api.Mount{Source: host, Target: d, ReadOnly: true})
	}
	if err := cf.write(ctx); err != nil {
		_ = os.RemoveAll(cf.dir)
		return nil, nil, err
	}
	// A remapping runtime reaches this tree as an ORDINARY USER, so every
	// directory from the data dir down has to be traversable by it. AFTER write,
	// because write is what creates them. Without this the mapping above is
	// correct and useless: podman answered
	// `statfs .../creds-<session>/0: permission denied` on the DIRECTORY, before
	// it ever opened a file.
	if translated {
		if err := s.makeServerPathTraversable(ctx); err != nil {
			_ = os.RemoveAll(cf.dir)
			return nil, nil, err
		}
	}
	return cf, func() { _ = os.RemoveAll(cf.dir) }, nil
}

// write materializes a new version of every group and swaps each ..data symlink
// onto it. Called once at deploy time and again on every refresh.
func (cf *credentialFiles) write(ctx context.Context) error {
	log := logging.FromContext(ctx)
	cf.version++
	version := fmt.Sprintf("%s%d", versionPrefix, cf.version)
	for _, g := range cf.groups {
		vdir := filepath.Join(g.hostDir, version)
		// 0755 on the directories: the workload must traverse them to reach a
		// file that is itself 0600.
		if err := os.MkdirAll(vdir, 0o755); err != nil {
			return fmt.Errorf("create credential dir: %w", err)
		}
		// The directories carry the same ownership as the files. 0755 already
		// lets anyone traverse, so this is not what makes the tree reachable —
		// it is what keeps ownership consistent when the runtime remaps ids, so
		// the workload is not looking at a tree owned by an id it cannot name.
		for _, d := range []string{g.hostDir, vdir} {
			if err := os.Chown(d, cf.uid, cf.gid); err != nil {
				log.WarnContext(ctx, "could not set credential directory ownership",
					"dir", d, "uid", cf.uid, "gid", cf.gid, "error", err.Error())
			}
		}
		for _, f := range g.files {
			p := filepath.Join(vdir, filepath.Base(f.Path))
			if err := os.WriteFile(p, f.Content, os.FileMode(f.Mode)); err != nil {
				return fmt.Errorf("write credential file: %w", err)
			}
			if err := os.Chown(p, f.UID, f.GID); err != nil {
				// Not fatal: already correct when the workload runs as the
				// server's own uid. Loud because when it is NOT, the symptom is
				// the application's own "permission denied" with nothing pointing
				// back here.
				log.WarnContext(ctx, "could not set credential file ownership; a workload running as a different user will not be able to read it",
					"path", f.Path, "uid", f.UID, "gid", f.GID, "error", err.Error())
			}
		}
		if err := swapLink(filepath.Join(g.hostDir, dataLink), version); err != nil {
			return err
		}
		// Per-file symlinks are created once and never move; only ..data does.
		for _, f := range g.files {
			link := filepath.Join(g.hostDir, filepath.Base(f.Path))
			target := filepath.Join(dataLink, filepath.Base(f.Path))
			if err := os.Symlink(target, link); err != nil && !os.IsExist(err) {
				return fmt.Errorf("link credential file: %w", err)
			}
		}
		// Drop the version this one replaced. Best-effort: a leftover directory
		// wastes space but can never serve a stale value, since nothing links it.
		if cf.version > 1 {
			_ = os.RemoveAll(filepath.Join(g.hostDir, fmt.Sprintf("%s%d", versionPrefix, cf.version-1)))
		}
	}
	return nil
}

// swapLink points name at target atomically, replacing any existing link.
//
// os.Symlink cannot overwrite, so the swap goes through a temporary name and
// rename — which IS atomic, and is the whole reason the layout has a symlink in
// it rather than files a refresh would overwrite in place.
func swapLink(name, target string) error {
	tmp := name + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("stage credential link: %w", err)
	}
	if err := os.Rename(tmp, name); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("swap credential link: %w", err)
	}
	return nil
}

// renderCredentialFiles turns a source's file deliveries into the bytes to
// materialize, using the credential the server already fetched for this source.
//
// Rendering happens HERE because the deploy-attach session is here: the value
// comes from the caller's own machine over a connection only this process holds.
func renderCredentialFiles(name string, cred credential.Credential, deliveries []api.CredentialDelivery, uid, gid int) ([]deploy.CredentialFile, error) {
	files := make([]deploy.CredentialFile, 0, len(deliveries))
	for _, d := range deliveries {
		if d.Path == "" {
			return nil, fmt.Errorf("credential %q: file delivery has no path", name)
		}
		if !filepath.IsAbs(d.Path) {
			return nil, fmt.Errorf("credential %q: file delivery path %q must be absolute", name, d.Path)
		}
		data, err := creddelivery.Render(cred, d.Format)
		if err != nil {
			return nil, fmt.Errorf("credential %q: render %s: %w", name, d.Path, err)
		}
		files = append(files, deploy.CredentialFile{
			Path: d.Path, Content: data, Mode: credFileMode, UID: uid, GID: gid,
		})
	}
	return files, nil
}

// credentialFileOwner resolves the uid/gid rendered files must carry, warning
// once when the spec names a user only the image can resolve.
//
// It warns rather than failing because a non-numeric `user:` is legitimate and
// common, and refusing the deploy over it would be worse than delivering a file a
// root workload reads fine. It warns rather than staying silent because the
// failure it predicts — mode 0600 owned by the wrong uid — surfaces as the
// application's own "permission denied", with nothing connecting it back here.
func credentialFileOwner(ctx context.Context, spec api.DeploySpec) (uid, gid int) {
	uid, gid, ok := deploy.CredentialFileOwner(spec.User)
	if !ok {
		logging.FromContext(ctx).WarnContext(ctx,
			"credential files will be owned by this server's user: the deployment's user is a name this server cannot resolve to a uid (it lives in the image's /etc/passwd), so a workload running as it may not be able to read them; use a numeric user: to fix the ownership",
			"deployment", spec.Name, "user", spec.User)
	}
	return uid, gid
}

// credentialFileHostOwner resolves the HOST ids a rendered credential file must
// be owned by, translating the workload's own uid/gid through the backend's id
// map.
//
// The translation is the whole point. spec.User names a CONTAINER-side id, and a
// runtime that remaps ids (rootless podman, incus) turns a file owned by that
// number into one owned by an id the workload's namespace does not map — which
// the kernel reports as 65534, the OVERFLOW uid, and which no mode bit can
// rescue because a userns root holds CAP_DAC_OVERRIDE only over ids inside its
// map.
//
// An unmappable id is an ERROR rather than a fallback to the container-side
// number. Falling back is precisely how an unreadable file gets written while
// the deploy reports success, and that outcome is what this whole facility
// exists to remove.
// hostDirs lists the directories the server wrote, for a backend that has to take
// ownership of them itself (deploy.LateIDCredentialBinder).
func (cf *credentialFiles) hostDirs() []string {
	if cf == nil {
		return nil
	}
	out := make([]string, 0, len(cf.groups))
	for _, g := range cf.groups {
		out = append(out, g.hostDir)
	}
	return out
}

// applyOwningCredentials calls Apply, routing through
// deploy.LateIDCredentialBinder when the backend is one AND there are credential
// directories for it to own.
//
// Both halves of that condition matter. A backend that is not a late binder has
// already had its files written with the right ids, and a late binder with no
// credential directories must take the ordinary path — otherwise every plain
// deploy would go down a route that exists for credentials alone.
func applyOwningCredentials(ctx context.Context, backend deploy.Backend, spec api.DeploySpec, credDirs []string) (api.DeployStatus, error) {
	if len(credDirs) > 0 {
		if late, ok := backend.(deploy.LateIDCredentialBinder); ok {
			return late.ApplyOwningCredentialDirs(ctx, spec, credDirs)
		}
	}
	return backend.Apply(ctx, spec)
}

// initialCredentialFileOwner resolves the ownership for the FIRST write, before
// Apply has run.
//
// For nearly every backend that is credentialFileHostOwner: the runtime's id map
// comes from something that outlives any one workload, so the host ids are
// knowable now and the files land correct the moment they are written.
//
// A deploy.LateIDCredentialBinder is the exception, and asking it here does not
// merely return a worse answer — it ERRORS, because the map lives on a container
// that Apply has not created yet. So the files are written with the CONTAINER-side
// ids and the backend corrects them in the one window where the map exists and
// nothing is reading it: after create, before start. Writing them wrong-and-then-
// fixed is safe in a way that writing them wrong-and-left would not be, because
// the correction happens before the workload can observe either state.
//
// translated is reported true for a late binder even though nothing was
// translated here. It drives makeServerPathTraversable, and the question that
// answers is "will a remapping runtime have to walk this tree" — which is exactly
// what a late binder is telling us.
func initialCredentialFileOwner(ctx context.Context, spec api.DeploySpec, backend deploy.Backend) (uid, gid int, translated bool, err error) {
	if _, late := backend.(deploy.LateIDCredentialBinder); late {
		uid, gid = credentialFileOwner(ctx, spec)
		return uid, gid, true, nil
	}
	return credentialFileHostOwner(ctx, spec, backend)
}

func credentialFileHostOwner(ctx context.Context, spec api.DeploySpec, backend deploy.Backend) (uid, gid int, translated bool, err error) {
	uid, gid = credentialFileOwner(ctx, spec)
	hostUID, hostGID, err := deploy.HostIDsFor(ctx, backend, spec.Name, uid, gid)
	if err != nil {
		return 0, 0, false, fmt.Errorf("credential file ownership: %w", err)
	}
	return hostUID, hostGID, hostUID != uid || hostGID != gid, nil
}

// refreshCredentialFiles rewrites the materialized files for as long as ctx
// lives, on the credential's own cadence.
//
// Without it a file delivery is a snapshot: correct at container start and stale
// forever after, which quietly breaks exactly the credentials the feature is most
// useful for — aws-sts and the LLM logins, whose tokens are short-lived by design.
// The caretaker refreshes for the same reason on kubernetes (serveCredFile); this
// shares the deadline arithmetic (credential.Expiry) with it rather than
// reimplementing it, so the two cannot disagree about when a credential is stale.
//
// A failed refresh is logged and retried rather than tearing the deploy down: the
// workload still holds the previous value, which beats a running application
// losing its credential because one fetch failed.
func (s *Server) refreshCredentialFiles(ctx context.Context, cf *credentialFiles, sess *deploywire.ServerSession, spec api.DeploySpec, first time.Time, backend deploy.Backend) {
	log := logging.FromContext(ctx, slog.String("component", "credential-files"), slog.String("deployment", spec.Name))
	next := first
	for {
		wait := time.Until(next)
		if wait < time.Second {
			// Never busy-loop on a credential already past its deadline.
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		deadline, err := s.rewriteCredentialFiles(ctx, cf, sess, spec, backend)
		if err != nil {
			log.WarnContext(ctx, "credential file refresh failed; the workload keeps the previous value and this will be retried",
				"error", err.Error())
			next = time.Now().Add(credential.DefaultTTL)
			continue
		}
		next = deadline
	}
}

// rewriteCredentialFiles re-fetches every file-bearing source, re-renders, writes
// a new version, and returns when the next rewrite is due.
func (s *Server) rewriteCredentialFiles(ctx context.Context, cf *credentialFiles, sess *deploywire.ServerSession, spec api.DeploySpec, backend deploy.Backend) (time.Time, error) {
	uid, gid, _, err := credentialFileHostOwner(ctx, spec, backend)
	if err != nil {
		return time.Time{}, err
	}
	byDir := map[string][]deploy.CredentialFile{}
	next := time.Now().Add(credential.DefaultTTL)
	for _, src := range spec.Credentials.Sources {
		deliveries := fileDeliveries(src)
		if len(deliveries) == 0 {
			continue
		}
		cred, err := fetchCredentialValue(sess, src.Name)
		if err != nil {
			return time.Time{}, fmt.Errorf("fetch credential %q: %w", src.Name, err)
		}
		files, err := renderCredentialFiles(src.Name, cred, deliveries, uid, gid)
		if err != nil {
			return time.Time{}, err
		}
		for _, f := range files {
			byDir[filepath.Dir(f.Path)] = append(byDir[filepath.Dir(f.Path)], f)
		}
		if d := credential.Expiry(time.Now(), cred.Expiration, credential.ParseTTL(src.TTL)); d.Before(next) {
			next = d
		}
	}
	for i := range cf.groups {
		cf.groups[i].files = byDir[cf.groups[i].containerDir]
	}
	if err := cf.write(ctx); err != nil {
		return time.Time{}, err
	}
	return next, nil
}

// credentialFileDeadline is when the first refresh is due: the earliest declared
// TTL among the spec's file-bearing sources.
//
// It uses the DECLARED TTL because the credential's own expiry is not known until
// a value is fetched, and refetching purely to learn it would cost an extra client
// round trip per deploy. Every later deadline comes from the fetched credential
// (credential.Expiry), so a short-lived value converges to its real cadence after
// one cycle.
func credentialFileDeadline(spec api.DeploySpec) time.Time {
	ttl := credential.DefaultTTL
	if spec.Credentials != nil {
		for _, src := range spec.Credentials.Sources {
			if len(fileDeliveries(src)) == 0 {
				continue
			}
			if d := credential.ParseTTL(src.TTL); d < ttl {
				ttl = d
			}
		}
	}
	return time.Now().Add(ttl)
}

// fileDeliveries selects a source's file-kind deliveries.
func fileDeliveries(src api.CredentialSource) []api.CredentialDelivery {
	var out []api.CredentialDelivery
	for _, d := range src.Deliveries {
		if d.Kind == "file" {
			out = append(out, d)
		}
	}
	return out
}

// specHasFileDelivery reports whether spec declares any file-kind delivery.
func specHasFileDelivery(spec api.DeploySpec) bool {
	if spec.Credentials == nil {
		return false
	}
	for _, src := range spec.Credentials.Sources {
		if len(fileDeliveries(src)) > 0 {
			return true
		}
	}
	return false
}

// makeServerPathTraversable widens the directories between the data dir and the
// material this server hands a workload — credential files, 9P mountpoints —
// just enough for a remapping runtime to walk to them.
//
// It is a no-op unless the ids were actually translated, so a default install —
// rootful docker, containerd, bare — keeps its data dir exactly as it was. Where
// it does apply, the data dir becomes 0711: TRAVERSABLE BUT NOT LISTABLE. That
// distinction is the whole reason this is acceptable. An unprivileged local user
// gains the ability to walk THROUGH the directory to a path they can already
// name, and nothing else:
//
//   - the secrets that live directly under it (the internal credential, the peer
//     key) are written 0600 and stay owner-only;
//   - the credential directory's own name carries the deploy session id, which
//     is the same unguessable capability the 9P mountpoints beside it rely on,
//     so "can traverse" is not "can find";
//   - listing is still denied, so the tree cannot be enumerated.
//
// The alternative was to keep refusing file deliveries on every runtime that
// remaps ids, which is what cornus did until this facility existed.
func (s *Server) makeServerPathTraversable(ctx context.Context) error {
	log := logging.FromContext(ctx)
	// 0711 on the data dir, 0755 from the mounts dir down. The data dir holds
	// secrets and must not become listable; the mounts dir holds only material
	// meant for workloads.
	for dir, mode := range map[string]os.FileMode{
		s.cfg.DataDir:     0o711,
		s.cfg.MountsDir(): 0o755,
	} {
		if err := os.Chmod(dir, mode); err != nil {
			return fmt.Errorf("a workload on this runtime must be able to traverse %s to reach what this server wrote: %w", dir, err)
		}
	}
	log.DebugContext(ctx, "widened the credential path for a runtime that remaps ids", "dataDir", s.cfg.DataDir)
	return nil
}
