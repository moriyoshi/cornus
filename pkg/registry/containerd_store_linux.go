//go:build linux

package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/namespaces"
	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"cornus/pkg/storage"
)

// containerdStore backs the /v2/* registry with a host containerd's native
// content store and image service (host-native re-export on the containerd
// backend). It is a full read-WRITE Store: a pull reads manifests/blobs straight
// from the content store (already digest-addressable — no `docker save`), and a
// push writes blobs into the content store and records the tagged image in the
// image service, so a `cornus build` that pushes to /v2/* lands directly in the
// store the containerd deploy backend runs from. No separate CAS is kept.
//
// It operates in the namespace the containerd deploy backend manages
// (CORNUS_CONTAINERD_NAMESPACE, default "cornus"). The containerd client is
// dialed lazily on first use so the server starts even when containerd is not yet
// up.
type containerdStore struct {
	address    string
	namespace  string
	stagingDir string

	mu     sync.Mutex
	client *ctd.Client

	// stores resolves the namespaced context plus the content and image stores.
	// It dials containerd lazily in production; tests inject in-memory fakes.
	stores func(ctx context.Context) (context.Context, content.Store, images.Store, error)
}

// NewContainerdStore builds a containerd-backed registry Store (host-native
// re-export on the containerd backend). address/namespace default to
// CORNUS_CONTAINERD_ADDRESS / CORNUS_CONTAINERD_NAMESPACE; stagingDir holds
// in-progress uploads. The containerd client is dialed lazily.
func NewContainerdStore(address, namespace, stagingDir string) (Store, error) {
	return newContainerdStore(address, namespace, stagingDir), nil
}

// newContainerdStore builds a containerd-backed registry store. stagingDir holds
// in-progress chunked uploads before they are committed into the content store.
func newContainerdStore(address, namespace, stagingDir string) *containerdStore {
	if address == "" {
		address = containerdDefaultAddress()
	}
	if namespace == "" {
		namespace = containerdDefaultNamespace()
	}
	s := &containerdStore{address: address, namespace: namespace, stagingDir: stagingDir}
	s.stores = s.dialStores
	return s
}

func containerdDefaultAddress() string {
	if a := os.Getenv("CORNUS_CONTAINERD_ADDRESS"); a != "" {
		return a
	}
	if a := os.Getenv("CONTAINERD_ADDRESS"); a != "" {
		return a
	}
	return "/run/containerd/containerd.sock"
}

func containerdDefaultNamespace() string {
	if n := os.Getenv("CORNUS_CONTAINERD_NAMESPACE"); n != "" {
		return n
	}
	return "cornus"
}

func (s *containerdStore) nsctx(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, s.namespace)
}

func (s *containerdStore) conn() (*ctd.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	c, err := ctd.New(s.address)
	if err != nil {
		return nil, fmt.Errorf("registry containerd store: dial %s: %w", s.address, err)
	}
	s.client = c
	return c, nil
}

func (s *containerdStore) dialStores(ctx context.Context) (context.Context, content.Store, images.Store, error) {
	c, err := s.conn()
	if err != nil {
		return nil, nil, nil, err
	}
	return s.nsctx(ctx), c.ContentStore(), c.ImageService(), nil
}

// --- blob reads -------------------------------------------------------------

func (s *containerdStore) StatBlob(ctx context.Context, dgst string) (int64, error) {
	nctx, cs, _, err := s.stores(ctx)
	if err != nil {
		return 0, err
	}
	d, err := digest.Parse(dgst)
	if err != nil {
		return 0, err
	}
	info, err := cs.Info(nctx, d)
	if err != nil {
		return 0, mapNotFound(err)
	}
	return info.Size, nil
}

func (s *containerdStore) GetBlob(ctx context.Context, dgst string) (io.ReadCloser, error) {
	nctx, cs, _, err := s.stores(ctx)
	if err != nil {
		return nil, err
	}
	d, err := digest.Parse(dgst)
	if err != nil {
		return nil, err
	}
	info, err := cs.Info(nctx, d)
	if err != nil {
		return nil, mapNotFound(err)
	}
	ra, err := cs.ReaderAt(nctx, ocispec.Descriptor{Digest: d, Size: info.Size})
	if err != nil {
		return nil, mapNotFound(err)
	}
	return sectionReadCloser{SectionReader: io.NewSectionReader(ra, 0, info.Size), c: ra}, nil
}

