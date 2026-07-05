//go:build credaws

// Package awssts mints short-lived AWS credentials on the client via STS, using
// the caller's own AWS credential chain (env, shared config, SSO, instance role
// — whatever the dev's machine is configured with). It is one CLOUD-SPECIFIC
// source among many; the cornus core, transport, and delivery remain unaware of
// AWS. Gated behind the `credaws` build tag so the default binary stays lean
// (mirroring pkg/storage's cloudblob gate); a no-op stub registers a clear error
// otherwise.
//
// Config keys:
//
//   - "mode": "assume-role", "session-token", "passthrough", or "auto". Default:
//     assume-role when role_arn is set, else "auto".
//   - "passthrough": hand the workload the caller's already-resolved credentials
//     as-is (no STS call). This is the AWS SSO scenario — the developer's primary
//     identity is itself temporary, so GetSessionToken cannot mint a separate
//     temporary credential; passthrough forwards the live SSO/session credential
//     (the SDK refreshes it from the SSO cache on each fetch).
//   - "auto" (the zero-config default): resolve the caller's credentials and
//     forward them if they are already temporary (SSO / assumed role), otherwise
//     mint a scoped session token from long-term keys. Works for both.
//   - "role_arn", "session_name" (assume-role; session_name default "cornus").
//   - "duration": Go duration for the credential lifetime (default 1h;
//     assume-role / session-token only).
//   - "region": AWS region for the STS client (else the default chain's region).
//   - "external_id": optional AssumeRole ExternalId.
//   - "serial_number", "token_code": optional MFA for either STS mode.
//
// The minted credential carries canonical keys AccessKeyId / SecretAccessKey /
// SessionToken plus the STS expiry, which the aws-imds delivery adapter renders
// into AWS's expected shapes.
package awssts

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"

	"cornus/pkg/credential"
)

func init() { credential.Register("aws-sts", newSource) }

type source struct {
	cfg map[string]string
}

func newSource(cfg map[string]string) (credential.Source, error) {
	mode := cfg["mode"]
	if mode == "" {
		if cfg["role_arn"] != "" {
			mode = "assume-role"
		} else {
			// "auto" (not "session-token") is the safe zero-config default: it works
			// whether the caller's base credentials are long-term IAM user keys OR
			// already-temporary session/SSO credentials. GetSessionToken rejects the
			// latter, so a session-token default would break every SSO user.
			mode = "auto"
		}
	}
	switch mode {
	case "assume-role", "session-token", "passthrough", "auto":
	default:
		return nil, fmt.Errorf("awssts: unknown mode %q (want assume-role, session-token, passthrough, or auto)", mode)
	}
	if mode == "assume-role" && cfg["role_arn"] == "" {
		return nil, fmt.Errorf("awssts: assume-role requires role_arn")
	}
	if d := cfg["duration"]; d != "" {
		if _, err := time.ParseDuration(d); err != nil {
			return nil, fmt.Errorf("awssts: parse duration: %w", err)
		}
	}
	c := map[string]string{"mode": mode}
	for k, v := range cfg {
		c[k] = v
	}
	return &source{cfg: c}, nil
}

func (s *source) duration() time.Duration {
	if d := s.cfg["duration"]; d != "" {
		if v, err := time.ParseDuration(d); err == nil {
			return v
		}
	}
	return time.Hour
}

