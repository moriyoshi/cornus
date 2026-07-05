package creddelivery_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cornus/pkg/creddelivery"
	_ "cornus/pkg/creddelivery/awsimds"
	_ "cornus/pkg/creddelivery/generic"
	"cornus/pkg/credential"
)

func testCred() credential.Credential {
	return credential.Credential{
		Values:     map[string]string{"AccessKeyId": "AKIA", "SecretAccessKey": "sk", "SessionToken": "tok"},
		Expiration: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// serveOnLoopback opens an endpoint on 127.0.0.1 and returns its base URL.
func serveOnLoopback(t *testing.T, provider string) string {
	t.Helper()
	ep, err := creddelivery.Open(provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ep.Serve(ctx, ln, func(context.Context) (credential.Credential, error) { return testCred(), nil })
	return "http://" + ln.Addr().String()
}

func TestGenericEndpoint(t *testing.T) {
	base := serveOnLoopback(t, "generic")
	resp, err := http.Get(base + "/credentials/db")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got credential.Credential
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Values["AccessKeyId"] != "AKIA" || got.Expiration.Year() != 2030 {
		t.Fatalf("generic body = %+v", got)
	}
}

func TestGenericEnv(t *testing.T) {
	ep, _ := creddelivery.Open("generic", nil)
	env := ep.Env("my-db", "127.0.0.1:19100")
	if env["CORNUS_CREDENTIALS_URL"] == "" || !strings.Contains(env["CORNUS_CREDENTIAL_MY_DB_URL"], "my-db") {
		t.Fatalf("generic env = %v", env)
	}
	if ep.WellKnownAddr() != "" {
		t.Fatalf("generic should have no well-known addr, got %q", ep.WellKnownAddr())
	}
}

func TestAWSIMDSEcs(t *testing.T) {
	base := serveOnLoopback(t, "aws-imds")
	resp, err := http.Get(base + "/creds")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	if got["AccessKeyId"] != "AKIA" || got["Token"] != "tok" || got["Code"] != "Success" {
		t.Fatalf("ecs creds = %v", got)
	}
}

func TestAWSIMDSv2Flow(t *testing.T) {
	base := serveOnLoopback(t, "aws-imds")

	// PUT token.
	req, _ := http.NewRequest(http.MethodPut, base+"/latest/api/token", nil)
	tr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	tokBody, _ := io.ReadAll(tr.Body)
	tr.Body.Close()
	if len(tokBody) == 0 || tr.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds") == "" {
		t.Fatalf("imds token response bad: body=%q ttl=%q", tokBody, tr.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds"))
	}

	// Role listing.
	lr, _ := http.Get(base + "/latest/meta-data/iam/security-credentials/")
	role, _ := io.ReadAll(lr.Body)
	lr.Body.Close()
	if strings.TrimSpace(string(role)) == "" {
		t.Fatal("empty imds role listing")
	}

	// Per-role credentials.
	cr, _ := http.Get(base + "/latest/meta-data/iam/security-credentials/" + strings.TrimSpace(string(role)))
	var got map[string]string
	json.NewDecoder(cr.Body).Decode(&got)
	cr.Body.Close()
	if got["AccessKeyId"] != "AKIA" || got["Expiration"] == "" {
		t.Fatalf("imds role creds = %v", got)
	}
}

func TestAWSIMDSWellKnown(t *testing.T) {
	ep, _ := creddelivery.Open("aws-imds", nil)
	if ep.WellKnownAddr() != "169.254.169.254:80" {
		t.Fatalf("aws-imds well-known addr = %q", ep.WellKnownAddr())
	}
	if ep.Env("x", "127.0.0.1:1")["AWS_CONTAINER_CREDENTIALS_FULL_URI"] == "" {
		t.Fatal("aws-imds missing AWS_CONTAINER_CREDENTIALS_FULL_URI env")
	}
}

func TestRenderFormats(t *testing.T) {
	cred := testCred()

	jsonOut, err := creddelivery.Render(cred, "json")
	if err != nil || !strings.Contains(string(jsonOut), "AccessKeyId") {
		t.Fatalf("json render: %q err=%v", jsonOut, err)
	}

	envOut, _ := creddelivery.Render(cred, "env")
	if !strings.Contains(string(envOut), "AccessKeyId=AKIA\n") {
		t.Fatalf("env render: %q", envOut)
	}

	ini, _ := creddelivery.Render(cred, "aws-credentials")
	if !strings.Contains(string(ini), "[default]") || !strings.Contains(string(ini), "aws_access_key_id = AKIA") || !strings.Contains(string(ini), "aws_session_token = tok") {
		t.Fatalf("aws-credentials render: %q", ini)
	}

	raw, err := creddelivery.Render(credential.Credential{Values: map[string]string{"value": "solo"}}, "raw")
	if err != nil || string(raw) != "solo" {
		t.Fatalf("raw render: %q err=%v", raw, err)
	}
	if _, err := creddelivery.Render(cred, "raw"); err == nil {
		t.Fatal("raw render of multi-value should error")
	}
	if _, err := creddelivery.Render(cred, "bogus"); err == nil {
		t.Fatal("unknown format should error")
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "creds.json")
	if err := creddelivery.WriteFile(path, "json", testCred()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "AccessKeyId") {
		t.Fatalf("file content = %q", b)
	}
}
