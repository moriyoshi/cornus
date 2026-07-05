// Package ingresspub acts on a deployment's declared `ingress.tunnel` opt-in: it
// asks the server to publish the ingress through a public tunnel and prints the
// resulting URL.
//
// It lives client-side on purpose. Hosting a tunnel needs a credential, and a
// DeploySpec is persisted into backend labels and echoed back in status, so a
// credential must never travel on one. The declaration says "publish me"; the
// client supplies the secret over the authenticated tunnel endpoint. That split
// is why this is a separate step after the deploy rather than something the
// server does while applying.
package ingresspub

import (
	"context"
	"fmt"
	"os"
	"strings"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/clientconfig"
)

// ResolveAuthToken picks the tunnel credential to inject: at most one of a direct
// token or a path to read it from (the latter trimmed of a single trailing
// newline, matching how `kubectl create secret --from-file` round-trips a file).
// If neither yields a value it falls back to the legacy NGROK_AUTHTOKEN env var,
// which many ngrok users already have set from other tools.
func ResolveAuthToken(token, tokenFile string) (string, error) {
	if token != "" && tokenFile != "" {
		return "", fmt.Errorf("--authtoken and --authtoken-file are mutually exclusive")
	}
	switch {
	case tokenFile != "":
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("reading --authtoken-file: %w", err)
		}
		return strings.TrimSuffix(string(b), "\n"), nil
	case token != "":
		return token, nil
	default:
		return os.Getenv("NGROK_AUTHTOKEN"), nil
	}
}

// OptFor returns the tunnel opt-in declared on a spec's ingress, or nil when it
// asks for none.
func OptFor(spec api.DeploySpec) *api.IngressTunnelOpt {
	if spec.Ingress == nil || spec.Ingress.Tunnel == nil || !spec.Ingress.Tunnel.Enabled {
		return nil
	}
	return spec.Ingress.Tunnel
}

// Publish hosts the declared ingress tunnel and prints its URL. Exactly one of
// project or deployment scopes it. The returned stop func tears the tunnel down
// and is never nil.
//
// Failures are reported and swallowed: a workload that deployed successfully must
// not be reported as failed because its optional public URL could not be
// obtained.
func Publish(ctx context.Context, d *cliout.Driver, cl *client.Client, defaults *clientconfig.Tunnel, opt *api.IngressTunnelOpt, project, deployment string) func() {
	noop := func() {}
	if opt == nil || cl == nil {
		return noop
	}

	tokenFile := ""
	if defaults != nil {
		tokenFile = defaults.AuthTokenFile
	}
	authToken, err := ResolveAuthToken("", tokenFile)
	if err != nil {
		d.Error("ingress tunnel: %v", err)
		return noop
	}

	hostMode := opt.HostMode
	if hostMode == "" && defaults != nil {
		hostMode = defaults.IngressHostMode
	}

	st, err := cl.IngressTunnelStart(ctx, api.IngressTunnelRequest{
		AuthToken:  authToken,
		Project:    project,
		Deployment: deployment,
		HostMode:   hostMode,
		Host:       opt.Host,
	})
	if err != nil {
		d.Error("ingress tunnel: %v", err)
		return noop
	}

	d.Info("ingress published at %s", st.URL)
	for _, h := range st.Hosts {
		d.Info("  serving %s", h)
	}
	if st.HostMode == api.HostModeRewrite {
		d.Info("  Host is rewritten to the ingress hostname; absolute redirects and Domain= cookies will point there")
	}

	return func() {
		// A fresh context: the session's is already cancelled by the time teardown runs.
		if err := cl.IngressTunnelStop(context.Background(), project, deployment); err != nil {
			d.Warn("stopping ingress tunnel: %v", err)
		}
	}
}
