package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/hostenv"
	"cornus/pkg/storage"

	"net/http/httptest"

	_ "cornus/pkg/credential/static"
)

// prefixMapper is a non-identity path mapper: it rewrites a container-side
// prefix to a host-side one, the way CORNUS_HOST_PATH_MAP does for a
// containerized server whose data dir is bind-mounted from the host.
//
// The whole point of this file is that the mapper is NOT the identity. The
// containerized E2E runner is co-located and reports "paths need no
// translation", so every translation bug is invisible to it: the wrong path and
// the right path are the same string. Only a test can hold the two apart.
type prefixMapper struct{ from, to string }

func (m prefixMapper) ToHost(path string) (string, bool) {
	if path == m.from || strings.HasPrefix(path, m.from+"/") {
		return m.to + strings.TrimPrefix(path, m.from), true
	}
	return path, true
}
func (m prefixMapper) Propagation(string) string { return hostenv.PropagationShared }

// fileCredBackend is co-located and binds server-written credential
// directories, but is NOT one of the backends on the 9P client-local-mount fast
// path — which is the combination the old gate made unreachable and which
// containerd and incus are both in.
type fileCredBackend struct {
	fakeBackend
	applies chan api.DeploySpec
}

func (f *fileCredBackend) Remote() bool                            { return false }
func (f *fileCredBackend) BindsCredentialDir(context.Context) bool { return true }

// Name must be a backend that is genuinely NOT on the 9P fast path, and
// "containerd" is the real one this change unblocks.
//
// An arbitrary name will not do, and quietly ruins the test: hostcheck's
// normalizeBackend maps anything it does not recognize to the dockerhost
// default, so a backend called "fake" reports UsesHostMountFastPath TRUE. The
// first version of this file used the inherited "fake" and passed with the fix
// neutralized — it was asserting nothing.
func (f *fileCredBackend) Name() string { return "containerd" }

func (f *fileCredBackend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	st, err := f.fakeBackend.Apply(ctx, spec)
	select {
	case f.applies <- spec:
	default:
	}
	return st, err
}

// newMappedTestServer is newTestServer with a non-identity path mapper, so the
// spec the backend receives can be checked against the spec the server built.
func newMappedTestServer(t *testing.T, backend deploy.Backend, mapperFor func(dataDir string) hostenv.Mapper) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: dir}
	s, err := New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	s.host.mapper = mapperFor(dir)
	s.newBackend = func() (deploy.Backend, error) { return backend, nil }
	return httptest.NewServer(s.Handler()), cfg.MountsDir()
}

// identityMapper is the no-translation case, for tests whose subject is routing
// rather than path rewriting.
func identityMapper(string) hostenv.Mapper { return hostenv.Identity() }

func fileCredSpec() deploywire.DeployAttachSpec {
	return deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "shell",
			Image: "img",
			Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
				Name:       "db",
				Backend:    "static",
				Config:     map[string]string{"value": "s3cr3t"},
				Deliveries: []api.CredentialDelivery{{Kind: "file", Path: "/creds/db.json", Format: "json"}},
			}}},
		},
		CredentialSources: []deploywire.CredentialBacking{{
			Name: "db", Backend: "static", Config: map[string]string{"value": "s3cr3t"},
		}},
	}
}

// TestCredentialMountIsTranslatedForTheRuntime is the regression test for a bug
// that shipped green.
//
// The server writes a credential directory under its own mounts dir and adds an
// ordinary bind. On a containerized server that path is meaningless to the
// runtime, which resolves it in the HOST's namespace — and Docker CREATES a
// missing bind source rather than failing, so the workload comes up healthy with
// an EMPTY credential directory. hostVisibleMountSources exists to make that
// impossible, and the credential mounts were not going through it: with no
// client-local mounts it was never called, and with them it ran on a spec built
// before the credential mounts existed, which were then appended after it.
//
// Nothing caught it because the only thing exercising this path end to end is
// the containerized E2E runner, which is co-located: its mapper is the identity,
// so the untranslated path and the translated path are the same string.
func TestCredentialMountIsTranslatedForTheRuntime(t *testing.T) {
	const hostPrefix = "/srv/cornus-host"
	fb := &fileCredBackend{applies: make(chan api.DeploySpec, 1)}
	srv, mountsDir := newMappedTestServer(t, fb, func(dataDir string) hostenv.Mapper {
		// Built from the ACTUAL data dir, so the mapper genuinely moves this
		// server's paths. A mapper that happens not to match would make every
		// assertion below vacuous — which is precisely how this bug survived.
		return prefixMapper{from: dataDir, to: hostPrefix}
	})
	defer srv.Close()
	if strings.HasPrefix(mountsDir, hostPrefix) {
		t.Fatal("the mapped and unmapped prefixes overlap; the test cannot tell them apart")
	}

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, fileCredSpec())

	select {
	case spec := <-fb.applies:
		var src string
		for _, m := range spec.Mounts {
			if m.Target == "/creds" {
				src = m.Source
			}
		}
		if src == "" {
			t.Fatalf("no credential mount reached the backend: %+v", spec.Mounts)
		}
		if !strings.HasPrefix(src, hostPrefix) && strings.HasPrefix(src, mountsDir) {
			t.Fatalf("credential mount source %s was handed to the runtime untranslated; "+
				"the runtime resolves it in the HOST namespace, creates it fresh, and the "+
				"workload comes up healthy with an empty credential directory", src)
		}
		if !strings.HasPrefix(src, hostPrefix) {
			t.Fatalf("credential mount source %s is neither the server's path nor the host's", src)
		}
	case e := <-errs:
		t.Fatalf("deploy refused: %s", e)
	case <-ctx.Done():
		t.Fatal("backend never received an apply")
	}
}

// TestCredentialFilesDoNotNeedTheNinePFastPath pins the gate correction.
//
// fileCredBackend is co-located and binds server-written directories but is not
// on hostcheck.UsesHostMountFastPath, which is about client-local 9P mounts. A
// credential file involves no 9P, so gating it on that predicate excluded
// containerd and incus from a capability they have, for a reason about a
// different feature. Reaching the backend at all IS the assertion.
func TestCredentialFilesDoNotNeedTheNinePFastPath(t *testing.T) {
	fb := &fileCredBackend{applies: make(chan api.DeploySpec, 1)}
	srv, _ := newMappedTestServer(t, fb, identityMapper)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, fileCredSpec())

	select {
	case spec := <-fb.applies:
		if spec.Credentials != nil {
			t.Error("the credential block must be cleared once realized")
		}
	case e := <-errs:
		t.Fatalf("a file-credential deploy was refused on a backend that binds credential "+
			"directories, because it is not on the 9P client-local-mount fast path — a "+
			"predicate about a different feature: %s", e)
	case <-ctx.Done():
		t.Fatal("backend never received an apply")
	}
}
