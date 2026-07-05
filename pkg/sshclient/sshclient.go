// Package sshclient establishes an SSH connection through which the cornus CLI
// reaches a cornus server running on a remote docker/containerd host, and hands
// out net.Conns to that server over the SSH connection. It is the
// docker/containerd-host analogue of pkg/svcforward (the in-cluster Kubernetes
// port-forward): instead of binding a local listener, it injects a *ssh.Client's
// direct-tcpip DialContext straight into the cornus HTTP/WebSocket transport, so
// no local port is ever bound.
//
// The Dialer is reconnecting: an SSH egress tunnel can drop at any time, so it
// re-establishes the connection on demand and requests issued after a drop
// transparently succeed. In-flight streams cannot be resumed (that is a
// fundamental tunnel limit); reconnection helps the next request.
//
// Two transports share one interface (a DialContext + Close): the pure-Go Dialer
// here, and the ssh-binary fallback in binary.go for configs that need a
// ProxyCommand or Match block the pure-Go path cannot honor. Both are selected in
// cmd/cornus/internal/clientconn.
package sshclient

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyCallback builds the SSH host-key verifier, failing closed: with no
// known-hosts file, no pinned key, and no explicit insecure opt-in, it errors
// rather than trusting any host key. It is shared with pkg/tunnel/ssh, which wants
// identical semantics.
func HostKeyCallback(knownHostsFile, hostKey string, insecure bool) (ssh.HostKeyCallback, error) {
	switch {
	case insecure:
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicit dev opt-in
	case hostKey != "":
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
		if err != nil {
			return nil, fmt.Errorf("ssh: host key is not a valid authorized_keys line: %w", err)
		}
		return ssh.FixedHostKey(pk), nil
	case knownHostsFile != "":
		cb, err := knownhosts.New(knownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("ssh: known-hosts %s: %w", knownHostsFile, err)
		}
		return cb, nil
	default:
		return nil, errors.New("ssh: host-key verification not configured; set a known-hosts file or a pinned host key (or opt into insecure host keys for dev)")
	}
}

// errPassphraseUnavailable marks an encrypted identity file that could not be
// unlocked because no prompt was available (a reconnect). It is skipped rather than
// fatal when an ssh-agent might supply a working key instead.
var errPassphraseUnavailable = errors.New("ssh: identity file is passphrase-protected and no prompt is available")

// AuthMethods builds the SSH auth-method list for a client-side tunnel connection.
// It combines every public key — the identity files first (each read from disk),
// then the local ssh-agent's keys (when ag is non-nil) — into a SINGLE publickey
// method. This is deliberate: golang.org/x/crypto/ssh tries only the first
// publickey AuthMethod (all share the method name "publickey"), so separate
// methods would strand every key after the first; one callback offering all
// signers lets the server try each until one is accepted, mirroring OpenSSH's
// fall-through from the agent to an -i identity.
//
// Unlike pkg/tunnel/ssh.authMethods (an unattended server-side path that rejects
// encrypted keys), this can unlock a passphrase-protected key: when prompt is
// non-nil it is called for the passphrase; when prompt is nil an encrypted key is
// skipped if the agent can still supply a key, else it is a clear error (the caller
// passes nil on a reconnect so a background goroutine never blocks on a prompt).
// At least one of ag or a loadable identity file must yield a key.
//
// There is deliberately no password or inline-key option: nothing safe to persist
// for an unattended reconnect.
func AuthMethods(identityFiles []string, ag agent.Agent, prompt func(keyPath string) ([]byte, error)) ([]ssh.AuthMethod, error) {
	var idSigners []ssh.Signer
	for _, f := range identityFiles {
		if f == "" {
			continue
		}
		signer, err := loadIdentity(f, prompt)
		if err != nil {
			// An encrypted key we cannot unlock is skipped when the agent may still
			// have a usable key (the reconnect case); otherwise it is fatal.
			if ag != nil && errors.Is(err, errPassphraseUnavailable) {
				continue
			}
			return nil, err
		}
		idSigners = append(idSigners, signer)
	}
	if len(idSigners) == 0 && ag == nil {
		return nil, errors.New("ssh: no credential available (load a key into your ssh-agent, or set an identity file)")
	}
	// One publickey method offering the identity keys first, then the agent's keys.
	signersFn := func() ([]ssh.Signer, error) {
		signers := append([]ssh.Signer{}, idSigners...)
		if ag != nil {
			agentSigners, err := ag.Signers()
			if err == nil {
				signers = append(signers, agentSigners...)
			}
		}
		return signers, nil
	}
	return []ssh.AuthMethod{ssh.PublicKeysCallback(signersFn)}, nil
}

// loadIdentity reads and parses a private key from disk, prompting for a
// passphrase when the key is encrypted and prompt is non-nil.
func loadIdentity(path string, prompt func(keyPath string) ([]byte, error)) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: read identity file %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return signer, nil
	}
	var passErr *ssh.PassphraseMissingError
	if !errors.As(err, &passErr) {
		return nil, fmt.Errorf("ssh: parse identity file %s: %w", path, err)
	}
	// The key is passphrase-protected.
	if prompt == nil {
		return nil, fmt.Errorf("%w: %s", errPassphraseUnavailable, path)
	}
	pass, err := prompt(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: read passphrase for %s: %w", path, err)
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, pass)
	if err != nil {
		return nil, fmt.Errorf("ssh: decrypt identity file %s: %w", path, err)
	}
	return signer, nil
}

// expandTokens performs the subset of OpenSSH percent-token expansion cornus
// honors on ssh_config values: %h (host), %p (port), %r (remote user), %% (a
// literal percent). Unknown tokens are left as-is.
func expandTokens(s, host, port, user string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	r := strings.NewReplacer("%h", host, "%p", port, "%r", user, "%%", "%")
	return r.Replace(s)
}
