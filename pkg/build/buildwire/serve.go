package buildwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/hugelgupf/p9/fsimpl/composefs"
	"github.com/hugelgupf/p9/fsimpl/staticfs"
	"github.com/hugelgupf/p9/p9"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"cornus/pkg/build/buildprog"
	"cornus/pkg/build/internal/lazyctx"
	"cornus/pkg/wire"
)

// ServeOpts describes the local resources the caller exports to the remote build.
type ServeOpts struct {
	ContextDir     string
	DockerfileDir  string
	DockerfileName string            // basename of the Dockerfile, for <name>.dockerignore lookup
	NamedContexts  map[string]string // name -> local directory (eagerly synced)
	LazyContexts   map[string]string // name -> local directory (served on demand over 9P)
	Secrets        map[string][]byte // id -> value
	SSHSockets     map[string]string // ssh id -> local agent socket (RUN --mount=type=ssh)
}

// Serve runs a remote build: it dials the cornus build endpoint, sends the
// spec, serves the caller's directories and secrets over 9P, and delivers
// progress events to progress, returning the build result. header carries extra
// WebSocket-handshake headers (e.g. Authorization); nil sends none. ct carries the
// optional client transport customizations for an https/wss endpoint (custom CA /
// mTLS, and/or a custom dial function such as an SSH tunnel); the zero value uses
// the system defaults.
func Serve(ctx context.Context, url string, spec BuildSpec, opts ServeOpts, progress buildprog.Sink, header http.Header, ct wire.ClientTransport) (*Result, error) {
	sess, err := wire.DialControlHeaderCT(ctx, url, nil, header, ct)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	control, err := wire.OpenTagged(sess, wire.TagControl)
	if err != nil {
		return nil, err
	}
	p9stream, err := wire.OpenTagged(sess, tagP9)
	if err != nil {
		return nil, err
	}

	// Compute each lazy context's content identity + per-file content digests
	// locally (the producer-side hash — a local read, no network) and send them so
	// the server can seed BuildKit's contenthash and skip the cache-key scan. Each
	// named context honors its own .dockerignore, applied identically to the
	// manifest, the digests, and the 9P export so all three agree.
	lazyIgnores := map[string]*patternmatcher.PatternMatcher{}
	for name, dir := range opts.LazyContexts {
		m, err := loadDockerignore(dir, "")
		if err != nil {
			return nil, fmt.Errorf("buildwire: lazy context %q .dockerignore: %w", name, err)
		}
		lazyIgnores[name] = m
		ign := ignoreFunc(m)
		man, err := lazyctx.Build(dir, ign)
		if err != nil {
			return nil, fmt.Errorf("buildwire: lazy context %q: %w", name, err)
		}
		digs, err := lazyctx.ComputeDigests(ctx, dir, ign)
		if err != nil {
			return nil, fmt.Errorf("buildwire: lazy digests %q: %w", name, err)
		}
		spec.LazyContexts = append(spec.LazyContexts, LazySpec{
			Name:        name,
			LayerDigest: man.Digest().String(),
			LayerSize:   int64(len(man.Bytes())),
			Digests:     digs,
		})
	}

	if err := json.NewEncoder(control).Encode(spec); err != nil {
		return nil, fmt.Errorf("buildwire: send spec: %w", err)
	}

	attacher, err := buildAttacher(opts)
	if err != nil {
		return nil, err
	}
	server := p9.NewServer(attacher)
	go func() { _ = server.Handle(p9stream, p9stream) }()

	// A single accept loop owns the session and routes every server-initiated
	// stream by tag: SSH agent tunnels and on-demand lazy-context 9P backings.
	// yamux delivers each stream to exactly one AcceptStream caller, so running
	// two loops here would race and misroute streams — hence one dispatcher.
	// backingReads counts bytes served over lazy-context backings.
	var backingReads atomic.Int64
	if len(opts.SSHSockets) > 0 || len(opts.LazyContexts) > 0 {
		go serveCallerStreams(sess, opts, lazyIgnores, &backingReads)
	}

	dec := json.NewDecoder(control)
	for {
		var m controlMsg
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("buildwire: control stream: %w", err)
		}
		if m.Event != nil {
			progress.Call(*m.Event)
		}
		if m.Done {
			if len(opts.LazyContexts) > 0 {
				// Machine-readable marker; build-lazy-9p.star parses
				// "CORNUS-9P-BACKING served N bytes" to prove the lazy backing ran.
				progress.Log("CORNUS-9P-BACKING served %d bytes\n", backingReads.Load())
			}
			if m.Err != "" {
				return nil, errors.New(m.Err)
			}
			return &Result{ImageDigest: m.Digest}, nil
		}
	}
}

