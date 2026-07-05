package server

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"cornus/pkg/build/builder"
	"cornus/pkg/build/buildprog"
)

// handleBuild serves POST /.cornus/v1/build. The request body is a tar stream of the
// build context; query parameters configure the build:
//
//	t          target image reference (required), e.g. localhost:5000/app:v1
//	dockerfile Dockerfile path within the context (default "Dockerfile")
//	push       "true" to push the result (default true)
//	no-cache   "true" to disable the build cache
//	insecure   "true" to allow an HTTP registry (default true)
//
// Build progress is streamed back as text/plain.
func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// Gate the build on the API policy before touching the engine or the request
	// body.
	if !s.apiPolicy.Allow(Identity(r), "build") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to build"})
		return
	}
	q := r.URL.Query()
	target := q.Get("t")
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing target (?t=)"})
		return
	}

	ctxDir, err := os.MkdirTemp(s.cfg.DataDir, "build-context-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer os.RemoveAll(ctxDir)
	// Cap the build-context tar so a runaway or hostile upload cannot fill the
	// data dir. MaxBytesReader makes reads past the ceiling fail, surfacing as a
	// 400 below (headers are not yet written here).
	body := http.MaxBytesReader(w, r.Body, maxBuildContextBytes())
	if err := extractTar(body, ctxDir); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad context tar: " + err.Error()})
		return
	}

	// Bound concurrent builds against the shared engine: block (queue) until a
	// semaphore slot is free, or bail if the client goes away while waiting.
	// Acquired before the 200 header is written so a queued-then-cancelled build
	// never commits a status.
	select {
	case s.buildSem <- struct{}{}:
		defer func() { <-s.buildSem }()
	case <-r.Context().Done():
		return
	}

	engine, err := s.getEngine()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "build engine unavailable: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	pw := &flushWriter{w: w, f: flusher}

	buildArgs := map[string]string{}
	for _, kv := range q["build-arg"] {
		if k, v, ok := strings.Cut(kv, "="); ok {
			buildArgs[k] = v
		}
	}

	push := q.Get("push") != "false"
	noCache := q.Get("no-cache") == "true"
	// docker-daemon host-native has no push-able registry, so the build lands in
	// the daemon: export a docker-archive streamed into POST /images/load, target
	// verbatim so the daemon tags it as deployed. Every other mode pushes to the
	// registry normally (the containerd store imports the push; the CAS stores it),
	// with the advertised-registry→loopback redirect.
	var dockerArchiveOut func(map[string]string) (io.WriteCloser, error)
	var loadWait func() error
	if s.registrySource == registrySourceDockerDaemon {
		push = false
		dockerArchiveOut, loadWait = s.dockerLoadExport(r.Context())
	} else {
		target = s.localPushTarget(r.Context(), target, push)
	}

	ctx, span := s.tracer.Start(r.Context(), "cornus.build", trace.WithAttributes(
		attribute.String("cornus.build.target", target),
		attribute.Bool("cornus.build.push", push),
		attribute.Bool("cornus.build.no_cache", noCache),
	))
	defer span.End()
	start := time.Now()

	// The plain HTTP build path streams a text log to docker-style clients; render
	// progress events as plain text to the flushing writer.
	res, err := engine.Build(ctx, builder.Request{
		ContextDir:          ctxDir,
		Dockerfile:          defaultStr(q.Get("dockerfile"), "Dockerfile"),
		Target:              target,
		BuildArgs:           buildArgs,
		Push:                push,
		NoCache:             noCache,
		Insecure:            q.Get("insecure") != "false",
		DockerArchiveOutput: dockerArchiveOut,
	}, buildprog.NewSink(pw, buildprog.Plain, false))
	// Wait for the daemon load to finish (docker-daemon mode) so the image is
	// present before the client proceeds to deploy; surface a load error as the
	// build error.
	if loadWait != nil {
		if lerr := loadWait(); lerr != nil && err == nil {
			err = lerr
		}
	}

	outcome := "ok"
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else if res.ImageDigest != "" {
		span.SetAttributes(attribute.String("cornus.build.digest", res.ImageDigest))
	}
	outAttr := metric.WithAttributes(attribute.String("outcome", outcome))
	s.metrics.builds.Add(ctx, 1, outAttr)
	s.metrics.buildDur.Record(ctx, time.Since(start).Seconds(), outAttr)

	// Wire convention: the response status is 200 as soon as the build starts
	// streaming, so a build failure cannot change it. Failure is signalled
	// in-band by a trailing "BUILD FAILED: ..." line (success by "BUILD OK ...").
	// Clients must parse the trailer, not rely on the HTTP status, to detect a
	// failed build.
	if err != nil {
		fmt.Fprintf(pw, "\nBUILD FAILED: %v\n", err)
		return
	}
	fmt.Fprintf(pw, "\nBUILD OK %s %s\n", target, res.ImageDigest)
}

// defaultMaxBuildContextBytes caps the build-context tar upload (2 GiB), a
// generous ceiling for a source tree. CORNUS_MAX_BUILD_CONTEXT_BYTES overrides
// it; a non-positive or unparseable value falls back to the default.
const defaultMaxBuildContextBytes = 2 << 30

func maxBuildContextBytes() int64 {
	if raw := os.Getenv("CORNUS_MAX_BUILD_CONTEXT_BYTES"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxBuildContextBytes
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// dockerLoadExport builds a docker-archive export sink for docker-daemon
// re-export mode. out is passed to the build as its docker-archive Output writer;
// on the first (and only) call it pipes the archive into the daemon's
// POST /images/load in a background goroutine. wait blocks until that load
// finishes and returns its error — or nil if the build never produced an archive
// (e.g. it failed before export), so no goroutine leaks. Requires s.daemonImageAPI.
func (s *Server) dockerLoadExport(ctx context.Context) (out func(map[string]string) (io.WriteCloser, error), wait func() error) {
	loadDone := make(chan error, 1)
	started := false
	out = func(_ map[string]string) (io.WriteCloser, error) {
		pr, pw := io.Pipe()
		started = true
		go func() {
			err := s.daemonImageAPI.ImageLoad(ctx, pr)
			// Unblock the exporter's writer if the load aborts early.
			_ = pr.CloseWithError(err)
			loadDone <- err
		}()
		return pw, nil
	}
	wait = func() error {
		if !started {
			return nil
		}
		return <-loadDone
	}
	return out, wait
}

// flushWriter flushes after every write so progress streams to the client.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// extractTar unpacks a tar stream into dir, rejecting path traversal.
func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) && target != dir {
			return fmt.Errorf("illegal path in tar: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink, tar.TypeLink:
			// Skip links in the build context for safety.
		}
	}
}
