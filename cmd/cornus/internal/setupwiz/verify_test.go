package setupwiz

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cornus/pkg/clientconfig"
)

func writeServerConfig(t *testing.T, server string, tls *clientconfig.TLS) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	f := &clientconfig.File{Contexts: map[string]*clientconfig.Context{
		"t": {Server: server, TLS: tls},
	}}
	if err := clientconfig.Save(path, f); err != nil {
		t.Fatal(err)
	}
	return path
}

func infoHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.cornus/v1/info" {
			http.NotFound(w, r)
			return
		}
		switch {
		case status == 200:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"registry_host":"reg:5000"}`))
		default:
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		}
	})
}

func TestVerifyStatuses(t *testing.T) {
	cases := []struct {
		status  int
		wantOK  bool
		class   string
		hasHint bool
	}{
		{200, true, "ok", false},
		{404, true, "ok-legacy", false},
		{401, false, "auth", true},
	}
	for _, tc := range cases {
		ts := httptest.NewServer(infoHandler(tc.status))
		defer ts.Close()
		path := writeServerConfig(t, ts.URL, nil)
		res := VerifyConnection(context.Background(), path, "t")
		if res.OK != tc.wantOK || res.Class != tc.class {
			t.Errorf("status %d: got OK=%v class=%q, want OK=%v class=%q", tc.status, res.OK, res.Class, tc.wantOK, tc.class)
		}
		if tc.hasHint && len(res.Hints) == 0 {
			t.Errorf("status %d: expected hints", tc.status)
		}
	}
}

func TestVerifyBadTLS(t *testing.T) {
	ts := httptest.NewTLSServer(infoHandler(200)) // self-signed cert
	defer ts.Close()
	path := writeServerConfig(t, ts.URL, nil) // no CA -> verification fails
	res := VerifyConnection(context.Background(), path, "t")
	if res.OK || res.Class != "tls" {
		t.Errorf("bad TLS: got OK=%v class=%q, want class tls", res.OK, res.Class)
	}
	if len(res.Hints) == 0 {
		t.Error("tls failure should carry hints")
	}
}

func TestVerifyRefused(t *testing.T) {
	// Bind then immediately close to get a definitely-closed local address.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	path := writeServerConfig(t, "http://"+addr, nil)
	res := VerifyConnection(context.Background(), path, "t")
	if res.OK || res.Class != "refused" {
		t.Errorf("closed port: got OK=%v class=%q, want class refused", res.OK, res.Class)
	}
}

func TestClassifyResolveErrors(t *testing.T) {
	ssh := classifyResolveError(errors.New(`context "x": ssh: handshake failed: unable to authenticate`))
	if ssh.Class != "ssh" || len(ssh.Hints) == 0 {
		t.Errorf("ssh classify: %+v", ssh)
	}
	kube := classifyResolveError(errors.New(`context "x": svcforward: no cornus service found in namespace "default"`))
	if kube.Class != "kube" || len(kube.Hints) == 0 {
		t.Errorf("kube classify: %+v", kube)
	}
	other := classifyResolveError(errors.New("something else"))
	if other.Class != "resolve" {
		t.Errorf("other classify: %+v", other)
	}
}
