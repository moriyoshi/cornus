package setupwiz

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
)

// VerifyResult is the outcome of a post-save connection check. It never carries a
// fatal error: the wizard reports it and moves on (the profile stays saved).
type VerifyResult struct {
	// OK reports that the server was reached (including a legacy server that 404s
	// the /info endpoint — the full transport still round-tripped).
	OK bool
	// Class is a short machine-ish tag: ok, ok-legacy, auth, refused, tls, timeout,
	// dns, http, ssh, kube, resolve, or error.
	Class string
	// Detail is a human-readable one-liner.
	Detail string
	// Hints are optional remediation lines.
	Hints []string
}

// VerifyConnection resolves the given context exactly as a real command would
// (clientconn.Resolver, including any port-forward the profile names) and calls
// GET /.cornus/v1/info through the full transport, classifying the result. It
// never returns an error: a failure is reported as a non-OK VerifyResult.
func VerifyConnection(ctx context.Context, configPath, contextName string) VerifyResult {
	r := &clientconn.Resolver{ConfigFile: configPath, Context: contextName}
	cn, err := r.Resolve("")
	if err != nil {
		return classifyResolveError(err)
	}
	defer cn.Cleanup()
	if cn.Endpoint == "" {
		return VerifyResult{
			Class:  "unresolved",
			Detail: "no reachable endpoint could be resolved for this context",
			Hints:  []string{"an SSH-tunnel or port-forward profile needs its transport available to verify; the profile is saved and can be used once the server is reachable"},
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, ierr := cn.Client().Info(cctx)
	return classifyInfoError(ierr)
}

// statusCode extracts the leading HTTP status code from an apiError message,
// which formats as "<code> <reason>: <body>" (e.g. "404 Not Found: ..."). It
// returns 0 for a transport error, whose message does not begin with a code.
func statusCode(msg string) int {
	fields := strings.Fields(msg)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n < 100 || n > 599 {
		return 0
	}
	return n
}

// classifyInfoError turns the Info call's error into a VerifyResult.
func classifyInfoError(err error) VerifyResult {
	if err == nil {
		return VerifyResult{OK: true, Class: "ok", Detail: "connected — the server answered /info"}
	}
	msg := err.Error()
	if code := statusCode(msg); code != 0 {
		switch {
		case code == 404:
			return VerifyResult{OK: true, Class: "ok-legacy", Detail: "connected — the server is reachable (it predates /info)"}
		case code == 401 || code == 403:
			return VerifyResult{Class: "auth", Detail: msg, Hints: []string{
				"the server requires a valid bearer token — add one with --token or a kube-auth block",
			}}
		default:
			return VerifyResult{Class: "http", Detail: msg}
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return VerifyResult{Class: "timeout", Detail: "the connection timed out", Hints: []string{
			"check the address is reachable and any tunnel/port-forward is up",
		}}
	case isRefused(err):
		return VerifyResult{Class: "refused", Detail: "connection refused", Hints: []string{
			"is the server running and listening on that address?",
		}}
	case isTLS(msg):
		return VerifyResult{Class: "tls", Detail: msg, Hints: []string{
			"supply the server CA with --tls-ca-cert, set --tls-server-name if the cert names a different host, or (testing only) --insecure-skip-verify",
		}}
	case isDNS(err):
		return VerifyResult{Class: "dns", Detail: msg, Hints: []string{"the server host could not be resolved — check the URL"}}
	default:
		return VerifyResult{Class: "error", Detail: msg}
	}
}

// classifyResolveError classifies a failure to even resolve the connection (an
// SSH handshake or a kube/port-forward setup error) before any HTTP call.
func classifyResolveError(err error) VerifyResult {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "ssh"):
		return VerifyResult{Class: "ssh", Detail: msg, Hints: []string{
			"check the SSH destination, user, and key (ssh to it manually to confirm)",
		}}
	case strings.Contains(lower, "kube") || strings.Contains(lower, "svcforward") || strings.Contains(lower, "port-forward"):
		return VerifyResult{Class: "kube", Detail: msg, Hints: []string{
			"check the kube context/namespace and that the cornus Service exists",
		}}
	default:
		return VerifyResult{Class: "resolve", Detail: msg}
	}
}

func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused")
}

func isTLS(msg string) bool {
	return strings.Contains(msg, "x509") ||
		strings.Contains(msg, "certificate") ||
		strings.Contains(msg, "tls:")
}

func isDNS(err error) bool {
	var d *net.DNSError
	return errors.As(err, &d) || strings.Contains(err.Error(), "no such host")
}