func (s *source) Fetch(ctx context.Context, _ map[string]string) (credential.Credential, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if r := s.cfg["region"]; r != "" {
		opts = append(opts, awsconfig.WithRegion(r))
	}
	// Static credentials override the default chain — used to authenticate against
	// a private / mock STS endpoint (e.g. an S3-compatible stack, or the
	// winterbaume emulator in tests); omit to use the caller's own AWS chain.
	if ak, sk := s.cfg["access_key"], s.cfg["secret_key"]; ak != "" && sk != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(ak, sk, s.cfg["session_token_in"])))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return credential.Credential{}, fmt.Errorf("awssts: load config: %w", err)
	}

	mode := s.cfg["mode"]
	// "auto": resolve the caller's current credentials and branch on their kind —
	// already-temporary (a session token is present, e.g. SSO or an assumed role)
	// pass straight through; long-term keys mint a scoped session token. This is
	// what makes the zero-config / SSO case work without a separate STS call.
	if mode == "auto" {
		base, err := awsCfg.Credentials.Retrieve(ctx)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("awssts: resolve credentials: %w", err)
		}
		if base.SessionToken != "" {
			return s.credFromChain(base), nil
		}
		mode = "session-token"
	}
	// "passthrough": hand the workload the caller's already-resolved credentials
	// as-is (the primary SSO scenario — the user's primary identity is temporary
	// and cannot issue a separate temporary credential via GetSessionToken). No
	// STS call is made; the SDK's own provider handles SSO refresh on each fetch.
	if mode == "passthrough" {
		base, err := awsCfg.Credentials.Retrieve(ctx)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("awssts: resolve credentials: %w", err)
		}
		return s.credFromChain(base), nil
	}

	// An explicit endpoint points the STS client at a non-AWS endpoint (mirrors
	// pkg/storage/open.go's S3 endpoint override); empty uses the real regional STS.
	var stsOpts []func(*sts.Options)
	if ep := s.cfg["endpoint"]; ep != "" {
		stsOpts = append(stsOpts, func(o *sts.Options) { o.BaseEndpoint = aws.String(ep) })
	}
	cl := sts.NewFromConfig(awsCfg, stsOpts...)
	secs := int32(s.duration() / time.Second)

	var (
		accessKey, secretKey, sessionToken string
		expires                            time.Time
	)
	switch mode {
	case "assume-role":
		name := s.cfg["session_name"]
		if name == "" {
			name = "cornus"
		}
		in := &sts.AssumeRoleInput{
			RoleArn:         aws.String(s.cfg["role_arn"]),
			RoleSessionName: aws.String(name),
			DurationSeconds: aws.Int32(secs),
		}
		if v := s.cfg["external_id"]; v != "" {
			in.ExternalId = aws.String(v)
		}
		if v := s.cfg["serial_number"]; v != "" {
			in.SerialNumber = aws.String(v)
		}
		if v := s.cfg["token_code"]; v != "" {
			in.TokenCode = aws.String(v)
		}
		out, err := cl.AssumeRole(ctx, in)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("awssts: assume-role: %w", err)
		}
		accessKey, secretKey, sessionToken, expires = deref(out.Credentials)
	case "session-token":
		in := &sts.GetSessionTokenInput{DurationSeconds: aws.Int32(secs)}
		if v := s.cfg["serial_number"]; v != "" {
			in.SerialNumber = aws.String(v)
		}
		if v := s.cfg["token_code"]; v != "" {
			in.TokenCode = aws.String(v)
		}
		out, err := cl.GetSessionToken(ctx, in)
		if err != nil {
			return credential.Credential{}, fmt.Errorf("awssts: get-session-token: %w", err)
		}
		accessKey, secretKey, sessionToken, expires = deref(out.Credentials)
	}

	values := map[string]string{
		"AccessKeyId":     accessKey,
		"SecretAccessKey": secretKey,
		"SessionToken":    sessionToken,
	}
	if r := s.cfg["region"]; r != "" {
		values["Region"] = r
	}
	return credential.Credential{Values: values, Expiration: expires}, nil
}

// credFromChain maps the caller's already-resolved credentials (from the default
// provider chain — env, shared config, SSO, an assumed role, instance role) into
// a neutral Credential. Temporary credentials carry an expiry (CanExpire), which
// drives the broker's refresh cadence; long-term keys have none.
func (s *source) credFromChain(c aws.Credentials) credential.Credential {
	values := map[string]string{
		"AccessKeyId":     c.AccessKeyID,
		"SecretAccessKey": c.SecretAccessKey,
	}
	if c.SessionToken != "" {
		values["SessionToken"] = c.SessionToken
	}
	if r := s.cfg["region"]; r != "" {
		values["Region"] = r
	}
	cred := credential.Credential{Values: values}
	if c.CanExpire {
		cred.Expiration = c.Expires
	}
	return cred
}

func deref(c *ststypes.Credentials) (ak, sk, st string, exp time.Time) {
	if c == nil {
		return
	}
	if c.AccessKeyId != nil {
		ak = *c.AccessKeyId
	}
	if c.SecretAccessKey != nil {
		sk = *c.SecretAccessKey
	}
	if c.SessionToken != nil {
		st = *c.SessionToken
	}
	if c.Expiration != nil {
		exp = *c.Expiration
	}
	return
}
