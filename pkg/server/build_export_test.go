package server

import (
	"context"
	"errors"
	"io"
	"testing"
)

// fakeLoader is a registry.DockerImageAPI stand-in capturing the loaded archive.
type fakeLoader struct {
	loaded []byte
	err    error
}

func (f *fakeLoader) ImageSave(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (f *fakeLoader) ImageLoad(_ context.Context, r io.Reader) error {
	b, _ := io.ReadAll(r)
	f.loaded = b
	return f.err
}

// TestDockerLoadExport confirms the export sink pipes the archive into the
// daemon's ImageLoad and wait returns its result.
func TestDockerLoadExport(t *testing.T) {
	f := &fakeLoader{}
	s := &Server{daemonImageAPI: f}
	out, wait := s.dockerLoadExport(context.Background())
	wc, err := out(nil)
	if err != nil {
		t.Fatalf("out: %v", err)
	}
	if _, err := wc.Write([]byte("tar-archive-bytes")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if string(f.loaded) != "tar-archive-bytes" {
		t.Fatalf("loaded = %q, want the archive bytes", f.loaded)
	}
}

// TestDockerLoadExportError surfaces a daemon load failure through wait.
func TestDockerLoadExportError(t *testing.T) {
	f := &fakeLoader{err: errors.New("load boom")}
	s := &Server{daemonImageAPI: f}
	out, wait := s.dockerLoadExport(context.Background())
	wc, _ := out(nil)
	_, _ = wc.Write([]byte("x"))
	_ = wc.Close()
	if err := wait(); err == nil {
		t.Fatalf("wait = nil, want the load error")
	}
}

// TestDockerLoadExportNoArchive confirms wait is a no-op (no goroutine leak) when
// the build never produced an archive, so out was never called.
func TestDockerLoadExportNoArchive(t *testing.T) {
	s := &Server{daemonImageAPI: &fakeLoader{}}
	_, wait := s.dockerLoadExport(context.Background())
	if err := wait(); err != nil {
		t.Fatalf("wait without export = %v, want nil", err)
	}
}
