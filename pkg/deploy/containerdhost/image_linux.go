//go:build linux

package containerdhost

import (
	"context"
	"fmt"
	"os"
	"strings"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/distribution/reference"
)

// resolver builds the registry resolver for image pulls. Unlike the dockerhost
// backend — where the Docker daemon's own insecure-registries config governs
// plain-HTTP pulls — the containerd backend must decide itself: localhost
// registries (the common case: the cornus registry on the same host) are
// plain-HTTP automatically, and CORNUS_CONTAINERD_INSECURE_REGISTRIES extends
// that to an explicit comma-separated host[:port] list. Everything else
// (docker.io, private HTTPS registries) resolves normally.
func (b *Backend) resolver() remotes.Resolver {
	insecure := parseInsecureRegistries(os.Getenv("CORNUS_CONTAINERD_INSECURE_REGISTRIES"))
	matcher := func(host string) (bool, error) {
		if ok, _ := docker.MatchLocalhost(host); ok {
			return true, nil
		}
		return insecure[host], nil
	}
	return docker.NewResolver(docker.ResolverOptions{
		// ConfigureDefaultRegistries does NOT attach an Authorizer, and without
		// one public registries fail with a bare 401: the anonymous bearer-token
		// dance (WWW-Authenticate -> auth.docker.io token -> retry) never runs.
		// NewDockerAuthorizer with no credentials implements exactly that flow.
		Hosts: docker.ConfigureDefaultRegistries(
			docker.WithAuthorizer(docker.NewDockerAuthorizer()),
			docker.WithPlainHTTP(matcher),
		),
	})
}

func parseInsecureRegistries(v string) map[string]bool {
	out := map[string]bool{}
	for _, h := range strings.Split(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out[h] = true
		}
	}
	return out
}

// normalizeRef expands a docker-style short image name into the fully
// qualified reference containerd's resolver requires — dockerd does this
// normalization itself, but docker.NewResolver rejects unqualified names
// ("nginx:1.27-alpine" parses as scheme-less host "nginx" with a bogus port):
//
//	nginx              → docker.io/library/nginx:latest
//	nginx:1.27-alpine  → docker.io/library/nginx:1.27-alpine
//	user/repo:tag      → docker.io/user/repo:tag
//
// Already-qualified refs (127.0.0.1:5000/x:y, localhost:5000/x:y,
// ghcr.io/a/b@sha256:...) pass through unchanged, so the resolver's
// localhost plain-HTTP matching keeps working on the same host strings.
func normalizeRef(ref string) (string, error) {
	named, err := reference.ParseDockerRef(ref)
	if err != nil {
		return "", fmt.Errorf("containerd: invalid image reference %q: %w", ref, err)
	}
	return named.String(), nil
}

// pullImage pulls (and unpacks) the spec's image ref. When the registry is
// unreachable but the ref is already present in the namespace's image store —
// e.g. just built there by the containerd build worker — it falls back to the
// local image, so a same-host build-then-deploy needs no registry round trip.
//
// The ref is normalized once here — the single choke point through which every
// image lookup flows — so both the pull and the local-store fallback use the
// fully qualified name. containerd's image store (and BuildKit's containerd
// worker, which records tagged builds there) keys images by that normalized
// form, so lookups stay consistent; container records created from the pulled
// image carry the normalized name too.
func (b *Backend) pullImage(ctx context.Context, ref string) (ctd.Image, error) {
	full, err := normalizeRef(ref)
	if err != nil {
		return nil, err
	}
	pullOpts := []ctd.RemoteOpt{ctd.WithPullUnpack, ctd.WithResolver(b.resolver())}
	if b.snapshotter != "" {
		pullOpts = append(pullOpts, ctd.WithPullSnapshotter(b.snapshotter))
	}
	img, err := b.client.Pull(ctx, full, pullOpts...)
	if err == nil {
		return img, nil
	}
	if local, lerr := b.client.GetImage(ctx, full); lerr == nil {
		// Empty snapshotter means containerd's default, matching the pull path.
		if uerr := local.Unpack(ctx, b.snapshotter); uerr == nil {
			return local, nil
		}
	}
	return nil, fmt.Errorf("containerd: pull %s: %w", ref, err)
}
