//go:build linux

package builder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"cornus/pkg/registry"
	"cornus/pkg/storage"
)

// TestNewContainerdWorkerRejectsLazyBuild checks the construction-time gate:
// the containerd worker cannot serve lazy build contexts, so an engine
// configured with both must fail before dialing containerd (no daemon needed).
func TestNewContainerdWorkerRejectsLazyBuild(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv(lazyBuildEnv, "1")
	_, err := New(Config{Root: t.TempDir(), Worker: WorkerContainerd})
	if err == nil {
		t.Fatal("expected New to fail with CORNUS_LAZY_BUILD + containerd worker")
	}
	if !strings.Contains(err.Error(), lazyBuildEnv) || !strings.Contains(err.Error(), WorkerContainerd) {
		t.Errorf("error %q should mention %s and the containerd worker", err, lazyBuildEnv)
	}
}

// TestNewContainerdWorkerDeadSocket checks that pointing the containerd worker
// at a nonexistent socket fails engine construction quickly with an error
// naming containerd (via the pre-dial socket probe, not a hung blocking dial).
func TestNewContainerdWorkerDeadSocket(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv(lazyBuildEnv, "")
	addr := filepath.Join(t.TempDir(), "nonexistent.sock")
	start := time.Now()
	_, err := New(Config{
		Root:       t.TempDir(),
		Worker:     WorkerContainerd,
		Containerd: ContainerdConfig{Address: addr},
	})
	if err == nil {
		t.Fatal("expected New to fail against a nonexistent containerd socket")
	}
	if !strings.Contains(err.Error(), "containerd") || !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q should mention containerd and the socket address", err)
	}
	if elapsed := time.Since(start); elapsed > containerdDialTimeout {
		t.Errorf("New took %v; the socket probe should fail well before the dial timeout", elapsed)
	}
}

// containerdTestAddressEnv points the privileged integration test at a
// containerd socket; it defaults to the standard system socket.
const containerdTestAddressEnv = "CORNUS_TEST_CONTAINERD_ADDRESS"

// TestBuildAndPushContainerdWorker mirrors TestBuildAndPush on the containerd
// worker: it builds a hermetic "FROM scratch" image, pushes it to an
// in-process cornus registry, pulls the manifest back, and additionally
// asserts the tagged image landed in containerd's image store (the worker sets
// ImageStore to the containerd image service).
//
// It requires root and a reachable containerd daemon; skipped otherwise. It
// uses a throwaway containerd namespace so it never touches existing state.
func TestBuildAndPushContainerdWorker(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("build execution requires root; skipping on unprivileged host")
	}
	address := os.Getenv(containerdTestAddressEnv)
	if address == "" {
		address = defaultContainerdAddress
	}
	cl, err := containerd.New(address, containerd.WithTimeout(2*time.Second))
	if err != nil {
		t.Skipf("containerd not reachable at %s (set %s): %v", address, containerdTestAddressEnv, err)
	}
	defer cl.Close()
	vctx, vcancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = cl.Version(vctx)
	vcancel()
	if err != nil {
		t.Skipf("containerd at %s did not answer Version (set %s): %v", address, containerdTestAddressEnv, err)
	}

	// In-process cornus registry as the push target.
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	mux := http.NewServeMux()
	registry.New(st).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// Hermetic build context.
	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	df := "FROM scratch\nCOPY hello.txt /hello.txt\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}

	ns := fmt.Sprintf("cornus-test-%d", os.Getpid())
	nsctx := namespaces.WithNamespace(context.Background(), ns)
	target := host + "/demo:v1"
	defer func() {
		// Best-effort cleanup of the throwaway namespace's contents.
		_ = cl.ImageService().Delete(nsctx, target)
		_ = cl.NamespaceService().Delete(context.Background(), ns)
	}()

	eng, err := New(Config{
		Root:       t.TempDir(),
		Worker:     WorkerContainerd,
		Containerd: ContainerdConfig{Address: address, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("New engine (containerd worker): %v", err)
	}
	defer eng.Close()

	res, err := eng.Build(context.Background(), Request{
		ContextDir: ctxDir,
		Target:     target,
		Push:       true,
		Insecure:   true,
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.ImageDigest == "" {
		t.Fatal("expected an image digest")
	}

	// The push must be pullable from the registry, exactly like the runc worker.
	ref, err := name.ParseReference(target, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Image(ref); err != nil {
		t.Fatalf("pull built image: %v", err)
	}

	// The containerd worker's ImageStore is the containerd image service, so the
	// tagged build must be visible in containerd's image store in our namespace.
	img, err := cl.ImageService().Get(nsctx, target)
	if err != nil {
		t.Fatalf("image %q not recorded in containerd image store (namespace %s): %v", target, ns, err)
	}
	if img.Target.Digest.String() != res.ImageDigest {
		t.Errorf("containerd image digest = %s, build reported %s", img.Target.Digest, res.ImageDigest)
	}
}
