package compose

// This file implements the compose-spec top-level `include:` directive: a
// project pulls one or more other Compose models into itself
// (https://github.com/compose-spec/compose-spec/blob/master/14-include.md).
//
// Each included model is loaded as its OWN complete Compose model — its own
// ${VAR} interpolation (with its own env_file / sibling .env), its own `extends`
// expansion, and its own nested `include:` — and then merged into the including
// project.
//
// Precedence: the MAIN (including) file wins on conflict. So for a service (or
// def) present in both, the result is mergeService(includedService,
// mainService) — the included model is the merge base and the including file
// overrides it field-by-field, reusing the same deep-merge (merge.go) as
// multi-file loading and `extends`. Included-only definitions are added as-is.
//
// Ordering of multiple includes / files: they are folded in sequence with a
// later include overriding an earlier one (mirroring `-f a -f b` file order),
// and finally the including file overrides them all.
//
// project_directory (APPROXIMATED). The compose-spec `project_directory` sets
// the base directory against which the INCLUDED model's own relative paths
// (build contexts, bind-mount sources, service env_file) resolve. cornus does
// not resolve those per-model at load time — it resolves them downstream against
// a single top-level project directory (see ServicePlan.ResolveMounts and the
// build-context handling in the caller). There is therefore no per-service
// origin dir to attach a distinct project_directory to, so the field is parsed
// and honored for resolving the include entry's OWN env_file, but it does not
// rewrite the included model's internal relative paths. In the common case
// (included files live in / beside the including file's directory) this matches
// Compose; a deeply relocated included model with relative build contexts is the
// gap.

import (
	"fmt"
	"path/filepath"
)

// processInclude expands p's top-level `include:` directives in place, folding
// each included model in under p (p wins on conflict) and clearing p.Include.
// file is p's own Compose file path; envFiles are the env files p itself was
// interpolated with (they are NOT inherited by included models — an included
// model uses the include entry's env_file, or its own sibling .env). It is a
// no-op (and does no filesystem work) when p has no includes.
func processInclude(p *ProjectDocument, file string, envFiles []string, warn func(service, field string)) error {
	if len(p.Include) == 0 {
		return nil
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("compose: %s: %w", file, err)
	}
	r := &includeResolver{warn: warn}
	// Seed the cycle-detection stack with the including file so an entry that
	// includes it directly (or transitively) is caught.
	return r.expand(p, absFile, map[string]bool{absFile: true})
}

// includeResolver carries the state for one nested include expansion: the warn
// sink threaded to every loaded model, and the set of absolute file paths
// currently on the include stack (for cycle detection).
type includeResolver struct {
	warn func(service, field string)
}

// expand resolves p's includes (p already parsed + extends-resolved, its
// Include populated) against absFile's directory, then overlays p's own
// definitions on top so the including file wins. stack holds the abs paths of
// files currently being expanded, including absFile.
func (r *includeResolver) expand(p *ProjectDocument, absFile string, stack map[string]bool) error {
	includes := p.Include
	p.Include = nil
	if len(includes) == 0 {
		return nil
	}
	dir := filepath.Dir(absFile)

	// base accumulates the merged content of every included model; a later
	// include overrides an earlier one.
	base := &ProjectDocument{}
	for _, ref := range includes {
		// The include entry's own env_file resolves against the including file's
		// directory (or project_directory when set — see the file-level note).
		envBase := dir
		if ref.ProjectDirectory != "" {
			pd := ref.ProjectDirectory
			if !filepath.IsAbs(pd) {
				pd = filepath.Join(dir, pd)
			}
			envBase = pd
		}
		envFiles := resolveEnvFiles(envBase, ref.EnvFile)

		for _, rel := range ref.Path {
			incAbs, err := absInclude(dir, rel)
			if err != nil {
				return fmt.Errorf("compose: include: %s: %w", rel, err)
			}
			if stack[incAbs] {
				return fmt.Errorf("compose: include: circular reference: %s", incAbs)
			}
			inc, err := r.load(incAbs, envFiles, stack)
			if err != nil {
				return err
			}
			mergeProjectInto(base, inc)
		}
	}

	// The including file overrides every included model.
	mergeProjectInto(base, p)

	p.Name = base.Name
	p.Services = base.Services
	p.Secrets = base.Secrets
	p.Volumes = base.Volumes
	p.Networks = base.Networks
	return nil
}

// load parses, extends-resolves, and recursively include-expands the included
// model at absFile, returning a fully-expanded Project. envFiles are the env
// files for interpolating this model (from the include entry, or nil to use the
// model's sibling .env). absFile is pushed onto stack while its own nested
// includes expand so a transitive cycle back to it is detected.
func (r *includeResolver) load(absFile string, envFiles []string, stack map[string]bool) (*ProjectDocument, error) {
	p, err := parseFile(absFile, envFiles, r.warn)
	if err != nil {
		return nil, fmt.Errorf("compose: include: %s: %w", absFile, err)
	}
	if err := resolveExtends(p, absFile, envFiles, r.warn); err != nil {
		return nil, err
	}
	stack[absFile] = true
	defer delete(stack, absFile)
	if err := r.expand(p, absFile, stack); err != nil {
		return nil, err
	}
	return p, nil
}

// absInclude resolves an include path (relative to the including file's dir) to
// an absolute path.
func absInclude(dir, rel string) (string, error) {
	if !filepath.IsAbs(rel) {
		rel = filepath.Join(dir, rel)
	}
	return filepath.Abs(rel)
}

// resolveEnvFiles resolves an include entry's env_file list against base,
// returning absolute paths. It returns nil for an empty list so the included
// model falls back to its own sibling .env (parseFile with no explicit files).
func resolveEnvFiles(base string, envFile []string) []string {
	if len(envFile) == 0 {
		return nil
	}
	out := make([]string, 0, len(envFile))
	for _, f := range envFile {
		if !filepath.IsAbs(f) {
			f = filepath.Join(base, f)
		}
		out = append(out, f)
	}
	return out
}

// mergeProjectInto deep-merges override on top of dst (override wins), reusing
// the compose-spec field-level merge helpers (merge.go). It mutates dst,
// creating its maps lazily. Used to fold included models together and to overlay
// the including file on top of them.
func mergeProjectInto(dst, override *ProjectDocument) {
	if override.Name != "" {
		dst.Name = override.Name
	}
	for name, svc := range override.Services {
		if dst.Services == nil {
			dst.Services = map[string]ServiceDocument{}
		}
		if existing, ok := dst.Services[name]; ok {
			dst.Services[name] = mergeService(existing, svc)
		} else {
			dst.Services[name] = svc
		}
	}
	for name, sec := range override.Secrets {
		if dst.Secrets == nil {
			dst.Secrets = map[string]SecretDefDocument{}
		}
		if existing, ok := dst.Secrets[name]; ok {
			dst.Secrets[name] = mergeSecretDef(existing, sec)
		} else {
			dst.Secrets[name] = sec
		}
	}
	for name, vol := range override.Volumes {
		if dst.Volumes == nil {
			dst.Volumes = map[string]VolumeDefDocument{}
		}
		if existing, ok := dst.Volumes[name]; ok {
			dst.Volumes[name] = mergeVolumeDef(existing, vol)
		} else {
			dst.Volumes[name] = vol
		}
	}
	for name, net := range override.Networks {
		if dst.Networks == nil {
			dst.Networks = map[string]NetworkDefDocument{}
		}
		if existing, ok := dst.Networks[name]; ok {
			dst.Networks[name] = mergeNetworkDef(existing, net)
		} else {
			dst.Networks[name] = net
		}
	}
}
