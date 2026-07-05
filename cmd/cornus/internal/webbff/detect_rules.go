package webbff

// Detection rules for agentdetect.go: TOML documents (our own schema) matched
// against the rendered bottom buffer of a session's screen to classify it as
// blocked or working. A default set is embedded; users may extend it with files
// under ~/.config/cornus/agent-detection/*.toml. User files add to (never replace)
// the defaults, and a malformed file or pattern is logged and skipped so a bad
// override can never break detection.

import (
	"embed"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	toml "github.com/pelletier/go-toml"
)

//go:embed rules.toml
var embeddedRules embed.FS

// ruleFile is the TOML schema: a list of [[rule]] tables.
type ruleFile struct {
	Rules []ruleSpec `toml:"rule"`
}

type ruleSpec struct {
	State   string `toml:"state"`   // "blocked" | "working"
	Pattern string `toml:"pattern"` // Go (RE2) regexp, matched against the rendered bottom buffer
	Agent   string `toml:"agent"`   // optional: only apply when the detected agent matches
}

// compiledRule is a ruleSpec with its pattern compiled once.
type compiledRule struct {
	state   sessionState
	agent   string
	pattern *regexp.Regexp
}

// ruleSet is the merged, compiled detection rules.
type ruleSet struct {
	rules []compiledRule
}

// matches reports whether any rule for the given state (and matching agent scope)
// hits the rendered screen text. An empty rule.agent applies to every session.
func (rs *ruleSet) matches(state sessionState, agent, screen string) bool {
	if rs == nil {
		return false
	}
	for _, r := range rs.rules {
		if r.state != state {
			continue
		}
		if r.agent != "" && r.agent != agent {
			continue
		}
		if r.pattern.MatchString(screen) {
			return true
		}
	}
	return false
}

// loadRules builds the rule set from the embedded defaults plus any user override
// files. It is called once per BFF (per `cornus web` start).
func loadRules() *ruleSet {
	rs := &ruleSet{}
	if data, err := embeddedRules.ReadFile("rules.toml"); err != nil {
		// Should never happen: rules.toml is embedded at build time.
		slog.Error("agent-detection: embedded rules unreadable", "err", err)
	} else {
		rs.add("<embedded>", data)
	}
	for _, p := range userRuleFiles() {
		data, err := os.ReadFile(p)
		if err != nil {
			slog.Warn("agent-detection: skipping unreadable rule file", "path", p, "err", err)
			continue
		}
		rs.add(p, data)
	}
	return rs
}

// add parses one TOML document and appends its valid, compilable rules. Invalid
// rules are logged and skipped individually; a document that fails to parse is
// skipped whole.
func (rs *ruleSet) add(source string, data []byte) {
	var f ruleFile
	if err := toml.Unmarshal(data, &f); err != nil {
		slog.Warn("agent-detection: skipping malformed rule file", "source", source, "err", err)
		return
	}
	for i, spec := range f.Rules {
		state := sessionState(spec.State)
		if state != stateBlocked && state != stateWorking {
			slog.Warn("agent-detection: skipping rule with unknown state",
				"source", source, "index", i, "state", spec.State)
			continue
		}
		re, err := regexp.Compile(spec.Pattern)
		if err != nil {
			slog.Warn("agent-detection: skipping rule with bad pattern",
				"source", source, "index", i, "pattern", spec.Pattern, "err", err)
			continue
		}
		rs.rules = append(rs.rules, compiledRule{state: state, agent: spec.Agent, pattern: re})
	}
}

// userRuleFiles lists ~/.config/cornus/agent-detection/*.toml (sorted for a stable
// load order). A missing directory just yields no files.
func userRuleFiles() []string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "cornus", "agent-detection", "*.toml"))
	sort.Strings(matches)
	return matches
}
