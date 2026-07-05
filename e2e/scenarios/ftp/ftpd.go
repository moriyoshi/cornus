// Command ftpd is a minimal FTP server for the ftp*.star E2E scenarios, built on
// github.com/fclairamb/ftpserverlib (a Go FTP server, so no Python runtime). It
// pins the passive-data port to a single fixed value so the scenario can publish it
// as a stable host port, and advertises FTP_PASV_ADDRESS as the PASV masquerade
// host. The special value FTP_PASV_ADDRESS=auto leaves ftpserverlib's PublicHost
// empty, so it advertises the passive address it detects from each control
// connection's local IP — the correct behaviour on a user-network where the server
// has no fixed public IP (ftp-usernet.star). Active mode is left enabled, so the
// same image serves the passive, active-mode, and user-network E2E scenarios.
// Files live in an in-memory filesystem (afero MemMapFs).
package main

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"os"
	"strconv"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
)

type driver struct {
	fs       afero.Fs
	user     string
	password string
	settings *ftpserver.Settings
}

func (d *driver) GetSettings() (*ftpserver.Settings, error) { return d.settings, nil }

func (d *driver) ClientConnected(ftpserver.ClientContext) (string, error) {
	return "cornus-ftp", nil
}

func (d *driver) ClientDisconnected(ftpserver.ClientContext) {}

func (d *driver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user == d.user && pass == d.password {
		return d.fs, nil
	}
	return nil, errors.New("ftpd: invalid credentials")
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	return nil, errors.New("ftpd: TLS not configured")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	pasvPort, err := strconv.Atoi(envOr("FTP_PASV_PORT", "30000"))
	if err != nil {
		panic("FTP_PASV_PORT: " + err.Error())
	}
	// FTP_PASV_ADDRESS=auto -> empty PublicHost, so ftpserverlib advertises the
	// passive host it derives from each connection's local IP (see package doc).
	pasvAddr := envOr("FTP_PASV_ADDRESS", "127.0.0.1")
	if pasvAddr == "auto" {
		pasvAddr = ""
	}
	d := &driver{
		fs:       afero.NewMemMapFs(),
		user:     envOr("FTP_USER", "cornus"),
		password: envOr("FTP_PASSWORD", "secret"),
		settings: &ftpserver.Settings{
			// FTP_LISTEN and FTP_PASV_PORT let the E2E scenarios run the server on a
			// non-privileged port when needed; the defaults match the ftp.star ports.
			ListenAddr:               envOr("FTP_LISTEN", "0.0.0.0:21"),
			PublicHost:               pasvAddr,
			PassiveTransferPortRange: &ftpserver.PortRange{Start: pasvPort, End: pasvPort},
			// Use an ephemeral source port for active-mode data connections instead
			// of the privileged port 20, so active mode (ftp-active.star) works
			// without root / CAP_NET_BIND_SERVICE.
			ActiveTransferPortNon20: true,
		},
	}
	server := ftpserver.NewFtpServer(d)
	server.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}
}