// --- manifest reads ---------------------------------------------------------

func (s *containerdStore) GetManifest(ctx context.Context, repo, ref string) (body []byte, dgst, mediaType string, err error) {
	nctx, cs, is, err := s.stores(ctx)
	if err != nil {
		return nil, "", "", err
	}
	// A digest reference addresses a manifest already in the content store.
	if _, _, derr := storage.ParseDigest(ref); derr == nil {
		d, perr := digest.Parse(ref)
		if perr != nil {
			return nil, "", "", perr
		}
		b, rerr := content.ReadBlob(nctx, cs, ocispec.Descriptor{Digest: d})
		if rerr != nil {
			return nil, "", "", mapNotFound(rerr)
		}
		return b, ref, detectMediaType(b), nil
	}
	// A tag reference: find the image and read its target manifest.
	img, err := resolveImage(nctx, is, repo, ref, s.namespace)
	if err != nil {
		return nil, "", "", storage.ErrNotFound
	}
	target := img.Target
	b, err := content.ReadBlob(nctx, cs, target)
	if err != nil {
		return nil, "", "", mapNotFound(err)
	}
	mt := target.MediaType
	if mt == "" {
		mt = detectMediaType(b)
	}
	return b, target.Digest.String(), mt, nil
}

// --- catalog / tags / referrers --------------------------------------------

func (s *containerdStore) Tags(ctx context.Context, repo string) ([]string, error) {
	nctx, _, is, err := s.stores(ctx)
	if err != nil {
		return nil, err
	}
	imgs, err := is.List(nctx)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, img := range imgs {
		named, terr := reference.ParseNormalizedNamed(img.Name)
		if terr != nil {
			continue
		}
		t, ok := named.(reference.Tagged)
		if !ok || trimLibrary(reference.Path(named)) != trimLibrary(repo) {
			continue
		}
		tags = append(tags, t.Tag())
	}
	return tags, nil
}

func (s *containerdStore) Repos(ctx context.Context) ([]string, error) {
	nctx, _, is, err := s.stores(ctx)
	if err != nil {
		return nil, err
	}
	imgs, err := is.List(nctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var repos []string
	for _, img := range imgs {
		named, terr := reference.ParseNormalizedNamed(img.Name)
		if terr != nil {
			continue
		}
		p := trimLibrary(reference.Path(named))
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		repos = append(repos, p)
	}
	return repos, nil
}

// Referrers: containerd has no referrers graph, so the spec-compliant answer is
// an empty index.
func (s *containerdStore) Referrers(ctx context.Context, repo, subject string) ([]storage.Descriptor, error) {
	return nil, nil
}

// --- blob writes ------------------------------------------------------------

func (s *containerdStore) PutBlob(ctx context.Context, r io.Reader, expect string) (string, int64, error) {
	nctx, cs, _, err := s.stores(ctx)
	if err != nil {
		return "", 0, err
	}
	return writeContentStream(nctx, cs, r, expect, "", nil)
}

func (s *containerdStore) DeleteBlob(ctx context.Context, dgst string) error {
	nctx, cs, _, err := s.stores(ctx)
	if err != nil {
		return err
	}
	d, err := digest.Parse(dgst)
	if err != nil {
		return err
	}
	if err := cs.Delete(nctx, d); err != nil {
		return mapNotFound(err)
	}
	return nil
}

// --- chunked uploads (staged to temp files, committed into the content store) ---

func (s *containerdStore) NewUpload(ctx context.Context) (storage.Upload, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return nil, err
	}
	return openCtdUpload(s.stagingDir, hex.EncodeToString(buf[:]), true)
}

func (s *containerdStore) GetUpload(ctx context.Context, id string) (storage.Upload, error) {
	return openCtdUpload(s.stagingDir, id, false)
}

func (s *containerdStore) AbortUpload(ctx context.Context, id string) error {
	_ = os.Remove(ctdUploadPath(s.stagingDir, id))
	return nil
}

