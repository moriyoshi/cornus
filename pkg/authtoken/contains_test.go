package authtoken

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestContains pins the containment relation that bounds a downscope request at
// the token-exchange endpoint.
//
// The cases that matter are the INCOMPARABLE ones. AccessRegistryPush and
// AccessCaretaker reach disjoint endpoints, so neither contains the other and
// both directions must be false — which is exactly what an ordering derived from
// the enum's declaration order would get wrong, since AccessCaretaker happens to
// sort below AccessRegistryPush. Comparing what each access actually reaches is
// what makes the answer independent of how the constants happen to be written.
func TestContains(t *testing.T) {
	for _, tc := range []struct {
		name         string
		outer, inner Access
		want         bool
	}{
		{"full contains itself", AccessFull, AccessFull, true},
		{"full contains push", AccessFull, AccessRegistryPush, true},
		{"full contains caretaker", AccessFull, AccessCaretaker, true},
		{"full contains peer", AccessFull, AccessPeer, true},
		{"push contains pull", AccessRegistryPush, AccessRegistryPull, true},

		{"pull does not contain push", AccessRegistryPull, AccessRegistryPush, false},
		{"pull does not contain full", AccessRegistryPull, AccessFull, false},

		// Incomparable in both directions, despite their enum order.
		{"push does not contain caretaker", AccessRegistryPush, AccessCaretaker, false},
		{"caretaker does not contain push", AccessCaretaker, AccessRegistryPush, false},
		{"peer does not contain pull", AccessPeer, AccessRegistryPull, false},

		// AccessNone reaches nothing, so everything contains it and it contains
		// nothing but itself. The first of those is why the exchange endpoint
		// checks issuableAccess separately: containment alone would happily admit
		// a request for a scope that grants nothing.
		{"none is contained by anything", AccessRegistryPull, AccessNone, true},
		{"none contains nothing", AccessNone, AccessRegistryPull, false},
		{"none contains none", AccessNone, AccessNone, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Contains(tc.outer, tc.inner); got != tc.want {
				t.Fatalf("Contains(%v, %v) = %v, want %v", tc.outer, tc.inner, got, tc.want)
			}
		})
	}
}

// TestIssuedTokenIsURLUserinfoSafe pins a constraint that currently holds by
// accident and would break exactly one thing, silently, if it stopped.
//
// A minted registry-pull credential is carried to the incus backend inside a URL
// userinfo component: incushost builds `scheme://user:pass@registry` with
// url.UserPassword().String(), and incusd base64s the PERCENT-ENCODED result into
// skopeo's authfile with nothing undoing the escapes
// (pkg/deploy/incushost/image_linux.go). Any character Go escapes there arrives
// at skopeo still escaped, and the pull fails with a credential that looks
// correct in every log that prints it.
//
// It survives because a compact JWS is base64url plus "." separators, and every
// one of those characters is unreserved in userinfo — so url.UserPassword
// escapes nothing. This asserts that property of the ISSUED token directly,
// rather than trusting the shape of base64url to stay in agreement with the
// shape of RFC 3986.
func TestIssuedTokenIsURLUserinfoSafe(t *testing.T) {
	tok, err := Issue(IssueOptions{
		Subject:     "cornus-internal",
		Scope:       ScopeRegistryPull,
		TTL:         time.Hour,
		HS256Secret: []byte("this-is-a-32-byte-minimum-secret!!"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if escaped := url.UserPassword("cornus-internal", tok).String(); strings.Contains(escaped, "%") {
		t.Fatalf("the issued token percent-escapes inside a URL userinfo (%q); incus registry pulls "+
			"would receive it still escaped — see internalPullCredentialsWithTTL", escaped)
	}
	// The property that makes it true, asserted separately so a failure says WHY:
	// every character of a compact JWS must be unreserved in userinfo.
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	for _, r := range tok {
		if !strings.ContainsRune(unreserved, r) {
			t.Fatalf("issued token contains %q, which is not unreserved in a URL userinfo component", r)
		}
	}
}
