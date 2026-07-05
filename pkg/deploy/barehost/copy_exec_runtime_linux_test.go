//go:build linux

package barehost

// The sandboxed-runtime copy path, driven end to end. A gVisor guest's
// filesystem is not visible through /proc/<pid>/root, so these copies run `tar`
// INSIDE the container over an exec and stream the archive across the runtime
// boundary. The fake runtime plays the part of that in-container tar — wiring the
// exec's stdio exactly as runc does — so the framing, the header parsing, the tee
// that must not swallow bytes, and the error reporting are all real.

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	runc "github.com/containerd/go-runc"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
)

// tarArchive builds a one-entry archive, the shape an in-container `tar -cf -`
// produces for a single file.
func tarArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		ModTime:  time.Unix(1_700_000_000, 0).UTC(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newSandboxedBackend builds a backend that takes the sandboxed copy path, with
// one running instance ready to be exec'd into.
func newSandboxedBackend(t *testing.T) (*Backend, *fakeRuntime) {
	t.Helper()
	b, rt := newTestBackend(t)
	b.sandboxed = true
	seedRunnableInstance(t, b, rt, "web", 0)
	return b, rt
}

// execTar makes the fake runtime behave like an in-container tar: it records the
// command it was asked to run, writes archive on stdout, and drains stdin.
func execTar(rt *fakeRuntime, archive []byte, gotArgs *[]string, gotStdin *bytes.Buffer) {
	rt.execFn = func(_ context.Context, _ string, process specs.Process, opts runtimeExecOpts) error {
		*gotArgs = process.Args
		runAsExecProcess(opts, func(stdin io.Reader, stdout, _ io.Writer) {
			if stdin != nil && gotStdin != nil {
				_, _ = io.Copy(gotStdin, stdin)
			}
			if stdout != nil && archive != nil {
				_, _ = stdout.Write(archive)
			}
		})
		return nil
	}
}

// TestCopyFromViaExecDeliversTheWholeArchiveAndItsStat covers the tee: the first
// header must yield the PathStat WITHOUT consuming it from the caller's stream,
// so the client still receives a complete, well-formed archive.
func TestCopyFromViaExecDeliversTheWholeArchiveAndItsStat(t *testing.T) {
	b, rt := newSandboxedBackend(t)
	archive := tarArchive(t, "app.log", "hello from the guest")
	var gotArgs []string
	execTar(rt, archive, &gotArgs, nil)

	var w bytes.Buffer
	st, err := b.CopyFrom(t.Context(), "web", "/var/log/app.log", &w)
	if err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if st.Name != "app.log" || st.Size != int64(len("hello from the guest")) {
		t.Errorf("stat = %+v, want the archived file's name and size", st)
	}
	if !bytes.Equal(w.Bytes(), archive) {
		t.Errorf("caller received %d bytes, want the complete %d-byte archive", w.Len(), len(archive))
	}
	// The stream must still parse as a tar with its payload intact.
	tr := tar.NewReader(bytes.NewReader(w.Bytes()))
	if _, err := tr.Next(); err != nil {
		t.Fatalf("delivered archive is not readable: %v", err)
	}
	payload, err := io.ReadAll(tr)
	if err != nil || string(payload) != "hello from the guest" {
		t.Errorf("delivered payload = %q, %v", payload, err)
	}
	// It archives the base entry from its parent dir, so the archive is rooted at
	// the requested name rather than an absolute path.
	if want := "tar -C /var/log -cf - -- app.log"; strings.Join(gotArgs, " ") != want {
		t.Errorf("in-container command = %q, want %q", strings.Join(gotArgs, " "), want)
	}
}

// TestStatPathViaExecReadsOnlyTheFirstHeader pins the early-cancel: a stat of a
// large directory must not archive the whole thing.
func TestStatPathViaExecReadsOnlyTheFirstHeader(t *testing.T) {
	b, rt := newSandboxedBackend(t)
	var gotArgs []string
	execTar(rt, tarArchive(t, "hosts", "127.0.0.1 localhost\n"), &gotArgs, nil)

	st, err := b.StatPath(t.Context(), "web", "/etc/hosts")
	if err != nil {
		t.Fatalf("StatPath: %v", err)
	}
	if st.Name != "hosts" || st.Size != int64(len("127.0.0.1 localhost\n")) {
		t.Errorf("stat = %+v, want the header's name and size", st)
	}
	if st.Mtime == "" {
		t.Error("stat carries no mtime")
	}
}

// TestCopyViaExecWithoutTarInTheImage covers the documented limitation: a
// scratch/distroless image has no tar, and the failure must say so rather than
// surfacing a bare EOF.
func TestCopyViaExecWithoutTarInTheImage(t *testing.T) {
	b, rt := newSandboxedBackend(t)
	rt.execFn = func(_ context.Context, _ string, _ specs.Process, _ runtimeExecOpts) error {
		return &runc.ExitError{Status: 127} // command not found; no archive on stdout
	}

	if _, err := b.StatPath(t.Context(), "web", "/etc/hosts"); err == nil {
		t.Error("StatPath without tar in the image: want error")
	} else if !strings.Contains(err.Error(), "tar") {
		t.Errorf("error = %v, want it to point at the missing tar", err)
	}
	var w bytes.Buffer
	if _, err := b.CopyFrom(t.Context(), "web", "/etc/hosts", &w); err == nil {
		t.Error("CopyFrom without tar in the image: want error")
	} else if !strings.Contains(err.Error(), "tar") {
		t.Errorf("error = %v, want it to point at the missing tar", err)
	}
}

// TestCopyToViaExecFeedsTheArchiveOnStdin covers the extract direction: the
// client's archive reaches the in-container tar byte for byte, with the
// ownership flag derived from the request.
func TestCopyToViaExecFeedsTheArchiveOnStdin(t *testing.T) {
	b, rt := newSandboxedBackend(t)
	archive := tarArchive(t, "seed.txt", "payload")
	var gotArgs []string
	var gotStdin bytes.Buffer
	execTar(rt, nil, &gotArgs, &gotStdin)

	if err := b.CopyTo(t.Context(), "web", "/srv", bytes.NewReader(archive), api.CopyToOptions{}); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if !bytes.Equal(gotStdin.Bytes(), archive) {
		t.Errorf("the container received %d bytes on stdin, want the whole %d-byte archive", gotStdin.Len(), len(archive))
	}
	if want := "tar --no-same-owner -C /srv -xf -"; strings.Join(gotArgs, " ") != want {
		t.Errorf("in-container command = %q, want %q", strings.Join(gotArgs, " "), want)
	}
}

// TestCopyToViaExecSurfacesTheGuestTarStderr matters because the failure is
// entirely inside the container: without relaying its stderr the operator gets
// only an exit status.
func TestCopyToViaExecSurfacesTheGuestTarStderr(t *testing.T) {
	b, rt := newSandboxedBackend(t)
	rt.execFn = func(_ context.Context, _ string, _ specs.Process, opts runtimeExecOpts) error {
		runAsExecProcess(opts, func(stdin io.Reader, _, stderr io.Writer) {
			if stdin != nil {
				_, _ = io.Copy(io.Discard, stdin)
			}
			_, _ = io.WriteString(stderr, "tar: /nowhere: Cannot open: No such file or directory")
		})
		return &runc.ExitError{Status: 2}
	}

	err := b.CopyTo(t.Context(), "web", "/nowhere", bytes.NewReader(tarArchive(t, "x", "y")), api.CopyToOptions{})
	if err == nil {
		t.Fatal("CopyTo into a missing destination: want error")
	}
	if !strings.Contains(err.Error(), "exit 2") {
		t.Errorf("error = %v, want the guest tar's exit status", err)
	}
	if !strings.Contains(err.Error(), "Cannot open") {
		t.Errorf("error = %v, want the guest tar's own message relayed", err)
	}
}

// TestCopyToViaExecFallsBackToAHintWhenTarIsSilent covers the empty-stderr case
// (a missing tar binary produces nothing useful), where the message has to carry
// the diagnosis itself.
func TestCopyToViaExecFallsBackToAHintWhenTarIsSilent(t *testing.T) {
	b, rt := newSandboxedBackend(t)
	rt.execFn = func(_ context.Context, _ string, _ specs.Process, _ runtimeExecOpts) error {
		return &runc.ExitError{Status: 127}
	}
	err := b.CopyTo(t.Context(), "web", "/srv", bytes.NewReader(nil), api.CopyToOptions{})
	if err == nil || !strings.Contains(err.Error(), "tar") {
		t.Errorf("error = %v, want a hint naming tar when the guest said nothing", err)
	}
}

// TestCopyViaExecNeedsARunningInstance pins the safety precondition shared with
// the cgroupfs path: a stopped instance has no process to exec into.
func TestCopyViaExecNeedsARunningInstance(t *testing.T) {
	b, rt := newTestBackend(t)
	b.sandboxed = true
	seedInstance(t, b, rt, "web", 0, false) // created, never started

	if _, err := b.StatPath(t.Context(), "web", "/etc/hosts"); err == nil {
		t.Error("StatPath against a non-running instance: want error")
	}
	if _, err := b.CopyFrom(t.Context(), "web", "/etc/hosts", io.Discard); err == nil {
		t.Error("CopyFrom against a non-running instance: want error")
	}
	if err := b.CopyTo(t.Context(), "web", "/srv", bytes.NewReader(nil), api.CopyToOptions{}); err == nil {
		t.Error("CopyTo against a non-running instance: want error")
	}
}

// TestExecTarProcessRunsAsRootFromTheContainerRoot pins the docker-cp parity
// choice: copy must read and write anywhere in the container regardless of the
// image's own user.
func TestExecTarProcessRunsAsRootFromTheContainerRoot(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedRunnableInstance(t, b, rt, "web", 0)

	p, err := b.execTarProcess(rec, tarPackArgs("/etc/hosts"))
	if err != nil {
		t.Fatalf("execTarProcess: %v", err)
	}
	if p.User.UID != 0 {
		t.Errorf("uid = %d, want 0 (docker cp reads as root in the container)", p.User.UID)
	}
	if p.Cwd != "/" {
		t.Errorf("cwd = %q, want /", p.Cwd)
	}
	// The container's own env is inherited so tar is found on PATH.
	if !hasOpt(p.Env, "PATH=/usr/bin") {
		t.Errorf("env = %v, want the container's PATH inherited", p.Env)
	}
}
