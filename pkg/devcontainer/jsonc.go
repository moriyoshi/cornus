package devcontainer

import (
	"encoding/json"
	"fmt"
)

// stripJSONC blanks out the JSONC extras encoding/json cannot parse — `//` line
// comments, `/* */` block comments, and trailing commas before a closing `}` or
// `]` — so the result is plain JSON.
//
// It is length- and newline-preserving: every removed byte is overwritten with a
// space in place (never deleted), and newline bytes are always kept verbatim,
// including newlines *inside* a block comment. Consequently the returned slice
// has the exact same length as the input and identical newline offsets, so a
// byte offset in the stripped bytes (e.g. a json.SyntaxError.Offset) maps to the
// same offset — and the same line/column — in the original source. offsetToLine
// relies on this to report sound error positions.
//
// The scan is string-aware: comment markers and commas inside a JSON string
// literal (respecting `\` escapes) are left untouched, so a URL like "http://x"
// or a comma inside a value survives verbatim.
func stripJSONC(in []byte) []byte {
	out := make([]byte, len(in))
	copy(out, in)
	blank := func(i int) {
		if out[i] != '\n' && out[i] != '\r' {
			out[i] = ' '
		}
	}
	// lastComma is the offset of the most recent ',' that could still turn out to
	// be a trailing comma, or -1 when the last significant token was not a comma.
	lastComma := -1
	inString := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			if c == '\\' && i+1 < len(in) {
				i++ // skip the escaped byte so it can't toggle string state
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			lastComma = -1
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			// Line comment: blank through end of line (keep the newline).
			for i < len(in) && in[i] != '\n' {
				blank(i)
				i++
			}
			i-- // let the loop consume the newline (or terminate)
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			// Block comment: blank through the closing */ (newlines preserved).
			blank(i)
			blank(i + 1)
			i += 2
			for i < len(in) {
				if i+1 < len(in) && in[i] == '*' && in[i+1] == '/' {
					blank(i)
					blank(i + 1)
					i++ // loop increment moves past the '/'
					break
				}
				blank(i)
				i++
			}
		case c == ',':
			lastComma = i
		case c == '}' || c == ']':
			if lastComma >= 0 {
				out[lastComma] = ' ' // erase the trailing comma
			}
			lastComma = -1
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			// Whitespace does not clear a pending trailing comma.
		default:
			lastComma = -1
		}
	}
	return out
}

// parseJSONC strips JSONC extras from data and unmarshals into v. On a JSON
// syntax error it rewrites the message to a 1-based line:column position, which
// (because stripJSONC preserves offsets and newlines) points into the original
// source.
func parseJSONC(data []byte, v any) error {
	stripped := stripJSONC(data)
	if err := json.Unmarshal(stripped, v); err != nil {
		if se, ok := err.(*json.SyntaxError); ok {
			line, col := offsetToLine(data, int(se.Offset))
			return fmt.Errorf("line %d, column %d: %s", line, col, se)
		}
		return err
	}
	return nil
}

// offsetToLine converts a 0-based byte offset into a 1-based (line, column).
func offsetToLine(data []byte, offset int) (line, col int) {
	line, col = 1, 1
	if offset > len(data) {
		offset = len(data)
	}
	for i := 0; i < offset; i++ {
		if data[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}
