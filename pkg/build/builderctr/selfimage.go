package builderctr

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// SelfImageRepo is the repository for images built from the running binary. The
// tag is the binary's content hash, so a rebuilt or upgraded cornus produces a
// new image and an unchanged one is reused as-is.
const SelfImageRepo = "cornus-builder"

// FallbackBaseImage is used when the host's distribution cannot be identified.
const FallbackBaseImage = "debian:bookworm-slim"

// selfImageDockerfile is the builder image recipe: the host's own cornus binary
// on a base that carries runc.
//
// runc is not optional — BuildKit's executor shells out to it for every RUN — and
// the published cornus image installs it for the same reason.
//
// The base defaults to the HOST's distribution (see hostBaseImage) rather than a
// fixed minimal image, because a locally built cornus is typically dynamically
// linked against the host's glibc; dropping that binary into an older or musl
// base would fail to exec. Matching the host keeps it runnable either way.
const selfImageDockerfile = `FROM %s
RUN (command -v runc >/dev/null 2>&1) || ( \
      apt-get update \
      && apt-get install -y --no-install-recommends runc ca-certificates \
      && rm -rf /var/lib/apt/lists/* )
COPY cornus /usr/local/bin/cornus
ENV CORNUS_DATA=%s
ENTRYPOINT ["/usr/local/bin/cornus"]
`

// selfBinary resolves the path of the currently running executable.
func selfBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks so the content hash is of the real binary.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// binaryTag is the first 12 hex digits of the binary's SHA-256 — a content
// address, so the image is rebuilt exactly when the binary changes.
func binaryTag(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// hostBaseImage picks a base image matching the host distribution, so a
// dynamically linked cornus binary finds the libc it was built against.
func hostBaseImage() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return FallbackBaseImage
	}
	var id, versionID string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			versionID = v
		}
	}
	if id == "" || versionID == "" {
		return FallbackBaseImage
	}
	switch id {
	case "ubuntu", "debian":
		return id + ":" + versionID
	default:
		// Any other distro (including derivatives) is not assumed to have a
		// matching Docker Hub tag; the fallback still works for a static binary.
		return FallbackBaseImage
	}
}

// ensureSelfImage builds an image containing the running cornus binary, reusing
// it when one already exists for this binary's content hash. Returns the ref.
func (c *client) ensureSelfImage(ctx context.Context, base string) (string, error) {
	exe, err := selfBinary()
	if err != nil {
		return "", fmt.Errorf("locating own binary: %w", err)
	}
	tag, err := binaryTag(exe)
	if err != nil {
		return "", fmt.Errorf("hashing own binary: %w", err)
	}
	ref := SelfImageRepo + ":" + tag

	resp, err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return ref, nil // already built for this exact binary
	}

	if base == "" {
		base = hostBaseImage()
	}
	if err := c.buildSelfImage(ctx, ref, base, exe); err != nil {
		return "", err
	}
	return ref, nil
}

// buildSelfImage POSTs a build context (Dockerfile + the binary) to the Docker
// daemon. The context is streamed rather than buffered: the cornus binary is
// well over a hundred megabytes.
func (c *client) buildSelfImage(ctx context.Context, ref, base, exe string) error {
	fi, err := os.Stat(exe)
	if err != nil {
		return err
	}

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(writeBuildContext(pw, fmt.Sprintf(selfImageDockerfile, base, dataDir), exe, fi.Size()))
	}()
	defer pr.Close()

	q := url.Values{}
	q.Set("t", ref)
	q.Set("dockerfile", "Dockerfile")
	// The classic builder needs no session; keep intermediate containers off.
	q.Set("rm", "1")
	q.Set("forcerm", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/build?"+q.Encode(), pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("build builder image: %w", err)
	}
	defer resp.Body.Close()
	if err := statusErr(resp, http.StatusOK); err != nil {
		return fmt.Errorf("build builder image: %w", err)
	}
	return buildStreamErr(resp.Body)
}

// writeBuildContext streams a tar holding the Dockerfile and the cornus binary.
func writeBuildContext(w io.Writer, dockerfile, exe string, size int64) error {
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0o644,
		Size: int64(len(dockerfile)),
	}); err != nil {
		return err
	}
	if _, err := io.WriteString(tw, dockerfile); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: "cornus",
		Mode: 0o755,
		Size: size,
	}); err != nil {
		return err
	}
	f, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer f.Close()
	// Copy exactly the header's size: a binary replaced mid-copy would otherwise
	// desynchronize the tar stream.
	if _, err := io.CopyN(tw, f, size); err != nil {
		return err
	}
	return tw.Close()
}

// buildStreamErr drains the daemon's JSON build stream and reports the first
// error it carries. A failed build still returns HTTP 200, with the failure only
// inside the stream, so this cannot be skipped.
func buildStreamErr(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			// Trailing non-JSON is not itself a build failure.
			return nil
		}
		if msg.ErrorDetail.Message != "" {
			return fmt.Errorf("build builder image: %s", msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return fmt.Errorf("build builder image: %s", msg.Error)
		}
	}
}
