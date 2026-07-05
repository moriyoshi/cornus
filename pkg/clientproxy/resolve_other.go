//go:build !linux && !darwin && !windows

package clientproxy

// platformProxy has no system-settings source on other platforms; the environment
// stays authoritative.
func platformProxy() (*ProxyConfig, error) { return &ProxyConfig{}, nil }
