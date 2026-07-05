package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"cornus/pkg/authtoken"
	"cornus/pkg/build/builder"
	"cornus/pkg/deploy"
	"cornus/pkg/imageref"
)

const (
	installationSecretEnv  = "CORNUS_INSTALLATION_SECRET"
	installationSecretFile = "installation.key"
	internalIssuer         = "cornus:internal"
	internalAudience       = "cornus:internal"
	sshKeyIssuer           = "cornus:ssh-key"
	sshKeyAudience         = "cornus:ssh-key"
	installationSecretSize = 32
	internalUsername       = "cornus-internal"
	internalBuildTTL       = 15 * time.Minute
	internalPullTTL        = 15 * time.Minute
	internalKubernetesTTL  = 12 * time.Hour
)

type installationSecretProvenance int

const (
	installationSecretUnknown installationSecretProvenance = iota
	installationSecretFromFile
	installationSecretFromEnv
)

// loadInstallationSecret returns the server's installation-wide signing secret.
// An environment value wins so replicas can share one key; otherwise a durable
// 0600 key is read or generated under DataDir.
func loadInstallationSecret(dataDir string) ([]byte, installationSecretProvenance, error) {
	if secret := os.Getenv(installationSecretEnv); secret != "" {
		// Hold the environment to the same minimum readInstallationSecret enforces
		// on the file. This key signs the internal registry-bypass credential and
		// every SSH-key session, so a short operator-supplied value silently
		// weakens both while still looking configured — the worst failure shape.
		if len(secret) < installationSecretSize {
			return nil, installationSecretUnknown, fmt.Errorf("%s is %d bytes; want at least %d", installationSecretEnv, len(secret), installationSecretSize)
		}
		return []byte(secret), installationSecretFromEnv, nil
	}
	if dataDir == "" {
		return nil, installationSecretUnknown, fmt.Errorf("%s is unset and DataDir is empty", installationSecretEnv)
	}
	path := filepath.Join(dataDir, installationSecretFile)
	secret, err := readInstallationSecret(path)
	if err == nil {
		return secret, installationSecretFromFile, nil
	}
	if !os.IsNotExist(err) {
		return nil, installationSecretUnknown, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, installationSecretUnknown, fmt.Errorf("create installation secret directory %s: %w", dataDir, err)
	}
	secret = make([]byte, installationSecretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, installationSecretUnknown, fmt.Errorf("generate installation secret: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".installation.key-*")
	if err != nil {
		return nil, installationSecretUnknown, fmt.Errorf("create installation secret temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, installationSecretUnknown, fmt.Errorf("chmod installation secret temporary file: %w", err)
	}
	if _, err := tmp.Write(secret); err != nil {
		tmp.Close()
		return nil, installationSecretUnknown, fmt.Errorf("write installation secret temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, installationSecretUnknown, fmt.Errorf("close installation secret temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, installationSecretUnknown, fmt.Errorf("install installation secret %s: %w", path, err)
	}
	// Rename can replace an existing file and os.WriteFile-style mode arguments
	// do not tighten an existing inode. Enforce the final contract explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, installationSecretUnknown, fmt.Errorf("chmod installation secret %s: %w", path, err)
	}
	return secret, installationSecretFromFile, nil
}

func readInstallationSecret(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("installation secret %s is not a regular file", path)
	}
	// Windows' os.FileMode does not model owner/group ACLs. On Unix, where these
	// bits are meaningful, a group/world-readable installation key is fatal.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("installation secret %s has insecure permissions %04o; want 0600 or stricter", path, info.Mode().Perm())
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read installation secret %s: %w", path, err)
	}
	if len(secret) < installationSecretSize {
		return nil, fmt.Errorf("installation secret %s is %d bytes; want at least %d", path, len(secret), installationSecretSize)
	}
	return secret, nil
}

// ownRegistryHosts returns every spelling that can name this server's own OCI
// registry from one of its supported vantages. Callers must consult this set
// before emitting an internal credential.
func (s *Server) ownRegistryHosts(ctx context.Context) map[string]struct{} {
	hosts := make(map[string]struct{}, 3)
	if loop := s.loopbackRegistry(); loop != "" {
		hosts[loop] = struct{}{}
	}
	if advertised := s.advertisedRegistry(ctx).RegistryHost; advertised != "" {
		hosts[advertised] = struct{}{}
	}
	if _, port, err := net.SplitHostPort(s.cfg.HTTPAddr); err == nil && port != "" {
		hosts["localhost:"+port] = struct{}{}
	}
	return hosts
}

func (s *Server) mintInternal(scope string, ttl time.Duration) (string, error) {
	if s.auth == nil || len(s.auth.internalSecret) == 0 {
		return "", nil
	}
	return authtoken.Issue(authtoken.IssueOptions{
		Subject:     internalIssuer,
		Scope:       scope,
		Issuer:      internalIssuer,
		Audience:    internalAudience,
		TTL:         ttl,
		HS256Secret: s.auth.internalSecret,
	})
}

// internalRegistryAuth mints one short-lived push credential and exposes it
// only under hostnames known to address this server's own registry.
func (s *Server) internalRegistryAuth(ctx context.Context) (map[string]builder.RegistryCredential, error) {
	if s.auth == nil || s.auth.internal == nil {
		return nil, nil
	}
	token, err := s.mintInternal(authtoken.ScopeRegistryPush, internalBuildTTL)
	if err != nil {
		return nil, err
	}
	hosts := s.ownRegistryHosts(ctx)
	creds := make(map[string]builder.RegistryCredential, len(hosts))
	for host := range hosts {
		creds[host] = builder.RegistryCredential{Username: internalUsername, Password: token}
	}
	return creds, nil
}

// internalPullCredentials mints a fresh read-only credential only when ref's
// registry host is one of this server's own. External refs receive nothing.
func (s *Server) internalPullCredentials(ctx context.Context, ref string) (deploy.RegistryCredential, bool, error) {
	return s.internalPullCredentialsWithTTL(ctx, ref, internalPullTTL)
}

// internalKubernetesPullCredentials uses a longer lifetime because the kubelet
// pulls independently of this process. A supervised loop refreshes the Secret
// every four hours, leaving a generous outage window without broadening scope.
func (s *Server) internalKubernetesPullCredentials(ctx context.Context, ref string) (deploy.RegistryCredential, bool, error) {
	return s.internalPullCredentialsWithTTL(ctx, ref, internalKubernetesTTL)
}

// CONSTRAINT ON THE TOKEN FORMAT, recorded here because this is where the
// credential is minted and nothing downstream can enforce it.
//
// The password below is carried to some backends inside a URL userinfo
// component. incushost builds `scheme://user:pass@registry` with
// url.UserPassword().String() and hands it to incusd, which base64s the
// PERCENT-ENCODED form into skopeo's authfile with nothing undoing the escapes
// (pkg/deploy/incushost/image_linux.go). A password containing any character Go
// escapes there would therefore arrive at skopeo still escaped, and the pull
// would fail with a credential that looks correct everywhere it is printed.
//
// It works today only because a compact JWS is base64url plus "." separators —
// every character of which is UNRESERVED in a userinfo component (ALPHA / DIGIT
// / "-" / "." / "_" / "~"), so url.UserPassword escapes nothing. That is a
// property of the token FORMAT, not a guarantee any of these call sites make: a
// switch to standard base64 (which uses "+", "/", "=") or to any opaque secret
// would break incus pulls and nothing else, silently, on a path with no unit
// coverage of its own.
//
// Keep minted registry credentials inside the userinfo-unreserved set.
func (s *Server) internalPullCredentialsWithTTL(ctx context.Context, ref string, ttl time.Duration) (deploy.RegistryCredential, bool, error) {
	if s.auth == nil || s.auth.internal == nil {
		return deploy.RegistryCredential{}, false, nil
	}
	host, _ := imageref.SplitHostRepo(ref)
	if host == "" {
		return deploy.RegistryCredential{}, false, nil
	}
	if _, ok := s.ownRegistryHosts(ctx)[host]; !ok {
		return deploy.RegistryCredential{}, false, nil
	}
	token, err := s.mintInternal(authtoken.ScopeRegistryPull, ttl)
	if err != nil {
		return deploy.RegistryCredential{}, false, err
	}
	return deploy.RegistryCredential{Username: internalUsername, Password: token}, true, nil
}
