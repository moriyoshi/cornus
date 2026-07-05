package ingressemu

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// LoadMkcertCA loads mkcert's locally-trusted root CA (rootCA.pem + rootCA-key.pem
// from its CAROOT), so the emulated ingress mints leaf certs the OS and browser trust
// stores already trust after `mkcert -install` — TLS then works with no manual CA
// trust and no --cacert. It returns an error when mkcert's CA is not present, so a
// caller can fall back to a self-signed CA.
func LoadMkcertCA() (*CA, error) {
	root := MkcertCAROOT()
	if root == "" {
		return nil, fmt.Errorf("ingressemu: mkcert CAROOT not found (is mkcert installed and `mkcert -install` run?)")
	}
	return LoadCA(filepath.Join(root, "rootCA.pem"), filepath.Join(root, "rootCA-key.pem"))
}

// MkcertCAROOT resolves mkcert's CA directory the way mkcert itself does — the CAROOT
// env var, else the per-OS default data dir — and only returns it when a rootCA.pem
// actually lives there. As a last resort it asks the mkcert binary (which handles any
// non-standard install). Empty means mkcert is not set up.
func MkcertCAROOT() string {
	if v := strings.TrimSpace(os.Getenv("CAROOT")); v != "" {
		return v
	}
	if d := mkcertDefaultCAROOT(); d != "" {
		if _, err := os.Stat(filepath.Join(d, "rootCA.pem")); err == nil {
			return d
		}
	}
	// Authoritative fallback: the binary knows a custom CAROOT we could not guess.
	if out, err := exec.Command("mkcert", "-CAROOT").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			if _, err := os.Stat(filepath.Join(p, "rootCA.pem")); err == nil {
				return p
			}
		}
	}
	return ""
}

// mkcertDefaultCAROOT returns mkcert's default CA directory for this OS (matching
// mkcert's getCAROOT): the user data dir on Linux/BSD, Application Support on macOS,
// and LocalAppData on Windows, each with an "mkcert" subdir. Empty when it cannot be
// resolved.
func mkcertDefaultCAROOT() string {
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "mkcert")
		}
	case "windows":
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "mkcert")
		}
	default: // linux and the BSDs
		if d := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); d != "" {
			return filepath.Join(d, "mkcert")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "mkcert")
		}
	}
	return ""
}
