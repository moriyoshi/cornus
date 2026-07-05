// Package jsonstore reads a local coding-agent credential store — a JSON file on
// the developer's machine — and extracts values at dotted paths. It is shared by
// the per-tool source backends (claude-code, codex), each of which supplies the
// store's default path and the paths of the fields it cares about. Nothing here
// is tool-specific.
package jsonstore

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Load reads and parses a JSON object file. The error mentions the path so a
// not-logged-in store is diagnosable.
func Load(file string) (map[string]any, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return doc, nil
}

// Walk descends a dot-separated path through nested JSON objects, returning the
// leaf value or nil if any segment is missing or not an object.
func Walk(doc map[string]any, path string) any {
	var cur any = doc
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

// String returns the string value at path, or "" if absent / not a string.
func String(doc map[string]any, path string) string {
	s, _ := Walk(doc, path).(string)
	return s
}
