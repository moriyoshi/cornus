package dockerhost

// Flavor is what makes one Backend type able to serve two runtimes without
// lying to the operator about which one it drove.
//
// Two properties are pinned here, and neither is checked anywhere else:
//
//   - the ZERO Flavor reads as "dockerhost". Every existing construction site —
//     the server factory, the local CLI, SelfInspector, and a long tail of tests
//     — builds a Backend without mentioning a flavor. If the zero value ever
//     produced "" instead, Name() would report an empty backend and every error
//     in the package would begin with ": ", and nothing would fail to compile.
//   - errf still WRAPS. The 31 error sites were converted from
//     fmt.Errorf("dockerhost: ...") to b.errf("..."), and three of them carry
//     %w on deploy.ErrNotFound, which pkg/server maps to a 404 via errors.Is. A
//     conversion that dropped the wrap would turn every "no such deployment"
//     into a 500 with the same message text, so asserting the STRING would not
//     have caught it.

import (
	"errors"
	"testing"

	"cornus/pkg/deploy"
)

func TestFlavorZeroValueReadsAsDockerhost(t *testing.T) {
	b := &Backend{} // no WithFlavor, as every pre-podman caller constructs it
	if got := b.Name(); got != "dockerhost" {
		t.Errorf("zero-flavor Name() = %q, want %q", got, "dockerhost")
	}
	if got := b.errf("boom").Error(); got != "dockerhost: boom" {
		t.Errorf("zero-flavor errf = %q, want %q", got, "dockerhost: boom")
	}
}

func TestFlavorNamesTheRuntimeItDrives(t *testing.T) {
	for _, tc := range []struct {
		flavor Flavor
		want   string
	}{
		{FlavorDocker, "dockerhost"},
		{FlavorPodman, "podman"},
	} {
		b := &Backend{}
		WithFlavor(tc.flavor)(b)
		if got := b.Name(); got != tc.want {
			t.Errorf("Name() with flavor %q = %q, want %q", tc.flavor, got, tc.want)
		}
		if got := b.errf("boom").Error(); got != tc.want+": boom" {
			t.Errorf("errf with flavor %q = %q, want %q", tc.flavor, got, tc.want+": boom")
		}
	}
}

// TestErrfPreservesWrapping is the one that matters for behaviour rather than
// wording: pkg/server turns deploy.ErrNotFound into a 404 through errors.Is, so
// the wrap has to survive the prefixing.
func TestErrfPreservesWrapping(t *testing.T) {
	b := &Backend{}
	err := b.errf("deployment %q: %w", "svc", deploy.ErrNotFound)
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("errf dropped the wrap: errors.Is(%v, ErrNotFound) = false; "+
			"a caller mapping ErrNotFound to 404 would return 500 instead", err)
	}
	if got, want := err.Error(), `dockerhost: deployment "svc": `+deploy.ErrNotFound.Error(); got != want {
		t.Errorf("errf = %q, want %q", got, want)
	}
}

// TestPodmanFlavorErrorsDoNotSayDockerhost guards the operator-facing half: a
// podman server whose errors say "dockerhost" sends the reader to the wrong
// runtime's documentation, and nothing contradicts them.
func TestPodmanFlavorErrorsDoNotSayDockerhost(t *testing.T) {
	b := &Backend{}
	WithFlavor(FlavorPodman)(b)
	err := b.errf("deployment %q: %w", "svc", deploy.ErrNotFound)
	if got := err.Error(); got[:len("podman: ")] != "podman: " {
		t.Errorf("podman-flavor error = %q, want it to start with %q", got, "podman: ")
	}
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Error("podman-flavor errf dropped the ErrNotFound wrap")
	}
}
