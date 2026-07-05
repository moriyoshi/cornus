package sshclient

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// maxJumpDepth bounds ProxyJump recursion so a self-referential ssh_config cannot
// loop forever.
const maxJumpDepth = 10

// configFinder, when non-nil, overrides where ssh_config is read from (tests point
// it at a fixture file). Production reads the default ~/.ssh/config and
// /etc/ssh/ssh_config.
var configFinder func() string

func userSettings() *ssh_config.UserSettings {
	us := &ssh_config.UserSettings{}
	if configFinder != nil {
		us.ConfigFinder(configFinder)
	}
	return us
}

// Resolve builds the effective SSH dial Options for a destination by layering the
// explicit profile fields (prof) over the host's ssh_config (unless useConfig is
// false). Explicit profile fields always win; ssh_config fills the gaps. The
// destination is an ssh_config Host alias or a literal host[:port].
func Resolve(destination string, prof Options, useConfig bool) (Options, error) {
	out := prof
	if !useConfig {
		host, port := splitHostPort(destination, "22")
		out.Addr = net.JoinHostPort(host, port)
		out.ProxyJump = nil
		out.ProxyCommand = ""
		if out.KnownHosts == "" && !out.Insecure {
			out.KnownHosts = defaultKnownHosts()
		}
		return out, nil
	}

	us := userSettings()
	rh, err := resolveHost(us, destination, "", "", 0)
	if err != nil {
		return Options{}, err
	}
	out.Addr = rh.Addr
	if out.User == "" {
		out.User = rh.User
	}
	if len(out.IdentityFiles) == 0 {
		out.IdentityFiles = rh.IdentityFiles
	}
	if out.KnownHosts == "" {
		out.KnownHosts = rh.KnownHosts
	}
	if !out.Insecure {
		out.Insecure = rh.Insecure
	}
	out.ProxyJump = rh.ProxyJump
	out.ProxyCommand = rh.ProxyCommand
	if out.KnownHosts == "" && !out.Insecure {
		out.KnownHosts = defaultKnownHosts()
	}
	return out, nil
}

// resolvedHost is one host fully resolved from ssh_config.
type resolvedHost struct {
	Addr          string
	User          string
	IdentityFiles []string
	KnownHosts    string
	Insecure      bool
	ProxyJump     []Hop
	ProxyCommand  string
}

// resolveHost resolves one host alias through ssh_config. userOverride/portOverride
// come from a ProxyJump entry (`user@host:port`) and win over config values.
func resolveHost(us *ssh_config.UserSettings, alias, userOverride, portOverride string, depth int) (resolvedHost, error) {
	if depth > maxJumpDepth {
		return resolvedHost{}, fmt.Errorf("ssh: ProxyJump chain too deep (possible loop) at %q", alias)
	}
	hostAlias, litPort := splitHostPort(alias, "")

	get := func(key string) (string, error) { return us.GetStrict(hostAlias, key) }

	hostName, err := get("HostName")
	if err != nil {
		return resolvedHost{}, fmt.Errorf("ssh: read ssh_config: %w", err)
	}
	if hostName == "" {
		hostName = hostAlias
	}
	port := firstNonEmpty(portOverride, litPort)
	if port == "" {
		if p, _ := get("Port"); p != "" {
			port = p
		} else {
			port = "22"
		}
	}
	user := userOverride
	if user == "" {
		user, _ = get("User")
	}

	tokHost, tokPort, tokUser := hostName, port, user

	idFiles, _ := us.GetAllStrict(hostAlias, "IdentityFile")
	identity := existingIdentityFiles(idFiles, tokHost, tokPort, tokUser)

	knownHosts := ""
	if khs, _ := us.GetAllStrict(hostAlias, "UserKnownHostsFile"); len(khs) > 0 {
		// UserKnownHostsFile may be a space-separated list; take the first existing.
		knownHosts = firstExistingKnownHosts(khs, tokHost, tokPort, tokUser)
	}

	insecure := false
	if v, _ := get("StrictHostKeyChecking"); strings.EqualFold(v, "no") {
		insecure = true
	}

	rh := resolvedHost{
		Addr:          net.JoinHostPort(hostName, port),
		User:          user,
		IdentityFiles: identity,
		KnownHosts:    knownHosts,
		Insecure:      insecure,
	}

	if pc, _ := get("ProxyCommand"); pc != "" && !strings.EqualFold(pc, "none") {
		rh.ProxyCommand = expandTokens(pc, tokHost, tokPort, tokUser)
	}
	if pj, _ := get("ProxyJump"); pj != "" && !strings.EqualFold(pj, "none") {
		hops, err := resolveProxyJump(us, pj, depth+1)
		if err != nil {
			return resolvedHost{}, err
		}
		rh.ProxyJump = hops
	}
	return rh, nil
}

// resolveProxyJump expands a ProxyJump spec (comma-separated `[user@]host[:port]`)
// into a flat, outermost-first hop chain, recursively expanding any nested
// ProxyJump on the jump hosts themselves.
func resolveProxyJump(us *ssh_config.UserSettings, spec string, depth int) ([]Hop, error) {
	var hops []Hop
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		user, host := splitUserHost(entry)
		rh, err := resolveHost(us, host, user, "", depth)
		if err != nil {
			return nil, err
		}
		// A jump host's own ProxyJump is reached first (it is further out).
		hops = append(hops, rh.ProxyJump...)
		hops = append(hops, Hop{
			Addr:          rh.Addr,
			User:          rh.User,
			IdentityFiles: rh.IdentityFiles,
			KnownHosts:    rh.KnownHosts,
			Insecure:      rh.Insecure,
		})
	}
	return hops, nil
}

// existingIdentityFiles expands and filters identity files to those present on
// disk, so ssh_config's built-in default (which points at a file that usually does
// not exist) does not make auth fail.
func existingIdentityFiles(files []string, host, port, user string) []string {
	var out []string
	for _, f := range files {
		p := expandUser(expandTokens(f, host, port, user))
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func firstExistingKnownHosts(entries []string, host, port, user string) string {
	for _, entry := range entries {
		for _, f := range strings.Fields(entry) {
			p := expandUser(expandTokens(f, host, port, user))
			if p == "" {
				continue
			}
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// defaultKnownHosts returns ~/.ssh/known_hosts when it exists, so host-key
// verification uses the user's existing file without extra configuration.
func defaultKnownHosts() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// expandUser expands a leading ~ to the user's home directory.
func expandUser(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// splitHostPort splits host[:port], returning defPort when no port is present.
// IPv6 literals in brackets are handled.
func splitHostPort(s, defPort string) (host, port string) {
	if s == "" {
		return "", defPort
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	return s, defPort
}

// splitUserHost splits `[user@]host` from a ProxyJump entry.
func splitUserHost(s string) (user, host string) {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
