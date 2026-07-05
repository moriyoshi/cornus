package compose

// This file implements the compose-spec `extends` directive: a service inherits
// configuration from a base service, either in the same file or in another
// Compose file. It follows the "extends" section of the compose-spec
// (https://github.com/compose-spec/compose-spec/blob/master/05-services.md#extends).
//
// Precedence: the referenced (base) service is the LOWER precedence and the
// extending service is the HIGHER precedence, so the result is
// mergeService(resolvedBase, extendingServiceWithExtendsCleared) — reusing the
// same field-level deep merge (merge.go) as multi-file loading.
//
// depends_on / cross-service references restriction. The compose-spec
// "extends" > "Restrictions" note is explicit that referenced resources are NOT
// automatically imported into the extended model: when a base service names
// other services or namespaces via depends_on, links, volumes_from, ipc, pid,
// network_mode, etc., the extending file must declare those itself — extends
// does not carry them over. cornus does not model links / volumes_from, but it
// DOES model depends_on, so we DROP the base's depends_on before merging and
// keep only the extending service's own depends_on. (An extends chain a->b->c
// therefore never accumulates b's or c's dependencies onto a.)

import (
	"fmt"
	"path/filepath"
)

// resolveExtends expands every `extends` directive in p's services into a
// fully-merged Service, in place. file is p's own Compose file path (used to
// resolve relative extends.file references and as the cache/cycle key for local
// extends). envFiles and warn thread through to any referenced files, which are
// parsed with the same pipeline (interpolation, env_file, warnings) as p.
func resolveExtends(p *ProjectDocument, file string, envFiles []string, warn func(service, field string)) error {
	// Fast path: nothing to do if no service in this file extends anything. This
	// also keeps the common case free of any filesystem work.
	hasExtends := false
	for _, svc := range p.Services {
		if svc.Extends != nil {
			hasExtends = true
			break
		}
	}
	if !hasExtends {
		return nil
	}

	r := &extendsResolver{
		envFiles:  envFiles,
		warn:      warn,
		files:     map[string]*ProjectDocument{},
		resolving: map[string]bool{},
		resolved:  map[string]ServiceDocument{},
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("compose: %s: %w", file, err)
	}
	// Seed the file cache with the already-parsed project so local extends do not
	// re-read and re-parse the current file.
	r.files[absFile] = p

	// Resolve into a fresh map: resolveService reads raw services from the cache,
	// so building the results separately avoids any mid-pass ambiguity.
	out := make(map[string]ServiceDocument, len(p.Services))
	for name := range p.Services {
		svc, err := r.resolveService(absFile, name)
		if err != nil {
			return err
		}
		out[name] = svc
	}
	p.Services = out
	return nil
}

// extendsResolver carries the state for a single top-level resolveExtends pass:
// a cache of parsed referenced files, the set of (file,service) pairs currently
// on the resolution stack (for cycle detection), and a cache of fully-resolved
// services (so a base shared by several extenders — a diamond — is resolved
// once).
type extendsResolver struct {
	envFiles  []string
	warn      func(service, field string)
	files     map[string]*ProjectDocument // abs file path -> raw parsed project
	resolving map[string]bool             // key(file,service) currently being resolved
	resolved  map[string]ServiceDocument  // key(file,service) -> fully-resolved service
}

// key identifies a service within a file for cycle detection and caching.
func extendsKey(absFile, service string) string {
	return absFile + "\x00" + service
}

// project returns the raw parsed project for absFile, loading and caching it on
// first use. The root file is pre-seeded by resolveExtends.
func (r *extendsResolver) project(absFile string) (*ProjectDocument, error) {
	if p, ok := r.files[absFile]; ok {
		return p, nil
	}
	p, err := parseFile(absFile, r.envFiles, r.warn)
	if err != nil {
		return nil, err
	}
	r.files[absFile] = p
	return p, nil
}

// resolveService returns the named service from absFile with its `extends`
// directive (and any transitive extends) fully expanded and cleared. It detects
// cycles across both local and cross-file references, and errors on a missing
// file or service.
func (r *extendsResolver) resolveService(absFile, name string) (ServiceDocument, error) {
	key := extendsKey(absFile, name)
	if s, ok := r.resolved[key]; ok {
		return s, nil
	}
	if r.resolving[key] {
		return ServiceDocument{}, fmt.Errorf("compose: extends: circular reference: service %q in %s extends itself", name, absFile)
	}

	p, err := r.project(absFile)
	if err != nil {
		return ServiceDocument{}, err
	}
	svc, ok := p.Services[name]
	if !ok {
		return ServiceDocument{}, fmt.Errorf("compose: extends: %s: no such service %q", absFile, name)
	}
	if svc.Extends == nil {
		r.resolved[key] = svc
		return svc, nil
	}

	r.resolving[key] = true
	defer delete(r.resolving, key)

	// Resolve the referenced base file. An absent file: means the same file; a
	// relative file: is resolved against the current file's directory.
	baseFile := absFile
	if svc.Extends.File != "" {
		bf := svc.Extends.File
		if !filepath.IsAbs(bf) {
			bf = filepath.Join(filepath.Dir(absFile), bf)
		}
		baseFile, err = filepath.Abs(bf)
		if err != nil {
			return ServiceDocument{}, fmt.Errorf("compose: extends: %s: %w", svc.Extends.File, err)
		}
	}
	baseName := svc.Extends.Service
	if baseName == "" {
		return ServiceDocument{}, fmt.Errorf("compose: extends: service %q: missing target service name", name)
	}

	// Fully resolve the base (its own extends first), then merge this service on
	// top of it (this service wins). The Extends directive is cleared on the
	// extending copy so it does not linger or get re-processed.
	base, err := r.resolveService(baseFile, baseName)
	if err != nil {
		return ServiceDocument{}, err
	}

	// Per the compose-spec Restrictions: extends does not import cross-service
	// references. Drop the base's depends_on so only this service's own
	// depends_on survives the merge (see the file-level comment).
	base.DependsOn = nil

	extending := svc
	extending.Extends = nil
	result := mergeService(base, extending)
	result.Extends = nil

	r.resolved[key] = result
	return result, nil
}
