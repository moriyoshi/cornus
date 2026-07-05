//go:build windows

package clientproxy

import (
	"golang.org/x/sys/windows/registry"
)

// platformProxy reads the Windows WinINET proxy configuration from the current
// user's Internet Settings registry key (only when the environment named no proxy
// — Resolve gates that). Parsing lives in the pure parseWinInetProxy so it is
// unit-tested off-Windows.
func platformProxy() (*ProxyConfig, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return &ProxyConfig{}, nil
	}
	defer key.Close()

	enable, _, _ := key.GetIntegerValue("ProxyEnable")
	server, _, _ := key.GetStringValue("ProxyServer")
	override, _, _ := key.GetStringValue("ProxyOverride")
	return parseWinInetProxy(server, override, enable == 1), nil
}
