package kubeauth

import (
	"context"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// tokenReactor makes a fake clientset answer TokenRequest creates with a fixed
// token, capturing the audience and expiration the code requested.
func tokenReactor(token string, gotAud *[]string, gotExp *int64) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(k8stesting.CreateAction)
		if !ok || ca.GetSubresource() != "token" {
			return false, nil, nil
		}
		tr := ca.GetObject().(*authenticationv1.TokenRequest)
		*gotAud = tr.Spec.Audiences
		if tr.Spec.ExpirationSeconds != nil {
			*gotExp = *tr.Spec.ExpirationSeconds
		}
		out := tr.DeepCopy()
		out.Status.Token = token
		return true, out, nil
	}
}

func TestMintRequestsAudienceAndExpiration(t *testing.T) {
	var gotAud []string
	var gotExp int64
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", tokenReactor("minted-jwt", &gotAud, &gotExp))

	tok, err := mint(context.Background(), cs, "cornus", Options{ServiceAccount: "cornus-client", Audience: "cornus", ExpirationSeconds: 1800})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok != "minted-jwt" {
		t.Errorf("token = %q, want minted-jwt", tok)
	}
	if len(gotAud) != 1 || gotAud[0] != "cornus" {
		t.Errorf("requested audiences = %v, want [cornus]", gotAud)
	}
	if gotExp != 1800 {
		t.Errorf("requested expiration = %d, want 1800", gotExp)
	}
}

func TestMintDefaultsExpiration(t *testing.T) {
	var gotAud []string
	var gotExp int64
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", tokenReactor("t", &gotAud, &gotExp))

	if _, err := mint(context.Background(), cs, "ns", Options{ServiceAccount: "sa", Audience: "aud"}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if gotExp != defaultExpirationSeconds {
		t.Errorf("expiration = %d, want default %d", gotExp, defaultExpirationSeconds)
	}
}

func TestMintEmptyTokenIsError(t *testing.T) {
	var gotAud []string
	var gotExp int64
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", tokenReactor("", &gotAud, &gotExp)) // server returns no token
	if _, err := mint(context.Background(), cs, "ns", Options{ServiceAccount: "sa", Audience: "aud"}); err == nil {
		t.Error("mint with empty token = nil error, want error")
	}
}

func TestTokenValidatesOptions(t *testing.T) {
	// Missing service account / audience fail before any cluster access, so no
	// kubeconfig is needed.
	if _, err := Token(context.Background(), Options{Audience: "cornus"}); err == nil {
		t.Error("Token without service account = nil error, want error")
	}
	if _, err := Token(context.Background(), Options{ServiceAccount: "sa"}); err == nil {
		t.Error("Token without audience = nil error, want error")
	}
}
