package compose

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mapping resolves a variable name to its value, reporting whether it is set.
type Mapping func(name string) (string, bool)

// envMapping builds the interpolation variable source: the process environment
// overlaid on env-file values (process env wins), matching Docker Compose. With
// no explicit envFiles it reads the sibling ".env" in dir (tolerating its
// absence). With explicit envFiles (compose --env-file) it reads those instead,
// in order with later entries winning; each is resolved as given (relative paths
// against the process working directory, docker parity) and a missing one is an
// error, since the user named it explicitly.
func envMapping(dir string, envFiles []string, onFile func(string)) (Mapping, error) {
	overlay := map[string]string{}
	if len(envFiles) == 0 {
		dotEnv := filepath.Join(dir, ".env")
		if onFile != nil {
			onFile(dotEnv) // record even when absent (watch retriggers if .env is created)
		}
		if b, err := os.ReadFile(dotEnv); err == nil {
			overlay, err = parseEnvBytes(b)
			if err != nil {
				return nil, fmt.Errorf(".env: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		for _, f := range envFiles {
			if onFile != nil {
				onFile(f)
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("env_file %s: %w", f, err)
			}
			kv, err := parseEnvBytes(b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			for k, v := range kv {
				overlay[k] = v
			}
		}
	}
	return func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		v, ok := overlay[name]
		return v, ok
	}, nil
}

// interpolate recursively expands ${VAR} references in every string value of a
// decoded YAML structure. Map keys are left untouched, matching Compose.
func interpolate(v any, m Mapping) (any, error) {
	switch t := v.(type) {
	case string:
		return expandString(t, m)
	case []any:
		for i := range t {
			nv, err := interpolate(t[i], m)
			if err != nil {
				return nil, err
			}
			t[i] = nv
		}
		return t, nil
	case map[string]any:
		for k := range t {
			nv, err := interpolate(t[k], m)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
		return t, nil
	default:
		return v, nil
	}
}

// expandString expands $VAR, ${VAR}, and the ${VAR<op>word} forms in s. "$$" is
// an escaped literal "$".
func expandString(s string, m Mapping) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		// Escaped "$$".
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		// Braced ${...} (depth-aware so ${A:-${B}} is captured whole).
		if i+1 < len(s) && s[i+1] == '{' {
			depth, j := 1, i+2
			for j < len(s) {
				if s[j] == '{' {
					depth++
				} else if s[j] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
				j++
			}
			if j >= len(s) {
				return "", fmt.Errorf("unterminated ${...} in %q", s)
			}
			val, err := expandBraced(s[i+2:j], m)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i = j + 1
			continue
		}
		// Bare $NAME.
		j := i + 1
		for j < len(s) && isNameChar(s[j]) {
			j++
		}
		if j == i+1 {
			b.WriteByte('$') // lone '$'
			i++
			continue
		}
		v, _ := m(s[i+1 : j])
		b.WriteString(v)
		i = j
	}
	return b.String(), nil
}

// expandBraced expands the contents of a ${...} expression (without the braces),
// honoring the :-, -, :?, ?, :+, and + operators.
func expandBraced(expr string, m Mapping) (string, error) {
	k := 0
	for k < len(expr) && isNameChar(expr[k]) {
		k++
	}
	name, rest := expr[:k], expr[k:]
	if name == "" {
		return "", fmt.Errorf("invalid variable reference ${%s}", expr)
	}
	val, set := m(name)
	if rest == "" {
		return val, nil
	}

	var op, word string
	switch {
	case strings.HasPrefix(rest, ":-"), strings.HasPrefix(rest, ":?"), strings.HasPrefix(rest, ":+"):
		op, word = rest[:2], rest[2:]
	case strings.HasPrefix(rest, "-"), strings.HasPrefix(rest, "?"), strings.HasPrefix(rest, "+"):
		op, word = rest[:1], rest[1:]
	default:
		return "", fmt.Errorf("invalid operator in ${%s}", expr)
	}

	word, err := expandString(word, m) // defaults/messages may reference other vars
	if err != nil {
		return "", err
	}
	empty := !set || val == ""

	switch op {
	case ":-":
		if empty {
			return word, nil
		}
		return val, nil
	case "-":
		if !set {
			return word, nil
		}
		return val, nil
	case ":+":
		if !empty {
			return word, nil
		}
		return "", nil
	case "+":
		if set {
			return word, nil
		}
		return "", nil
	case ":?":
		if empty {
			return "", fmt.Errorf("required variable %q is unset or empty: %s", name, word)
		}
		return val, nil
	case "?":
		if !set {
			return "", fmt.Errorf("required variable %q is unset: %s", name, word)
		}
		return val, nil
	}
	return val, nil
}

func isNameChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// parseEnvBytes parses KEY=VALUE lines (the .env / env_file format): blank lines
// and # comments are skipped, an optional leading "export " is stripped, and
// surrounding single or double quotes are removed.
func parseEnvBytes(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = unquoteEnv(strings.TrimSpace(val))
	}
	return out, sc.Err()
}

func unquoteEnv(v string) string {
	if len(v) >= 2 {
		// Double-quoted values: strip the surrounding quotes and expand the
		// escape sequences Docker Compose recognises in .env files.
		if v[0] == '"' && v[len(v)-1] == '"' {
			return expandEnvEscapes(v[1 : len(v)-1])
		}
		// Single-quoted values are literal: strip the surrounding quotes only.
		if v[0] == '\'' && v[len(v)-1] == '\'' {
			return v[1 : len(v)-1]
		}
	}
	// Unquoted values are returned as-is (no escape processing).
	// NOTE: multiline values and the env_file "format:" key are not handled here.
	return v
}

// expandEnvEscapes expands the escape sequences Docker Compose recognises inside
// double-quoted .env / env_file values. It mirrors compose-go/v2's dotenv
// expansion, which matches \\(?:[abcfnrtv$"\\]|0\d{0,3}) and unquotes it: the
// C-style single-character escapes (\a \b \f \n \r \t \v \\ \" and \$ -> $) plus
// an octal escape introduced by \0 and up to three further octal digits
// (e.g. \0101 -> 'A'). Any other \x sequence is left as-is (a literal backslash
// followed by the character).
func expandEnvEscapes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch c := s[i+1]; c {
			case 'a':
				b.WriteByte('\a')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'v':
				b.WriteByte('\v')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case '$':
				b.WriteByte('$')
			case '0':
				// Octal escape: \0 followed by up to three further octal digits.
				val := 0
				j := i + 2
				for ; j < len(s) && j < i+5 && s[j] >= '0' && s[j] <= '7'; j++ {
					val = val*8 + int(s[j]-'0')
				}
				b.WriteByte(byte(val))
				i = j - 1
				continue
			default:
				b.WriteByte('\\')
				b.WriteByte(c)
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
