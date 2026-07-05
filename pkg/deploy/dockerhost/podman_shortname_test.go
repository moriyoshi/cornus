package dockerhost

import "testing"

// The two directions this function must get right fail in opposite ways, and both
// fail silently:
//
//   - leaving a short name alone -> podman rejects the deploy outright
//     ("short-name did not resolve to an alias"), which at least says so;
//   - rewriting a loopback ref -> the pull goes to Docker Hub looking for an
//     image that only ever existed on cornus's own registry, and the error names
//     Docker Hub. Nothing points at the rewrite.
//
// So the loopback/localhost cases below are not padding. They are the half of the
// contract whose breakage is hard to trace back here.
func TestQualifyImageRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		// Short names: what podman cannot resolve on its own.
		{"official image, no tag", "nginx", "docker.io/library/nginx"},
		{"official image with tag", "nginx:alpine", "docker.io/library/nginx:alpine"},
		{"hub user namespace", "bitnami/redis:7", "docker.io/bitnami/redis:7"},

		// Already qualified: must pass through untouched.
		{"explicit docker.io", "docker.io/library/nginx:alpine", "docker.io/library/nginx:alpine"},
		{"third-party registry", "quay.io/podman/stable", "quay.io/podman/stable"},
		{"registry with port", "registry.example.com:5000/team/app:v1", "registry.example.com:5000/team/app:v1"},
		{"deep path", "ghcr.io/org/team/app:v1", "ghcr.io/org/team/app:v1"},

		// The ones cornus itself produces every build. A rewrite here sends the
		// pull to Docker Hub for an image that exists only locally.
		{"loopback ip with port", "127.0.0.1:39715/demo:latest", "127.0.0.1:39715/demo:latest"},
		{"localhost with port", "localhost:5000/app", "localhost:5000/app"},
		{"bare localhost", "localhost/app", "localhost/app"},

		// A digest is not a name; prefixing a host would corrupt it.
		{"bare digest", "sha256:0123456789abcdef", "sha256:0123456789abcdef"},
		{"empty", "", ""},

		// A tag containing a dot must not make the NAME look like a host: the
		// registry test applies to the first path component only.
		{"dotted tag on a short name", "nginx:1.27-alpine", "docker.io/library/nginx:1.27-alpine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualifyImageRef(tc.in); got != tc.want {
				t.Errorf("qualifyImageRef(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The engine boundary is where qualification happens, so the create body — the
// thing podman actually looks the image up by — must carry the qualified name.
// A pull stored as docker.io/library/nginx that create then asks for as "nginx"
// fails at create time with the pull already reported as successful.
func TestSpecGeneratorImageIsQualified(t *testing.T) {
	spec, err := toSpecGenerator("demo-0", createBody{Image: "nginx:1.27-alpine"})
	if err != nil {
		t.Fatalf("toSpecGenerator: %v", err)
	}
	if spec.Image != "docker.io/library/nginx:1.27-alpine" {
		t.Errorf("SpecGenerator.image = %q, want the qualified name; podman resolves no short name itself, "+
			"so create would fail even though the pull succeeded", spec.Image)
	}

	// ...and the loopback ref cornus builds to must survive create unchanged.
	spec, err = toSpecGenerator("demo-0", createBody{Image: "127.0.0.1:39715/demo:latest"})
	if err != nil {
		t.Fatalf("toSpecGenerator: %v", err)
	}
	if spec.Image != "127.0.0.1:39715/demo:latest" {
		t.Errorf("SpecGenerator.image = %q, want the loopback ref untouched", spec.Image)
	}
}