func (s *containerdStore) CommitUpload(ctx context.Context, u storage.Upload, expect string) (string, int64, error) {
	nctx, cs, _, err := s.stores(ctx)
	if err != nil {
		return "", 0, err
	}
	rc, err := u.Reader(nctx)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	dgst, size, err := writeContentStream(nctx, cs, rc, expect, "", nil)
	if err != nil {
		return "", 0, err
	}
	_ = u.Close()
	if cu, ok := u.(*ctdUpload); ok {
		_ = os.Remove(cu.path)
	}
	return dgst, size, nil
}

// --- manifest writes --------------------------------------------------------

func (s *containerdStore) PutManifest(ctx context.Context, repo, ref, mediaType string, body []byte) (string, error) {
	nctx, cs, is, err := s.stores(ctx)
	if err != nil {
		return "", err
	}
	dgst := digest.FromBytes(body)
	// Write the manifest blob with the GC-ref labels containerd needs so its
	// content GC treats the config/layers (or sub-manifests) as reachable — the
	// same labels containerd's own pull sets.
	labels := manifestGCLabels(body)
	if err := writeContent(nctx, cs, dgst, body, mediaType, labels); err != nil {
		return "", err
	}
	// Record the tagged image so the deploy backend (and a later re-export read)
	// resolves it by name. A push by digest carries no tag, so it only lands the
	// blob.
	if _, _, derr := storage.ParseDigest(ref); derr != nil {
		img := images.Image{
			Name:   repo + ":" + ref,
			Target: ocispec.Descriptor{MediaType: mediaType, Digest: dgst, Size: int64(len(body))},
		}
		if _, err := is.Update(nctx, img); err != nil {
			if !errdefs.IsNotFound(err) {
				return "", err
			}
			if _, err := is.Create(nctx, img); err != nil && !errdefs.IsAlreadyExists(err) {
				return "", err
			}
		}
	}
	return dgst.String(), nil
}

