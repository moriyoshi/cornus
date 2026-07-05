package devcontainer

import (
	"os"
	"regexp"
	"strings"
)

// varRef matches a ${...} devcontainer variable reference.
var varRef = regexp.MustCompile(`\$\{([^}]+)\}`)

// varContext resolves the devcontainer variables cornus supports in mounts,
// env, and workspaceMount values. Anything else is left literal and reported.
type varContext struct {
	localWorkspaceFolder         string // absolute host path of the project root
	localWorkspaceFolderBasename string
	containerWorkspaceFolder     string // the resolved workspaceFolder in the container
	getenv                       func(string) string
}

// substitute expands supported ${...} references in s. Every reference it does
// not recognise is left verbatim and its name appended to unresolved (so the
// caller can warn once). Supported: ${localWorkspaceFolder},
// ${localWorkspaceFolderBasename}, ${containerWorkspaceFolder},
// ${containerWorkspaceFolderBasename}, and ${localEnv:NAME[:default]}.
func (c varContext) substitute(s string) (string, []string) {
	getenv := c.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	var unresolved []string
	out := varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1] // strip ${ and }
		switch {
		case name == "localWorkspaceFolder":
			return c.localWorkspaceFolder
		case name == "localWorkspaceFolderBasename":
			return c.localWorkspaceFolderBasename
		case name == "containerWorkspaceFolder":
			return c.containerWorkspaceFolder
		case name == "containerWorkspaceFolderBasename":
			return basename(c.containerWorkspaceFolder)
		case strings.HasPrefix(name, "localEnv:"):
			key, def, hasDef := strings.Cut(name[len("localEnv:"):], ":")
			if v := getenv(key); v != "" {
				return v
			}
			if hasDef {
				return def
			}
			return ""
		default:
			unresolved = append(unresolved, match)
			return match
		}
	})
	return out, unresolved
}

// basename returns the last path element of a container (always forward-slash)
// path.
func basename(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
