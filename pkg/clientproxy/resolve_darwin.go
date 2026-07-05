//go:build darwin

package clientproxy

import (
	"os/exec"
)

// scutilProxy runs `scutil --proxy`; a field so tests can stub the probe.
var scutilProxy = func() (string, error) {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// platformProxy reads the macOS system proxy configuration via scutil (only when
// the environment named no proxy — Resolve gates that). Parsing lives in the pure
// parseScutilProxy so it is unit-tested off-macOS.
func platformProxy() (*ProxyConfig, error) {
	out, err := scutilProxy()
	if err != nil {
		// No scutil / not reachable: fall back to the empty config, environment stays
		// authoritative. This is not a hard error on a Mac without configured proxies.
		return &ProxyConfig{}, nil
	}
	return parseScutilProxy(out), nil
}