// buildAttacher composes the caller's 9P export tree:
//
//	/context/...      the build context
//	/dockerfile/...   the directory containing the Dockerfile
//	/ctx/<name>/...   each named build context (--build-context)
//	/secrets/<id>     each secret value
//
// Every directory subtree is exported read-only and confined to its root (see
// confinedAttacher); the build context and each named context additionally honor
// a .dockerignore at their own root.
func buildAttacher(opts ServeOpts) (p9.Attacher, error) {
	ignore, err := loadDockerignore(opts.ContextDir, opts.DockerfileName)
	if err != nil {
		return nil, err
	}
	contextFS, err := wire.ConfinedAttacher(opts.ContextDir, ignore)
	if err != nil {
		return nil, fmt.Errorf("buildwire: context %q: %w", opts.ContextDir, err)
	}
	dfFS, err := wire.ConfinedAttacher(opts.DockerfileDir, nil)
	if err != nil {
		return nil, fmt.Errorf("buildwire: dockerfile dir %q: %w", opts.DockerfileDir, err)
	}
	cOpts := []composefs.Opt{
		composefs.WithMount("context", contextFS),
		composefs.WithMount("dockerfile", dfFS),
	}
	if len(opts.NamedContexts) > 0 {
		sub := make([]composefs.Opt, 0, len(opts.NamedContexts))
		for name, dir := range opts.NamedContexts {
			// Each named context honors its own <dir>/.dockerignore (an
			// independent tree, so it never inherits the main context's patterns).
			nIgnore, err := loadDockerignore(dir, "")
			if err != nil {
				return nil, fmt.Errorf("buildwire: build-context %q: %w", name, err)
			}
			fs, err := wire.ConfinedAttacher(dir, nIgnore)
			if err != nil {
				return nil, fmt.Errorf("buildwire: build-context %q: %w", name, err)
			}
			sub = append(sub, composefs.WithMount(name, fs))
		}
		cOpts = append(cOpts, composefs.WithDir("ctx", sub...))
	}
	if len(opts.Secrets) > 0 {
		sopts := make([]staticfs.Option, 0, len(opts.Secrets))
		for id, val := range opts.Secrets {
			sopts = append(sopts, staticfs.WithFile(id, string(val)))
		}
		sa, err := staticfs.New(sopts...)
		if err != nil {
			return nil, err
		}
		cOpts = append(cOpts, composefs.WithMount("secrets", sa))
	}
	return composefs.New(cOpts...)
}

// loadDockerignore reads the build context's ignore file and compiles it into a
// matcher (nil if there is none). Mirroring buildx, a "<dockerfile>.dockerignore"
// next to the context takes precedence over a plain ".dockerignore".
func loadDockerignore(contextDir, dockerfileName string) (*patternmatcher.PatternMatcher, error) {
	candidates := []string{".dockerignore"}
	if dockerfileName != "" {
		candidates = []string{dockerfileName + ".dockerignore", ".dockerignore"}
	}
	for _, name := range candidates {
		f, err := os.Open(filepath.Join(contextDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		patterns, err := ignorefile.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("buildwire: parse %s: %w", name, err)
		}
		if len(patterns) == 0 {
			return nil, nil
		}
		return patternmatcher.New(patterns)
	}
	return nil, nil
}
