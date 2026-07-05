// Package awsimds is ONE optional, interchangeable delivery adapter: it renders a
// neutral cornus credential in the shapes AWS SDKs expect, so an unmodified SDK
// picks it up with no app change. It is pure HTTP (no AWS SDK dependency) and is
// always compiled; it is not the credential surface, just one shape among the
// providers registered in pkg/creddelivery. GCP / Azure metadata adapters slot in
// the same way.
//
// It answers two shapes over one mux:
//   - ECS container credentials: GET /creds -> {AccessKeyId, SecretAccessKey,
//     Token, Expiration}. Advertised via AWS_CONTAINER_CREDENTIALS_FULL_URI when
//     bound to loopback.
//   - EC2 IMDSv2: PUT /latest/api/token, then GET
//     /latest/meta-data/iam/security-credentials/[<role>]. Reached at the
//     well-known 169.254.169.254 with no env when WellKnown binding is enabled.
package awsimds

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
)

func init() {
	creddelivery.Register("aws-imds", func(map[string]string) (creddelivery.Endpoint, error) { return endpoint{}, nil })
}

// roleName is the synthetic IAM role the IMDS listing advertises.
const roleName = "cornus"

type endpoint struct{}

func (endpoint) Serve(ctx context.Context, ln net.Listener, get creddelivery.Getter) error {
	mux := http.NewServeMux()

	// ECS container-credentials shape.
	mux.HandleFunc("/creds", func(w http.ResponseWriter, r *http.Request) {
		writeCreds(w, r, get)
	})

	// EC2 IMDSv2 token endpoint (v1 clients simply skip this).
	mux.HandleFunc("/latest/api/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "21600")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("cornus-imds-token"))
	})

	// IMDS role listing and per-role credentials.
	const base = "/latest/meta-data/iam/security-credentials/"
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, base) == "" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(roleName))
			return
		}
		writeCreds(w, r, get)
	})

	srv := &http.Server{Handler: mux}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (endpoint) Env(name, addr string) map[string]string {
	// Loopback advertisement uses the ECS mechanism (the link-local IMDS IP is not
	// advertised via env; SDKs reach 169.254.169.254 directly when WellKnown binds).
	return map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://" + addr + "/creds"}
}

func (endpoint) WellKnownAddr() string { return "169.254.169.254:80" }

// imdsCreds is the JSON both AWS shapes return (a superset; ECS ignores the
// IMDS-only fields).
type imdsCreds struct {
	Code            string `json:"Code"`
	LastUpdated     string `json:"LastUpdated"`
	Type            string `json:"Type"`
	AccessKeyId     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Expiration      string `json:"Expiration"`
}

func writeCreds(w http.ResponseWriter, r *http.Request, get creddelivery.Getter) {
	cred, err := get(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := imdsCreds{
		Code:            "Success",
		LastUpdated:     time.Now().UTC().Format(time.RFC3339),
		Type:            "AWS-HMAC",
		AccessKeyId:     aws(cred, "AccessKeyId", "aws_access_key_id", "access_key_id"),
		SecretAccessKey: aws(cred, "SecretAccessKey", "aws_secret_access_key", "secret_access_key"),
		Token:           aws(cred, "SessionToken", "Token", "aws_session_token", "session_token"),
	}
	if !cred.Expiration.IsZero() {
		out.Expiration = cred.Expiration.UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func aws(cred credential.Credential, aliases ...string) string {
	for _, a := range aliases {
		if v, ok := cred.Values[a]; ok {
			return v
		}
	}
	return ""
}
