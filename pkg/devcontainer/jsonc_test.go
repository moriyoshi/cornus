package devcontainer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripJSONCPreservesLengthAndNewlines(t *testing.T) {
	in := []byte("{\n  // a comment\n  \"a\": 1, /* block\n   spanning */ \"b\": 2,\n}")
	out := stripJSONC(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: in=%d out=%d", len(in), len(out))
	}
	// Newline offsets must be identical so error offsets map back to the source.
	for i := range in {
		if (in[i] == '\n') != (out[i] == '\n') {
			t.Fatalf("newline moved at offset %d", i)
		}
	}
	var v map[string]int
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stripped output not valid JSON: %v\n%s", err, out)
	}
	if v["a"] != 1 || v["b"] != 2 {
		t.Fatalf("unexpected decode: %+v", v)
	}
}

func TestStripJSONCLeavesStringsAlone(t *testing.T) {
	in := []byte(`{"url": "http://example.com/x", "note": "a, b, // not a comment"}`)
	out := stripJSONC(in)
	var v map[string]string
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v["url"] != "http://example.com/x" {
		t.Errorf("url mangled: %q", v["url"])
	}
	if v["note"] != "a, b, // not a comment" {
		t.Errorf("note mangled: %q", v["note"])
	}
}

func TestStripJSONCTrailingCommas(t *testing.T) {
	in := []byte(`{"a": [1, 2, 3,], "b": {"c": 1,},}`)
	out := stripJSONC(in)
	var v struct {
		A []int          `json:"a"`
		B map[string]int `json:"b"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(v.A) != 3 || v.B["c"] != 1 {
		t.Fatalf("unexpected: %+v", v)
	}
}

func TestStripJSONCEscapedQuoteInString(t *testing.T) {
	// A string containing an escaped quote followed by a // must not be treated
	// as a comment start once the string closes.
	in := []byte(`{"a": "he said \"hi\"", "b": 2}`)
	out := stripJSONC(in)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if v["a"] != `he said "hi"` {
		t.Errorf("string mangled: %v", v["a"])
	}
}

func TestParseJSONCReportsLine(t *testing.T) {
	// Missing colon on line 3 -> a syntax error whose reported line points there.
	in := []byte("{\n  \"a\": 1,\n  \"b\" 2\n}")
	var v map[string]int
	err := parseJSONC(in, &v)
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error should point at line 3, got: %v", err)
	}
}
