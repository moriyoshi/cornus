package main

import "testing"

func TestParseResolveRules(t *testing.T) {
	rules, err := parseResolveRules([]string{
		`^(.*):5000$=\1:10000`,
		`^(.*)\.svc$=\1:80`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
	if rules[0].Pattern != `^(.*):5000$` || rules[0].Replace != `\1:10000` {
		t.Errorf("rule 0 = %+v", rules[0])
	}
	// Split is on the FIRST '=', so a '=' in the replacement is preserved.
	rules, err = parseResolveRules([]string{`^(.*)$=\1:80`})
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].Replace != `\1:80` {
		t.Errorf("replace = %q", rules[0].Replace)
	}
}

func TestParseResolveRulesErrors(t *testing.T) {
	if _, err := parseResolveRules([]string{"no-equals-sign"}); err == nil {
		t.Error("want error for missing '='")
	}
	if _, err := parseResolveRules([]string{`=\1:80`}); err == nil {
		t.Error("want error for empty pattern")
	}
	if _, err := parseResolveRules([]string{`([bad=repl`}); err == nil {
		t.Error("want error for uncompilable pattern")
	}
}
