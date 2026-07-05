package agentdetect

import (
	"path"
	"strings"
)

// AgentName resolves which agent a foreground process IS, from the kernel's
// process name plus its argv.
//
// The process name alone is not enough, and that is the whole reason this
// function exists. `/proc/<pid>/comm` is truncated to 15 bytes and names the
// thread of the program that is EXECUTING, not the program the user thinks they
// ran: a Node-based agent reports "node" (or "node-MainThread", which Node sets
// on its main thread), never "claude". Scoping detection on comm alone therefore
// matches nothing for any agent behind an interpreter, which is most of them.
//
// The resolution order is upstream's (herdr src/detect/mod.rs):
//
//  1. If the process name is a generic runtime or shell, unwrap the agent from
//     argv — skipping the flags that carry a script rather than a program.
//  2. Otherwise, if the process name itself names an agent, use it.
//  3. Otherwise fall back to argv[0]'s basename.
//
// known reports whether a candidate name is an agent we have a manifest for;
// pass nil to accept any plausible name, which is what a caller wanting a label
// rather than a manifest lookup wants.
func AgentName(comm string, argv []string, known func(string) bool) string {
	accept := func(name string) (string, bool) {
		n := normalizeLookupName(path.Base(name))
		if n == "" || n == "." || n == "/" || strings.HasPrefix(n, "-") {
			return "", false
		}
		if known != nil && !known(n) {
			return "", false
		}
		return n, true
	}

	effective := normalizeLookupName(path.Base(strings.TrimSpace(comm)))

	// (1) A runtime or shell in front means the interesting name is in argv.
	if IsGenericRuntimeOrShell(effective) {
		if name := wrappedAgentFromArgv(effective, argv, accept); name != "" {
			return name
		}
	}
	// (2) The process name itself, when it names something we know.
	if name, ok := accept(effective); ok {
		return name
	}
	// (3) argv[0], which is where a shim or a wrapper puts the real program.
	if len(argv) > 0 {
		if name, ok := accept(argv[0]); ok {
			return name
		}
	}
	return ""
}

// IsGenericRuntimeOrShell reports whether a program name is an interpreter or
// shell — something that runs OTHER programs, so its own name says nothing about
// what the user is doing.
func IsGenericRuntimeOrShell(name string) bool {
	switch normalizeLookupName(path.Base(name)) {
	case "sh", "bash", "zsh", "fish", "dash", "ash", "tmux",
		"node", "bun", "deno", "python", "python3", "ruby", "perl",
		"cmd", "powershell", "pwsh", "env", "npx", "bunx", "uv", "uvx":
		return true
	}
	return false
}

// normalizeLookupName lowercases and strips the extensions a launcher adds, so
// "Claude.CMD" and "claude.js" both look up as "claude".
//
// It also strips Node's main-thread suffix. Node names its main thread
// "node-MainThread", which is what lands in comm on Linux; without this the name
// is not even recognisable as "node" and step (1) above never fires.
func normalizeLookupName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1", ".js", ".mjs", ".cjs"} {
		if strings.HasSuffix(n, suffix) {
			n = strings.TrimSuffix(n, suffix)
			break
		}
	}
	if i := strings.IndexByte(n, '-'); i > 0 && strings.HasSuffix(n, "-mainthread") {
		n = n[:i]
	}
	return n
}

// wrappedAgentFromArgv finds the program a runtime was asked to run.
//
// The per-runtime flag lists are the point: `node -e '<code>'` has no script
// path, and treating the code as one would name the agent after a fragment of
// JavaScript. Flags that TAKE a value are skipped along with their value; flags
// whose value IS the program (python -m) name it directly.
func wrappedAgentFromArgv(runtime string, argv []string, accept func(string) (string, bool)) string {
	if len(argv) < 2 {
		return ""
	}
	var inlineFlags, moduleFlags []string
	switch runtime {
	case "node", "bun", "deno":
		inlineFlags = []string{"-e", "--eval", "-p", "--print"}
	case "python", "python3":
		inlineFlags = []string{"-c"}
		moduleFlags = []string{"-m"}
	case "ruby", "perl":
		inlineFlags = []string{"-e"}
	case "sh", "bash", "zsh", "fish", "dash", "ash":
		inlineFlags = []string{"-c"}
	case "env", "npx", "bunx", "uvx":
		// These take a program name directly, after any VAR=value or flags.
	default:
		return ""
	}

	for i := 1; i < len(argv); i++ {
		a := argv[i]
		// An inline-code flag means there is no script to name; stop rather than
		// mistake the code for a path.
		if contains(inlineFlags, a) {
			return ""
		}
		if contains(moduleFlags, a) && i+1 < len(argv) {
			if name, ok := accept(argv[i+1]); ok {
				return name
			}
			return ""
		}
		// `env` passes VAR=value assignments before the program.
		if runtime == "env" && strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if name, ok := accept(a); ok {
			return name
		}
		// The first non-flag operand is the script/program. If it did not resolve
		// to a known agent, nothing later will either.
		return ""
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
