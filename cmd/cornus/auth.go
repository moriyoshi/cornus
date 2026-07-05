package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/sshclient"
)

// AuthCmd manages SSH public-key authentication and its short-lived sessions.
type AuthCmd struct {
	EnrollmentCode AuthEnrollmentCodeCmd `kong:"cmd,name='enrollment-code',help='Print the current enrollment code from the local server data directory.'"`
	Enroll         AuthEnrollCmd         `kong:"cmd,help='Enroll an SSH public key with a server.'"`
	Token          AuthTokenCmd          `kong:"cmd,help='Mint and print a short-lived session using an enrolled SSH key.'"`
	Keys           AuthKeysCmd           `kong:"cmd,help='List authorized SSH public keys.'"`
	DeleteKey      AuthDeleteKeyCmd      `kong:"cmd,name='delete-key',help='Remove a runtime-enrolled SSH public key.'"`
}

type AuthEnrollmentCodeCmd struct{}

func (c *AuthEnrollmentCodeCmd) Run(cli *CLI) error {
	path := filepath.Join(cli.resolveConfig().DataDir, "auth", "enrollment.secret")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read enrollment code %s: %w", path, err)
	}
	cli.out().Item("%s", strings.TrimSpace(string(data)))
	return nil
}

type AuthSignerFlags struct {
	IdentityFile   string `kong:"name='identity-file',type='path',help='SSH private-key file to use.'"`
	KeyFingerprint string `kong:"name='key-fingerprint',help='SHA256 fingerprint of a key in SSH_AUTH_SOCK.'"`
}

type AuthEnrollCmd struct {
	AuthSignerFlags
	Server string `kong:"help='Cornus server base URL; defaults to the selected context.',env='CORNUS_SERVER'"`
	Code   string `kong:"required,help='Current one-time enrollment code.'"`
	Name   string `kong:"help='Human-readable key name; empty uses its SHA256 fingerprint as the subject.'"`
}

func (c *AuthEnrollCmd) Run(cli *CLI) error {
	signer, cleanup, err := loadAuthSigner(cli, c.IdentityFile, c.KeyFingerprint)
	if err != nil {
		return err
	}
	defer cleanup()
	cn, err := requireAuthTransport(cli.resolver(), c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	ctx, cancel := context.WithTimeout(cli.rootContext(), 30*time.Second)
	defer cancel()
	info, err := cn.Client().SSHEnroll(ctx, signer, c.Code, c.Name)
	if err != nil {
		return err
	}
	cli.out().Done("SSH key %s enrolled as %q", info.Fingerprint, info.Subject)
	return nil
}

type AuthTokenCmd struct {
	AuthSignerFlags
	Server string        `kong:"help='Cornus server base URL; defaults to the selected context.',env='CORNUS_SERVER'"`
	Scope  string        `kong:"default='api',help='Requested credential scope.'"`
	TTL    time.Duration `kong:"default='1h',help='Requested lifetime (maximum 24h).'"`
}

func (c *AuthTokenCmd) Run(cli *CLI) error {
	signer, cleanup, err := loadAuthSigner(cli, c.IdentityFile, c.KeyFingerprint)
	if err != nil {
		return err
	}
	defer cleanup()
	cn, err := requireAuthTransport(cli.resolver(), c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	ctx, cancel := context.WithTimeout(cli.rootContext(), 30*time.Second)
	defer cancel()
	token, _, err := cn.Client().SSHKeyToken(ctx, signer, c.Scope, c.TTL.String())
	if err != nil {
		return err
	}
	cli.out().Item("%s", token)
	return nil
}

type AuthKeysCmd struct {
	Server string `kong:"help='Cornus server base URL; defaults to the selected context.',env='CORNUS_SERVER'"`
}

func (c *AuthKeysCmd) Run(cli *CLI) error {
	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	ctx, cancel := context.WithTimeout(cli.rootContext(), 30*time.Second)
	defer cancel()
	keys, err := cn.Client().SSHKeys(ctx)
	if err != nil {
		return err
	}
	table := cli.out().Table("FINGERPRINT", "NAME", "SUBJECT", "ENROLLED")
	for _, key := range keys {
		enrolled := ""
		if !key.Enrolled.IsZero() {
			enrolled = key.Enrolled.Format(time.RFC3339)
		}
		table.Row(key.Fingerprint, key.Name, key.Subject, enrolled)
	}
	return table.Flush()
}

type AuthDeleteKeyCmd struct {
	Fingerprint string `kong:"arg,required,help='SHA256 fingerprint to remove.'"`
	Server      string `kong:"help='Cornus server base URL; defaults to the selected context.',env='CORNUS_SERVER'"`
}

func (c *AuthDeleteKeyCmd) Run(cli *CLI) error {
	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	ctx, cancel := context.WithTimeout(cli.rootContext(), 30*time.Second)
	defer cancel()
	if err := cn.Client().DeleteSSHKey(ctx, c.Fingerprint); err != nil {
		return err
	}
	cli.out().Done("SSH key %s deleted", c.Fingerprint)
	return nil
}

func requireAuthTransport(resolver *clientconn.Resolver, server string) (*clientconn.Conn, error) {
	cn, err := resolver.ResolveTransport(server)
	if err != nil {
		return nil, err
	}
	if cn.Endpoint == "" {
		cn.Cleanup()
		return nil, fmt.Errorf("no server configured: pass --server or select a context with a server")
	}
	return cn, nil
}

func loadAuthSigner(cli *CLI, identityFile, fingerprint string) (ssh.Signer, func(), error) {
	if identityFile == "" && fingerprint == "" {
		file, err := cli.loadConfig()
		if err != nil {
			return nil, func() {}, err
		}
		_, selected, err := file.Resolve(cli.Context)
		if err != nil {
			return nil, func() {}, err
		}
		if selected != nil && selected.KeyAuth != nil {
			identityFile = selected.KeyAuth.IdentityFile
			fingerprint = selected.KeyAuth.KeyFingerprint
		}
	}
	return sshclient.LoadSigner(identityFile, fingerprint, sshclient.NewInteractivePrompt())
}
