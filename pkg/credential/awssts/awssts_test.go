//go:build credaws

package awssts_test

import (
	"context"
	"os"
	"testing"

	"cornus/pkg/credential"

	_ "cornus/pkg/credential/awssts"
)

// TestAWSSTSIntegration exercises the aws-sts source against a real STS API
// served by an emulator (the winterbaume mock — winterbaume-sts is 11/11
// operations, state-backed). It is opt-in: set CORNUS_TEST_STS_ENDPOINT to the
// endpoint URL to run it, mirroring pkg/storage/s3_test.go. Optional env:
// AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (default "test"),
// CORNUS_TEST_STS_ROLE_ARN (default a synthetic role arn for assume-role).
//
// Run winterbaume first, then this test (needs -tags credaws):
//
//	cd ../winterbaume && ./.agents/bin/cargo.sh run -p winterbaume-server -- --host 127.0.0.1 --port 5555
//	CORNUS_TEST_STS_ENDPOINT=http://127.0.0.1:5555 \
//	  go test -tags credaws ./pkg/credential/awssts/ -run STS -v
func TestAWSSTSIntegration(t *testing.T) {
	endpoint := os.Getenv("CORNUS_TEST_STS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CORNUS_TEST_STS_ENDPOINT to run the STS integration test (e.g. winterbaume)")
	}
	ak := envOr("AWS_ACCESS_KEY_ID", "test")
	sk := envOr("AWS_SECRET_ACCESS_KEY", "test")
	roleArn := envOr("CORNUS_TEST_STS_ROLE_ARN", "arn:aws:iam::123456789012:role/cornus-test")

	base := map[string]string{
		"region":     "us-east-1",
		"endpoint":   endpoint,
		"access_key": ak,
		"secret_key": sk,
		"duration":   "15m",
	}

	t.Run("session-token", func(t *testing.T) {
		cfg := cloneWith(base, map[string]string{"mode": "session-token"})
		assertMinted(t, cfg)
	})

	t.Run("assume-role", func(t *testing.T) {
		cfg := cloneWith(base, map[string]string{"mode": "assume-role", "role_arn": roleArn, "session_name": "cornus-e2e"})
		assertMinted(t, cfg)
	})
}

// TestAWSSTSPassthroughSSO is HERMETIC (no STS endpoint needed): it models the
// AWS SSO scenario where the caller's primary credentials are already temporary
// (a session token), so GetSessionToken would fail. passthrough / auto must
// forward those credentials as-is, making no STS call. Runs whenever the credaws
// binary is tested; named "...STS..." so the integration-backends -run filter
// picks it up alongside the endpoint-gated test.
func TestAWSSTSPassthroughSSO(t *testing.T) {
	// A temporary/session credential in the environment (what `aws sso login`
	// effectively yields to the default chain).
	t.Setenv("AWS_ACCESS_KEY_ID", "ASIA_SSO_EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sso-secret")
	t.Setenv("AWS_SESSION_TOKEN", "sso-session-token")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true") // never reach IMDS

	for _, mode := range []string{"passthrough", "auto", ""} {
		t.Run("mode="+modeName(mode), func(t *testing.T) {
			cfg := map[string]string{"region": "us-east-1"}
			if mode != "" {
				cfg["mode"] = mode
			}
			src, err := credential.Open("aws-sts", cfg)
			if err != nil {
				t.Fatal(err)
			}
			cred, err := src.Fetch(context.Background(), nil)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if cred.Values["AccessKeyId"] != "ASIA_SSO_EXAMPLE" ||
				cred.Values["SecretAccessKey"] != "sso-secret" ||
				cred.Values["SessionToken"] != "sso-session-token" {
				t.Fatalf("passthrough did not forward the SSO session credentials: %v", cred.Values)
			}
		})
	}
}

func modeName(m string) string {
	if m == "" {
		return "default"
	}
	return m
}

func assertMinted(t *testing.T, cfg map[string]string) {
	t.Helper()
	src, err := credential.Open("aws-sts", cfg)
	if err != nil {
		t.Fatalf("open aws-sts: %v", err)
	}
	cred, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, k := range []string{"AccessKeyId", "SecretAccessKey", "SessionToken"} {
		if cred.Values[k] == "" {
			t.Errorf("minted credential missing %s: %v", k, cred.Values)
		}
	}
	if cred.Expiration.IsZero() {
		t.Error("minted credential has no expiration")
	}
}

func cloneWith(base, over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
