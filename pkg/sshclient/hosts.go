package sshclient

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// ConfigHost is one connectable Host alias declared in the user's ssh_config,
// together with the values that make it recognizable in a picker. HostName,
// User, and Port are the EFFECTIVE values (resolved the same way Resolve does),
// so an alias that inherits everything from a wildcard block still describes
// itself correctly.
type ConfigHost struct {
	Alias    string
	HostName string
	User     string
	Port     string
}

// Summary renders the alias's target as "user@hostname[:port]", omitting the
// user when the config does not set one and the port when it is the default 22.
// It is empty when the alias resolves to nothing but itself, which is the case
// worth showing no annotation for.
func (h ConfigHost) Summary() string {
	host := h.HostName
	if host == "" || host == h.Alias {
		host = ""
	}
	if host == "" && h.User == "" && (h.Port == "" || h.Port == "22") {
		return ""
	}
	if host == "" {
		host = h.Alias
	}
	if h.User != "" {
		host = h.User + "@" + host
	}
	if h.Port != "" && h.Port != "22" {
		host += ":" + h.Port
	}
	return host
}

// ConfigHosts returns the connectable Host aliases declared in the user's
// ssh_config (~/.ssh/config, or the fixture a test points configFinder at), in
// file order and de-duplicated. It reuses the same parser and the same file
// selection Resolve does, so a listed alias is exactly one Resolve can dial.
//
// Only literal aliases are returned: a pattern containing a wildcard ('*' or
// '?') or a negation ('!foo') configures a CLASS of hosts rather than naming
// one, so it is not a destination anybody can connect to. Include directives are
// not followed — the parser keeps them as nodes inside the enclosing block
// rather than merging their Host declarations — so an alias defined in an
// included file simply does not appear.
//
// Every failure (no config, unreadable, unparsable) yields nil rather than an
// error: the caller's response is the same in all of them — ask for the
// destination as free text.
func ConfigHosts() []ConfigHost {
	path := userConfigPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cfg, err := ssh_config.DecodeBytes(data)
	if err != nil {
		return nil
	}
	// Resolve the per-alias values against the very file the aliases were listed
	// from, so a summary can never describe a different config than the picker.
	us := &ssh_config.UserSettings{}
	us.ConfigFinder(func() string { return path })
	seen := map[string]bool{}
	var out []ConfigHost
	for _, host := range cfg.Hosts {
		for _, pat := range host.Patterns {
			alias := pat.String()
			if !connectableAlias(alias) || seen[alias] {
				continue
			}
			seen[alias] = true
			ch := ConfigHost{Alias: alias}
			// GetStrict errors are the same "cannot use this config" case the
			// decode above already survived; ignore them and show the bare alias.
			ch.HostName, _ = us.GetStrict(alias, "HostName")
			ch.User, _ = us.GetStrict(alias, "User")
			ch.Port, _ = us.GetStrict(alias, "Port")
			out = append(out, ch)
		}
	}
	return out
}

// connectableAlias reports whether an ssh_config Host pattern names a single
// host one can actually connect to.
func connectableAlias(alias string) bool {
	if alias == "" || strings.ContainsAny(alias, "*?!") {
		return false
	}
	return strings.IndexFunc(alias, func(r rune) bool { return r == ' ' || r == '\t' }) < 0
}

// userConfigPath is the ssh_config file the alias listing reads: the test
// override when one is installed (so ConfigHosts and Resolve always agree on
// which file is in effect), else ~/.ssh/config.
func userConfigPath() string {
	if configFinder != nil {
		return configFinder()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}
