package dockerproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"cornus/pkg/imageref"
)

// registryClient is the capability the proxy needs to reach the cornus server's
// builtin registry (the "local image store"): its host, bearer token, scheme,
// and a TLS-configured transport. *client.Client satisfies it; tests inject a
// fake pointing at an in-process registry.
type registryClient interface {
	Host() string
	RegistryToken() string
	RegistrySecure() bool
	RegistryTransport() http.RoundTripper
}

// handleImagePush serves POST /images/{name}/push. The docker daemon push
// protocol carries no image content — the image is taken from the daemon's
// store — so here the store IS the cornus builtin registry: the image must
// already live there (e.g. placed by `cornus build`, which sends bare tags to
// the builtin registry). The docker CLI normalizes a bare `docker push app` to
// `docker.io/library/app` before the daemon sees it, so a docker.io ref (Docker's
// default registry) is treated as the local store — its `library/` official
// prefix stripped — and reported as pushed there. A {name} naming any OTHER
// registry (ghcr.io, a host:port, ...) is copied from the builtin store (same
// repository path) out to that registry, using the CLI's X-Registry-Auth creds.
func (p *Proxy) handleImagePush(w http.ResponseWriter, r *http.Request, imgName string) {
	ctx := r.Context()
	stream := newJSONStream(w)

	rc, ok := p.attacher.(registryClient)
	if !ok || rc.Host() == "" {
		stream.fail("push unavailable: no builtin registry configured")
		return
	}

	tag := r.URL.Query().Get("tag")
	if tag == "" {
		tag = "latest"
	}
	host, repo := imageref.SplitHostRepo(imgName)
	// docker.io is Docker's default registry, i.e. what a bare `docker push`
	// resolves to — map it to the builtin store, stripping the official
	// `library/` namespace so it lines up with `cornus build -t <bare>`.
	localTarget := host == "" || host == rc.Host()
	if isDockerHub(host) {
		localTarget = true
		repo = strings.TrimPrefix(repo, "library/")
	}

	// Source: the image in the builtin registry (the local store), keyed by its
	// repository path (host-independent) and tag.
	var srcOpts []name.Option
	if !rc.RegistrySecure() {
		srcOpts = append(srcOpts, name.Insecure)
	}
	srcRef, err := name.ParseReference(rc.Host()+"/"+repo+":"+tag, srcOpts...)
	if err != nil {
		stream.fail(fmt.Sprintf("invalid reference %q: %v", imgName, err))
		return
	}
	desc, err := remote.Get(srcRef, p.builtinReadOpts(ctx, rc)...)
	if err != nil {
		// Mirror the docker daemon's "not in the local store" push error.
		stream.fail(fmt.Sprintf("An image does not exist locally with the tag: %s", imgName))
		return
	}

	// A local-store target (bare, the builtin registry, or docker.io) is a push
	// to the local store: the image is already there, so just acknowledge it.
	if localTarget {
		stream.status(fmt.Sprintf("The push refers to repository [%s/%s]", rc.Host(), repo))
		stream.status(fmt.Sprintf("%s: already present in the builtin registry", tag))
		reportPushTrailer(stream, tag, desc.Digest.String(), desc.Size)
		return
	}

	// External target: copy the image from the builtin store out to it. A
	// loopback target is treated as plain HTTP (docker's default insecure set
	// includes 127.0.0.0/8); everything else uses HTTPS.
	var dstNameOpts []name.Option
	if isLoopbackHost(host) {
		dstNameOpts = append(dstNameOpts, name.Insecure)
	}
	dstRef, err := name.ParseReference(imgName+":"+tag, dstNameOpts...)
	if err != nil {
		stream.fail(fmt.Sprintf("invalid reference %q: %v", imgName, err))
		return
	}
	dstOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(registryAuthFrom(r.Header.Get("X-Registry-Auth"))),
	}
	stream.status(fmt.Sprintf("The push refers to repository [%s]", dstRef.Context().Name()))
	if err := copyImage(desc, dstRef, dstOpts); err != nil {
		stream.fail(fmt.Sprintf("pushing %s: %v", dstRef, err))
		return
	}
	reportPushTrailer(stream, tag, desc.Digest.String(), desc.Size)
}

// builtinReadOpts returns the go-containerregistry options for reading the
// builtin registry: the client's TLS transport plus bearer auth (the cornus API
// token, which the registry accepts) when one is configured.
func (p *Proxy) builtinReadOpts(ctx context.Context, rc registryClient) []remote.Option {
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithTransport(rc.RegistryTransport()),
	}
	if tok := rc.RegistryToken(); tok != "" {
		opts = append(opts, remote.WithAuth(authn.FromConfig(authn.AuthConfig{RegistryToken: tok})))
	}
	return opts
}

// isDockerHub reports whether host is one of Docker Hub's names — what the docker
// CLI expands a bare push/pull reference to. cornus treats it as the builtin
// registry (its own default store), not the real Docker Hub.
func isDockerHub(host string) bool {
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	}
	return false
}

// isLoopbackHost reports whether an image-reference host (possibly "host:port")
// is the loopback interface, which docker treats as an insecure (plain-HTTP)
// registry by default.
func isLoopbackHost(host string) bool {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// copyImage writes the image or index behind desc to dstRef.
func copyImage(desc *remote.Descriptor, dstRef name.Reference, opts []remote.Option) error {
	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return err
		}
		return remote.WriteIndex(dstRef, idx, opts...)
	}
	img, err := desc.Image()
	if err != nil {
		return err
	}
	return remote.Write(dstRef, img, opts...)
}

// reportPushTrailer emits the docker push success trailer: a "<tag>: digest:
// ..." line plus the aux PushResult the docker CLI reads for --digestfile.
func reportPushTrailer(s *jsonStream, tag, digest string, size int64) {
	s.status(fmt.Sprintf("%s: digest: %s size: %d", tag, digest, size))
	s.aux(map[string]any{"Tag": tag, "Digest": digest, "Size": size})
}

// registryAuthFrom decodes a docker X-Registry-Auth header (base64 JSON
// AuthConfig) into a go-containerregistry authenticator. An empty or unparsable
// header yields anonymous access.
func registryAuthFrom(h string) authn.Authenticator {
	if h == "" {
		return authn.Anonymous
	}
	var raw []byte
	for _, enc := range []*base64.Encoding{base64.URLEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(h); err == nil {
			raw = b
			break
		}
	}
	if raw == nil {
		return authn.Anonymous
	}
	var ac struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		Auth          string `json:"auth"`
		IdentityToken string `json:"identitytoken"`
		RegistryToken string `json:"registrytoken"`
	}
	if err := json.Unmarshal(raw, &ac); err != nil {
		return authn.Anonymous
	}
	return authn.FromConfig(authn.AuthConfig{
		Username:      ac.Username,
		Password:      ac.Password,
		Auth:          ac.Auth,
		IdentityToken: ac.IdentityToken,
		RegistryToken: ac.RegistryToken,
	})
}