func (s *containerdStore) DeleteManifest(ctx context.Context, repo, dgst string) error {
	nctx, _, is, err := s.stores(ctx)
	if err != nil {
		return err
	}
	imgs, err := is.List(nctx)
	if err != nil {
		return err
	}
	deleted := false
	for _, img := range imgs {
		if img.Target.Digest.String() != dgst {
			continue
		}
		named, terr := reference.ParseNormalizedNamed(img.Name)
		if terr != nil || trimLibrary(reference.Path(named)) != trimLibrary(repo) {
			continue
		}
		if derr := is.Delete(nctx, img.Name); derr == nil {
			deleted = true
		}
	}
	if !deleted {
		return storage.ErrNotFound
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

// mapNotFound normalizes containerd's not-found errors to storage.ErrNotFound so
// the registry handlers map them to 404 like the CAS backend.
func mapNotFound(err error) error {
	if errdefs.IsNotFound(err) {
		return storage.ErrNotFound
	}
	return err
}

// writeContent writes bytes of known digest into the content store with the given
// labels, treating an already-present blob as success (and refreshing its labels).
func writeContent(ctx context.Context, cs content.Store, dgst digest.Digest, data []byte, mediaType string, labels map[string]string) error {
	w, err := cs.Writer(ctx, content.WithRef("cornus-"+dgst.String()),
		content.WithDescriptor(ocispec.Descriptor{Digest: dgst, Size: int64(len(data)), MediaType: mediaType}))
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			if len(labels) > 0 {
				_, _ = cs.Update(ctx, content.Info{Digest: dgst, Labels: labels})
			}
			return nil
		}
		return err
	}
	defer w.Close()
	if _, err := w.Write(data); err != nil {
		return err
	}
	var opts []content.Opt
	if len(labels) > 0 {
		opts = append(opts, content.WithLabels(labels))
	}
	if err := w.Commit(ctx, int64(len(data)), dgst, opts...); err != nil && !errdefs.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// writeContentStream streams a blob of expected digest into the content store,
// returning the digest and size. containerd verifies the digest at commit.
func writeContentStream(ctx context.Context, cs content.Store, r io.Reader, expect, mediaType string, labels map[string]string) (string, int64, error) {
	d, err := digest.Parse(expect)
	if err != nil {
		return "", 0, err
	}
	w, err := cs.Writer(ctx, content.WithRef("cornus-"+expect),
		content.WithDescriptor(ocispec.Descriptor{Digest: d, MediaType: mediaType}))
	if err != nil {
		if errdefs.IsAlreadyExists(err) {
			_, _ = io.Copy(io.Discard, r)
			info, ierr := cs.Info(ctx, d)
			if ierr != nil {
				return "", 0, ierr
			}
			return expect, info.Size, nil
		}
		return "", 0, err
	}
	defer w.Close()
	n, err := io.Copy(w, r)
	if err != nil {
		return "", 0, err
	}
	var opts []content.Opt
	if len(labels) > 0 {
		opts = append(opts, content.WithLabels(labels))
	}
	if err := w.Commit(ctx, n, d, opts...); err != nil && !errdefs.IsAlreadyExists(err) {
		return "", 0, err
	}
	return expect, n, nil
}

// manifestGCLabels builds the containerd content GC reference labels for a
// manifest or index blob, so its config/layers (or sub-manifests) are treated as
// reachable and not garbage-collected.
func manifestGCLabels(body []byte) map[string]string {
	var doc struct {
		Config    ocispec.Descriptor   `json:"config"`
		Layers    []ocispec.Descriptor `json:"layers"`
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	labels := map[string]string{}
	if len(doc.Manifests) > 0 {
		for i, m := range doc.Manifests {
			labels[fmt.Sprintf("containerd.io/gc.ref.content.m.%d", i)] = m.Digest.String()
		}
		return labels
	}
	if doc.Config.Digest != "" {
		labels["containerd.io/gc.ref.content.config"] = doc.Config.Digest.String()
	}
	for i, l := range doc.Layers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = l.Digest.String()
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// resolveImage finds the containerd image for a /v2 repository and tag. It tries
// the exact "repo:tag" name first, then lists the namespace and matches by
// repository path (host-stripped, library/-insensitive) — so an image stored
// under a loopback-qualified name (127.0.0.1:<port>/repo:tag) and an external
// docker.io/library/repo:tag both resolve from a bare /v2/<repo> pull.
func resolveImage(ctx context.Context, is images.Store, repo, ref, namespace string) (images.Image, error) {
	if img, err := is.Get(ctx, repo+":"+ref); err == nil {
		return img, nil
	}
	list, err := is.List(ctx)
	if err != nil {
		return images.Image{}, err
	}
	for _, img := range list {
		if matchImageName(img.Name, repo, ref) {
			return img, nil
		}
	}
	return images.Image{}, fmt.Errorf("registry containerd store: image %s:%s not found in namespace %q", repo, ref, namespace)
}

// matchImageName reports whether a containerd image name refers to repo:ref,
// ignoring the registry host and Docker Hub's "library/" prefix.
func matchImageName(name, repo, ref string) bool {
	named, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return false
	}
	t, ok := named.(reference.Tagged)
	if !ok || t.Tag() != ref {
		return false
	}
	return trimLibrary(reference.Path(named)) == trimLibrary(repo)
}

func trimLibrary(path string) string { return strings.TrimPrefix(path, "library/") }

// sectionReadCloser adapts a content.ReaderAt into a streaming io.ReadCloser.
type sectionReadCloser struct {
	*io.SectionReader
	c io.Closer
}

func (s sectionReadCloser) Close() error { return s.c.Close() }

// --- staged upload ----------------------------------------------------------

func ctdUploadPath(dir, id string) string { return filepath.Join(dir, "ctd-upload-"+id) }

// ctdUpload stages an in-progress blob upload to a temp file; CommitUpload streams
// it into the content store.
type ctdUpload struct {
	id   string
	path string
	f    *os.File
}

func openCtdUpload(dir, id string, create bool) (*ctdUpload, error) {
	flag := os.O_RDWR | os.O_APPEND
	if create {
		flag |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(ctdUploadPath(dir, id), flag, 0o600)
	if err != nil {
		if !create && os.IsNotExist(err) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return &ctdUpload{id: id, path: ctdUploadPath(dir, id), f: f}, nil
}

func (u *ctdUpload) ID() string { return u.id }

func (u *ctdUpload) Write(_ context.Context, r io.Reader) (int64, error) {
	if _, err := io.Copy(u.f, r); err != nil {
		return 0, err
	}
	if err := u.f.Sync(); err != nil {
		return 0, err
	}
	fi, err := u.f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (u *ctdUpload) Reader(_ context.Context) (io.ReadCloser, error) {
	return os.Open(u.path)
}

func (u *ctdUpload) Close() error { return u.f.Close() }
