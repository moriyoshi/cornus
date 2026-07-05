package cliout

import "strings"

// stripANSI removes every CSI escape sequence from s. Used by tests to assert
// that fancy output carries the same textual content as plain output. (The
// fancy renderer's colors come from lipgloss; this is the inverse used only to
// compare rendered text against plain.)
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until the final byte of the CSI sequence (0x40-0x7e).
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume the final byte
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
