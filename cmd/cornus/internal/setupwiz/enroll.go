package setupwiz

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/clientconfig"
	"cornus/pkg/sshclient"
)

// EnrollSSHKey enrolls the key named by a just-saved profile through that
// profile's resolved TLS and forwarding transport.
func EnrollSSHKey(ctx context.Context, configPath, contextName, code string) error {
	resolver := &clientconn.Resolver{ConfigFile: configPath, Context: contextName}
	file, err := resolver.LoadConfig()
	if err != nil {
		return err
	}
	_, profile, err := file.Resolve(contextName)
	if err != nil {
		return err
	}
	if profile == nil || profile.KeyAuth == nil {
		return fmt.Errorf("context %q has no key-auth block", contextName)
	}
	signer, closeSigner, err := sshclient.LoadSigner(profile.KeyAuth.IdentityFile, profile.KeyAuth.KeyFingerprint, sshclient.NewInteractivePrompt())
	if err != nil {
		return err
	}
	defer closeSigner()
	// Persist the public fingerprint even for an identity-file profile. The
	// background client agent can then address the foreground-minted session cache
	// without reading or unlocking the private key.
	profile.KeyAuth.KeyFingerprint = ssh.FingerprintSHA256(signer.PublicKey())
	if err := clientconfig.Save(configPath, file); err != nil {
		return fmt.Errorf("save SSH key fingerprint: %w", err)
	}
	conn, err := resolver.ResolveTransport("")
	if err != nil {
		return err
	}
	defer conn.Cleanup()
	if conn.Endpoint == "" {
		return fmt.Errorf("context %q has no server endpoint", contextName)
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = conn.Client().SSHEnroll(callCtx, signer, code, profile.KeyAuth.Name)
	return err
}

// SSHEnrollmentCode retrieves the current enrollment code from an SSH server
// host without sending it through the Cornus HTTP API.
func SSHEnrollmentCode(ctx context.Context, a *Answers) (string, error) {
	identityFiles := []string(nil)
	if a.SSHIdentityFile != "" {
		identityFiles = []string{a.SSHIdentityFile}
	}
	opts, err := sshclient.Resolve(a.SSHHost, sshclient.Options{
		User:             a.SSHUser,
		IdentityFiles:    identityFiles,
		PromptPassphrase: sshclient.NewInteractivePrompt(),
	}, true)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := sshclient.Output(callCtx, a.SSHHost, opts, opts.ProxyCommand != "", "cornus auth enrollment-code")
	if err != nil {
		return "", err
	}
	code := strings.TrimSpace(string(out))
	if code == "" {
		return "", fmt.Errorf("remote enrollment-code command returned empty output")
	}
	return code, nil
}
