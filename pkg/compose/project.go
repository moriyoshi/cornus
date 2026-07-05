package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// supportedServiceFields is the set of Compose service keys cornus translates
// (see the Service struct and translateService). Any other key present on a
// service is recognised-but-ignored: the loader warns once for it rather than
// silently dropping it. Keep this in sync with the Service struct.
var supportedServiceFields = map[string]struct{}{
	"image":                  {},
	"build":                  {},
	"entrypoint":             {},
	"command":                {},
	"environment":            {},
	"env_file":               {},
	"ports":                  {},
	"expose":                 {},
	"volumes":                {},
	"networks":               {},
	"restart":                {},
	"depends_on":             {},
	"deploy":                 {},
	"healthcheck":            {},
	"container_name":         {},
	"privileged":             {},
	"profiles":               {},
	"extends":                {},
	"configs":                {},
	"secrets":                {},
	"user":                   {},
	"working_dir":            {},
	"hostname":               {},
	"labels":                 {},
	"stop_signal":            {},
	"stop_grace_period":      {},
	"init":                   {},
	"tty":                    {},
	"stdin_open":             {},
	"read_only":              {},
	"cap_add":                {},
	"cap_drop":               {},
	"security_opt":           {},
	"group_add":              {},
	"sysctls":                {},
	"extra_hosts":            {},
	"dns":                    {},
	"dns_search":             {},
	"dns_opt":                {},
	"ulimits":                {},
	"tmpfs":                  {},
	"devices":                {},
	"shm_size":               {},
	"pid":                    {},
	"ipc":                    {},
	"mem_limit":              {},
	"cpus":                   {},
	"x-cornus-egress":        {},
	"x-cornus-ingress":       {},
	"x-cornus-agent-forward": {},
	"x-cornus-telemetry":     {},
}

// supportedDeployFields is the set of Compose `deploy:` sub-keys cornus
// honours. Other deploy sub-keys (placement, ...) are ignored and warned about
// as "deploy.<key>".
// supportedDeployFields is the set of Compose `deploy:` sub-keys cornus
// honours. The swarm-scheduler-only sub-keys (placement, mode, endpoint_mode,
// rollback_config) are intentionally absent: a deploy-to-a-single-server model
// cannot express them, so they stay in warnUnsupportedFields' warn-not-drop path
// as "deploy.<key>". reservations lives NESTED under resources, so it is not a
// top-level deploy key and does not need listing here.
var supportedDeployFields = map[string]struct{}{
	"replicas":       {},
	"resources":      {},
	"restart_policy": {},
	"labels":         {},
	"update_config":  {},
}

// warnUnsupportedFields walks the raw (post-interpolation) generic decode of a
// Compose file and reports every service field cornus does not translate. It
// calls warn(service, field) for each such field; deduplication and emission are
// the caller's responsibility. Fields are visited in a deterministic order.
func warnUnsupportedFields(generic any, warn func(service, field string)) {
	root, ok := generic.(map[string]any)
	if !ok {
		return
	}
	services, ok := root["services"].(map[string]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(services))
	for n := range services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		svc, ok := services[name].(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(svc))
		for k := range svc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := supportedServiceFields[k]; !ok {
				warn(name, k)
				continue
			}
			if k == "deploy" {
				dep, ok := svc[k].(map[string]any)
				if !ok {
					continue
				}
				dks := make([]string, 0, len(dep))
				for dk := range dep {
					dks = append(dks, dk)
				}
				sort.Strings(dks)
				for _, dk := range dks {
					if _, ok := supportedDeployFields[dk]; !ok {
						warn(name, "deploy."+dk)
					}
				}
			}
		}
	}
}

// ServicePlan is a translated service: the deployment spec plus any build
// instructions. Resource is the cornus deployment name (project-qualified).
type ServicePlan struct {
	Service  string // service name within the project
	Resource string // deployment name: "<project>-<service>"
	Spec     api.DeploySpec
	Build    *BuildPlan // non-nil when the service must be built
}

// ResolveMounts rewrites each bind-mount source to an absolute path relative to
// baseDir (the project directory), mirroring how Docker Compose and cornus's
// build contexts resolve relative paths. Absolute sources are left unchanged.
// Without this, a relative source like "./data" reaches the Docker daemon
// verbatim and is rejected as an invalid named-volume name.
func (p ServicePlan) ResolveMounts(baseDir string) {
	for i := range p.Spec.Mounts {
		src := p.Spec.Mounts[i].Source
		if src == "" || filepath.IsAbs(src) {
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(baseDir, src)); err == nil {
			p.Spec.Mounts[i].Source = abs
		}
	}
}

// BuildPlan describes an image build for a service.
type BuildPlan struct {
	Context    string
	Dockerfile string
	Args       map[string]string
	// Target is the Dockerfile multi-stage target stage (build.target). Empty
	// builds the final stage.
	Target string
	// CacheFrom lists registry references to import build cache from
	// (build.cache_from).
	CacheFrom []string
	// Image is the explicit image: tag if set, else empty (the caller derives
	// a registry tag).
	Image string
	// AdditionalContexts are extra named build contexts (name -> host dir),
	// resolved by the caller against the project directory.
	AdditionalContexts map[string]string
	// Secrets maps a build secret id to its host file path (file-based secrets
	// only), resolved by the caller against the project directory.
	Secrets map[string]string
	// SSH are SSH agent forwarding specs (RUN --mount=type=ssh), each a bare id
	// ("default") or "id=source"; a bare id resolves to $SSH_AUTH_SOCK when the
	// build runs.
	SSH []string
	// Labels are image labels applied to the built image (build.labels).
	Labels map[string]string
	// NoCache disables the build cache for this build (build.no_cache).
	NoCache bool
	// Pull always attempts to pull a newer base image (build.pull).
	Pull bool
	// Platforms are the target build platforms (build.platforms).
	Platforms []string
	// Tags are additional image references for the build result (build.tags).
	Tags []string
	// Network is the build-time network mode (build.network): default/none/host.
	Network string
	// CacheTo lists build-cache export specs (build.cache_to), buildx-style
	// "type=...,k=v" strings or a bare registry ref.
	CacheTo []string
	// ExtraHosts adds custom /etc/hosts entries during the build (build.extra_hosts),
	// each normalised "host:ip".
	ExtraHosts []string
	// ShmSize sizes /dev/shm for RUN steps in bytes (build.shm_size); 0 leaves the
	// engine default.
	ShmSize int64
	// DockerfileInline is an inline Dockerfile body (build.dockerfile_inline). When
	// non-empty it supersedes Dockerfile / the context Dockerfile.
	DockerfileInline string
}

// LoadOptions tunes Load. The zero value reproduces the default behavior
// (sibling .env interpolation).
type LoadOptions struct {
	// EnvFiles, when non-empty, are the env file(s) used for ${VAR} interpolation
	// instead of the default sibling .env (compose --env-file): loaded in order
	// with later entries winning, and the process environment still overriding
	// all of them. A missing explicitly-named file is an error.
	EnvFiles []string
}

// Load reads and merges one or more Compose files with default options.
func Load(files ...string) (*Project, error) {
	return LoadWithOptions(LoadOptions{}, files...)
}

// LoadWithOptions reads and merges one or more Compose files into a Project,
// sealing the merged ProjectDocument (see LoadDocumentWithOptions) behind
// Project's behavior-oriented API. Once returned, the document is not
// reachable from the Project again — a caller that needs to keep editing the
// parsed data after loading (e.g. pkg/devcontainer overlaying settings onto a
// service) should call LoadDocument/LoadDocumentWithOptions directly and wrap
// the result with NewProject only once editing is done.
func LoadWithOptions(opts LoadOptions, files ...string) (*Project, error) {
	doc, err := LoadDocumentWithOptions(opts, files...)
	if err != nil {
		return nil, err
	}
	return NewProject(doc), nil
}

// LoadDocument reads and merges one or more Compose files with default
// options, returning the plain ProjectDocument before it is sealed into a
// Project. See LoadDocumentWithOptions.
func LoadDocument(files ...string) (*ProjectDocument, error) {
	return LoadDocumentWithOptions(LoadOptions{}, files...)
}

// LoadDocumentWithOptions reads and merges one or more Compose files. Each
// file is variable-interpolated (using the process environment and either a
// sibling .env file or opts.EnvFiles) and has its services' env_file entries
// applied before merging. Later files override earlier ones at the service
// level.
func LoadDocumentWithOptions(opts LoadOptions, files ...string) (*ProjectDocument, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("compose: no files given")
	}
	merged := &ProjectDocument{Services: map[string]ServiceDocument{}}
	// Warn once (to stderr via slog) for each recognised-but-unsupported service
	// field, deduplicated across services and across merged files so a repeated
	// field is reported a single time.
	seen := map[string]struct{}{}
	warn := func(service, field string) {
		msg := fmt.Sprintf("service %q: field %q is not supported and was ignored", service, field)
		if _, ok := seen[msg]; ok {
			return
		}
		seen[msg] = struct{}{}
		// Load-time helper with no request context; log against the process default.
		ctx := context.Background()
		logging.FromContext(ctx).WarnContext(ctx, msg, slog.String("component", "compose"))
	}
	for _, f := range files {
		p, err := loadFile(f, opts.EnvFiles, warn)
		if err != nil {
			return nil, err
		}
		if p.Name != "" {
			merged.Name = p.Name
		}
		// Compose-spec field-level deep merge: a service (or def) already present
		// from an earlier file is merged with the later file's version (later wins
		// per field) rather than replaced wholesale. See merge.go for the rules.
		if p.Egress != nil {
			merged.Egress = p.Egress // last file with a project-level egress: wins
		}
		if p.Ingress != nil {
			merged.Ingress = p.Ingress // last file with a project-level ingress: wins
		}
		if p.Telemetry != nil {
			merged.Telemetry = p.Telemetry // last file with a project-level telemetry: wins
		}
		for name, svc := range p.Services {
			if existing, ok := merged.Services[name]; ok {
				merged.Services[name] = mergeService(existing, svc)
			} else {
				merged.Services[name] = svc
			}
		}
		for name, sec := range p.Secrets {
			if merged.Secrets == nil {
				merged.Secrets = map[string]SecretDefDocument{}
			}
			if existing, ok := merged.Secrets[name]; ok {
				merged.Secrets[name] = mergeSecretDef(existing, sec)
			} else {
				merged.Secrets[name] = sec
			}
		}
		for name, cfg := range p.Configs {
			if merged.Configs == nil {
				merged.Configs = map[string]ConfigDefDocument{}
			}
			if existing, ok := merged.Configs[name]; ok {
				merged.Configs[name] = mergeConfigDef(existing, cfg)
			} else {
				merged.Configs[name] = cfg
			}
		}
		for name, vol := range p.Volumes {
			if merged.Volumes == nil {
				merged.Volumes = map[string]VolumeDefDocument{}
			}
			if existing, ok := merged.Volumes[name]; ok {
				merged.Volumes[name] = mergeVolumeDef(existing, vol)
			} else {
				merged.Volumes[name] = vol
			}
		}
		for name, net := range p.Networks {
			if merged.Networks == nil {
				merged.Networks = map[string]NetworkDefDocument{}
			}
			if existing, ok := merged.Networks[name]; ok {
				merged.Networks[name] = mergeNetworkDef(existing, net)
			} else {
				merged.Networks[name] = net
			}
		}
	}
	if len(merged.Services) == 0 {
		return nil, fmt.Errorf("compose: no services defined")
	}
	return merged, nil
}

// Project is the internal, behavior-bearing representation of a loaded
// Compose project: dependency ordering (Order), deploy-plan translation
// (Plan), and profile-view derivation (View). It holds the internal
// representation of each field from the ProjectDocument it was built from —
// ServiceDocument -> Service, VolumeDefDocument -> VolumeDef, and so on
// (NewProject converts them at construction) — rather than the config-file
// document types. So once built, a Project is immutable: nothing the original
// document's owner does to it afterward (pkg/devcontainer overlaying settings,
// deleting services by runServices, ...) is visible through the Project.
// Callers that need to keep editing the parsed data do so on a
// *ProjectDocument directly (LoadDocument/LoadDocumentWithOptions, or a
// literal) and call NewProject only once that editing is done.
type Project struct {
	name      string
	services  map[string]Service
	secrets   map[string]SecretDef
	configs   map[string]ConfigDef
	volumes   map[string]VolumeDef
	networks  map[string]NetworkDef
	egress    *Egress
	ingress   *Ingress
	telemetry *Telemetry
}

// NewProject converts doc's document-typed fields into their internal
// representations (ServiceDocument -> Service, ...) and returns the sealed
// Project. The per-field converters copy the top-level maps, so reassigning
// one of doc's fields afterward cannot leak into the Project; a nested map
// value (e.g. one Service's Environment map) is still shared, so a caller must
// finish building doc before calling NewProject, not after.
func NewProject(doc *ProjectDocument) *Project {
	return &Project{
		name:      doc.Name,
		services:  servicesFromDocuments(doc.Services),
		secrets:   secretDefsFromDocuments(doc.Secrets),
		configs:   configDefsFromDocuments(doc.Configs),
		volumes:   volumeDefsFromDocuments(doc.Volumes),
		networks:  networkDefsFromDocuments(doc.Networks),
		egress:    egressFromDocument(doc.Egress),
		ingress:   ingressFromDocument(doc.Ingress),
		telemetry: telemetryFromDocument(doc.Telemetry),
	}
}

// The XxxFromDocument helpers convert a config-file document type into its
// internal representation. VolumeDef/SecretDef/ConfigDef/NetworkDef/Egress/
// Ingress reference no other split type, so each is a direct struct
// conversion (Go ignores struct tags for convertibility); Service additionally
// narrows its Egress/Ingress fields, so it is copied field by field.

func egressFromDocument(d *EgressDocument) *Egress {
	if d == nil {
		return nil
	}
	e := Egress(*d)
	return &e
}

func ingressFromDocument(d *IngressDocument) *Ingress {
	if d == nil {
		return nil
	}
	in := Ingress(*d)
	return &in
}

func telemetryFromDocument(d *TelemetryDocument) *Telemetry {
	if d == nil {
		return nil
	}
	t := Telemetry(*d)
	return &t
}

func secretDefsFromDocuments(m map[string]SecretDefDocument) map[string]SecretDef {
	if m == nil {
		return nil
	}
	out := make(map[string]SecretDef, len(m))
	for k, v := range m {
		out[k] = SecretDef(v)
	}
	return out
}

func configDefsFromDocuments(m map[string]ConfigDefDocument) map[string]ConfigDef {
	if m == nil {
		return nil
	}
	out := make(map[string]ConfigDef, len(m))
	for k, v := range m {
		out[k] = ConfigDef(v)
	}
	return out
}

func volumeDefsFromDocuments(m map[string]VolumeDefDocument) map[string]VolumeDef {
	if m == nil {
		return nil
	}
	out := make(map[string]VolumeDef, len(m))
	for k, v := range m {
		out[k] = VolumeDef(v)
	}
	return out
}

func networkDefsFromDocuments(m map[string]NetworkDefDocument) map[string]NetworkDef {
	if m == nil {
		return nil
	}
	out := make(map[string]NetworkDef, len(m))
	for k, v := range m {
		out[k] = NetworkDef(v)
	}
	return out
}

func servicesFromDocuments(m map[string]ServiceDocument) map[string]Service {
	if m == nil {
		return nil
	}
	out := make(map[string]Service, len(m))
	for k, v := range m {
		out[k] = serviceFromDocument(v)
	}
	return out
}

// serviceFromDocument copies every field across (the lineup mirrors
// ServiceDocument) and narrows Egress/Ingress to the internal types.
func serviceFromDocument(d ServiceDocument) Service {
	return Service{
		Image:           d.Image,
		Build:           d.Build,
		Entrypoint:      d.Entrypoint,
		Command:         d.Command,
		Environment:     d.Environment,
		EnvFile:         d.EnvFile,
		Ports:           d.Ports,
		Expose:          d.Expose,
		Volumes:         d.Volumes,
		Networks:        d.Networks,
		Restart:         d.Restart,
		DependsOn:       d.DependsOn,
		Deploy:          d.Deploy,
		Healthcheck:     d.Healthcheck,
		ContainerName:   d.ContainerName,
		Privileged:      d.Privileged,
		User:            d.User,
		WorkingDir:      d.WorkingDir,
		Hostname:        d.Hostname,
		Labels:          d.Labels,
		StopSignal:      d.StopSignal,
		StopGracePeriod: d.StopGracePeriod,
		Init:            d.Init,
		TTY:             d.TTY,
		StdinOpen:       d.StdinOpen,
		ReadOnly:        d.ReadOnly,
		CapAdd:          d.CapAdd,
		CapDrop:         d.CapDrop,
		SecurityOpt:     d.SecurityOpt,
		GroupAdd:        d.GroupAdd,
		Sysctls:         d.Sysctls,
		ExtraHosts:      d.ExtraHosts,
		DNS:             d.DNS,
		DNSSearch:       d.DNSSearch,
		DNSOpt:          d.DNSOpt,
		Ulimits:         d.Ulimits,
		Tmpfs:           d.Tmpfs,
		Devices:         d.Devices,
		ShmSize:         d.ShmSize,
		PID:             d.PID,
		IPC:             d.IPC,
		MemLimit:        d.MemLimit,
		CPUs:            d.CPUs,
		Configs:         d.Configs,
		Secrets:         d.Secrets,
		Profiles:        d.Profiles,
		Extends:         d.Extends,
		Egress:          egressFromDocument(d.Egress),
		Ingress:         ingressFromDocument(d.Ingress),
		AgentForward:    d.AgentForward,
		Telemetry:       telemetryFromDocument(d.Telemetry),
	}
}

// Services returns every service in the project, keyed by name.
func (p *Project) Services() map[string]Service {
	return p.services
}

// Volumes returns the project's top-level named volume definitions, keyed by
// name.
func (p *Project) Volumes() map[string]VolumeDef {
	return p.volumes
}

// Name returns the project's `name:` (compose-spec), empty when unset — see
// ResolveName for the default-applying form.
func (p *Project) Name() string {
	return p.name
}

// Secrets returns the project's top-level secret definitions, keyed by name.
func (p *Project) Secrets() map[string]SecretDef {
	return p.secrets
}

// Configs returns the project's top-level config definitions, keyed by name.
func (p *Project) Configs() map[string]ConfigDef {
	return p.configs
}

// Networks returns the project's top-level network definitions, keyed by
// name.
func (p *Project) Networks() map[string]NetworkDef {
	return p.networks
}

// Egress returns the project-level `x-cornus-egress:` default, nil when
// unset.
func (p *Project) Egress() *Egress {
	return p.egress
}

// Ingress returns the project-level `x-cornus-ingress:` defaults, nil when
// unset.
func (p *Project) Ingress() *Ingress {
	return p.ingress
}

// Telemetry returns the project-level `x-cornus-telemetry:` default, nil when
// unset.
func (p *Project) Telemetry() *Telemetry {
	return p.telemetry
}

// ProjectProfileView is a Project narrowed to the services selected by an
// active profile set (compose --profile / COMPOSE_PROFILES), together with
// the complete, unfiltered Project it was derived from. Load/LoadWithOptions
// never filter by profile — they always keep every service — so a caller that
// needs the complete model regardless of the active profile set (e.g.
// `compose ps`, a read-only status query that must report a service no matter
// which profile session deployed it) uses the Project accessor directly
// instead of the filtered Order/Plan/Services.
type ProjectProfileView struct {
	// project is the complete, unfiltered project this view was derived from.
	project *Project
	// services is the profile-selected subset of project.Services.
	services map[string]Service
}

// View computes the profile-filtered view of p for the active profile set. A
// service with no profiles: is always selected; one with profiles: is
// selected only if it shares a profile with profiles. depends_on targets of a
// selected service are pulled in transitively (Compose parity), even when
// profile-gated themselves. p itself is never mutated, so independent View
// calls (e.g. one per active profile set) are safe on the same *Project.
func (p *Project) View(profiles []string) *ProjectProfileView {
	all := p.services
	gated := false
	for _, s := range all {
		if len(s.Profiles) > 0 {
			gated = true
			break
		}
	}
	if !gated {
		return &ProjectProfileView{project: p, services: all}
	}
	active := map[string]bool{}
	for _, pr := range profiles {
		if pr != "" {
			active[pr] = true
		}
	}
	enabled := map[string]bool{}
	for name, s := range all {
		if len(s.Profiles) == 0 {
			enabled[name] = true
			continue
		}
		for _, pr := range s.Profiles {
			if active[pr] {
				enabled[name] = true
				break
			}
		}
	}
	// Transitively pull in dependencies of enabled services. A worklist avoids
	// mutating the map while ranging it.
	queue := make([]string, 0, len(enabled))
	for name := range enabled {
		queue = append(queue, name)
	}
	for len(queue) > 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		s, ok := all[name]
		if !ok {
			continue
		}
		for _, dep := range s.DependsOn.Names() {
			if _, exists := all[dep]; exists && !enabled[dep] {
				enabled[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	services := make(map[string]Service, len(enabled))
	for name := range enabled {
		services[name] = all[name]
	}
	return &ProjectProfileView{project: p, services: services}
}

// Project returns the complete, unfiltered project this view was derived
// from — every service, regardless of the active profile set.
func (v *ProjectProfileView) Project() *Project {
	return v.project
}

// Services returns the profile-selected subset of Project().Services().
func (v *ProjectProfileView) Services() map[string]Service {
	return v.services
}

// selected is the view narrowed to a *Project whose Services are only the
// profile-selected subset (top-level defs shared with the full project). Order
// and Plan run against it so profile-EXCLUDED services are never topo-sorted
// or translated — matching the pre-view behavior where profile filtering
// removed them at load, before any validation. Without this, a malformed or
// cyclic service behind an inactive profile would abort up/down/build even
// though the user never activated it.
func (v *ProjectProfileView) selected() *Project {
	p := v.project
	return &Project{
		name:      p.name,
		services:  v.services,
		secrets:   p.secrets,
		configs:   p.configs,
		volumes:   p.volumes,
		networks:  p.networks,
		egress:    p.egress,
		ingress:   p.ingress,
		telemetry: p.telemetry,
	}
}

// Order returns the view's selected (profile-bound) services in dependency
// order (dependencies first). Excluded services are not part of the sort, so a
// cycle among them cannot fail an up that never touches them.
func (v *ProjectProfileView) Order() ([]string, error) {
	return v.selected().Order()
}

// Plan translates the view's selected (profile-bound) services into
// ServicePlans, keyed by service name. Excluded services are not translated,
// so a malformed one behind an inactive profile cannot fail the command; the
// proxy/usernet topology is likewise computed over the selected set only.
func (v *ProjectProfileView) Plan(projectName string) (map[string]ServicePlan, error) {
	return v.selected().Plan(projectName)
}

// Document rebuilds a ProjectDocument shaped by the view's profile-selected
// subset: the complete project's internal fields converted back to their
// config-file document types, with Services narrowed to v.services. Used where
// a caller needs the config-file-shaped data for the active profile set, such
// as `compose config`'s rendered dump. Include is not reconstructed — it is a
// load-time directive already folded in and cleared.
func (v *ProjectProfileView) Document() *ProjectDocument {
	p := v.project
	return &ProjectDocument{
		Name:      p.name,
		Services:  servicesToDocuments(v.services),
		Secrets:   secretDefsToDocuments(p.secrets),
		Configs:   configDefsToDocuments(p.configs),
		Volumes:   volumeDefsToDocuments(p.volumes),
		Networks:  networkDefsToDocuments(p.networks),
		Egress:    p.egress.toDocument(),
		Ingress:   p.ingress.toDocument(),
		Telemetry: p.telemetry.toDocument(),
	}
}

// The XxxToDocuments helpers and toDocument methods are the inverse of the
// XxxFromDocument converters: they render the internal representation back to
// the config-file document type (used by ProjectProfileView.Document for
// `compose config`). Each mirrors its forward converter.

func (e *Egress) toDocument() *EgressDocument {
	if e == nil {
		return nil
	}
	d := EgressDocument(*e)
	return &d
}

func (in *Ingress) toDocument() *IngressDocument {
	if in == nil {
		return nil
	}
	d := IngressDocument(*in)
	return &d
}

func secretDefsToDocuments(m map[string]SecretDef) map[string]SecretDefDocument {
	if m == nil {
		return nil
	}
	out := make(map[string]SecretDefDocument, len(m))
	for k, v := range m {
		out[k] = SecretDefDocument(v)
	}
	return out
}

func configDefsToDocuments(m map[string]ConfigDef) map[string]ConfigDefDocument {
	if m == nil {
		return nil
	}
	out := make(map[string]ConfigDefDocument, len(m))
	for k, v := range m {
		out[k] = ConfigDefDocument(v)
	}
	return out
}

func volumeDefsToDocuments(m map[string]VolumeDef) map[string]VolumeDefDocument {
	if m == nil {
		return nil
	}
	out := make(map[string]VolumeDefDocument, len(m))
	for k, v := range m {
		out[k] = VolumeDefDocument(v)
	}
	return out
}

func networkDefsToDocuments(m map[string]NetworkDef) map[string]NetworkDefDocument {
	if m == nil {
		return nil
	}
	out := make(map[string]NetworkDefDocument, len(m))
	for k, v := range m {
		out[k] = NetworkDefDocument(v)
	}
	return out
}

func servicesToDocuments(m map[string]Service) map[string]ServiceDocument {
	if m == nil {
		return nil
	}
	out := make(map[string]ServiceDocument, len(m))
	for k, v := range m {
		out[k] = v.toDocument()
	}
	return out
}

// toDocument copies every field across (the lineup mirrors ServiceDocument)
// and widens Egress/Ingress back to the document types.
func (s Service) toDocument() ServiceDocument {
	return ServiceDocument{
		Image:           s.Image,
		Build:           s.Build,
		Entrypoint:      s.Entrypoint,
		Command:         s.Command,
		Environment:     s.Environment,
		EnvFile:         s.EnvFile,
		Ports:           s.Ports,
		Expose:          s.Expose,
		Volumes:         s.Volumes,
		Networks:        s.Networks,
		Restart:         s.Restart,
		DependsOn:       s.DependsOn,
		Deploy:          s.Deploy,
		Healthcheck:     s.Healthcheck,
		ContainerName:   s.ContainerName,
		Privileged:      s.Privileged,
		User:            s.User,
		WorkingDir:      s.WorkingDir,
		Hostname:        s.Hostname,
		Labels:          s.Labels,
		StopSignal:      s.StopSignal,
		StopGracePeriod: s.StopGracePeriod,
		Init:            s.Init,
		TTY:             s.TTY,
		StdinOpen:       s.StdinOpen,
		ReadOnly:        s.ReadOnly,
		CapAdd:          s.CapAdd,
		CapDrop:         s.CapDrop,
		SecurityOpt:     s.SecurityOpt,
		GroupAdd:        s.GroupAdd,
		Sysctls:         s.Sysctls,
		ExtraHosts:      s.ExtraHosts,
		DNS:             s.DNS,
		DNSSearch:       s.DNSSearch,
		DNSOpt:          s.DNSOpt,
		Ulimits:         s.Ulimits,
		Tmpfs:           s.Tmpfs,
		Devices:         s.Devices,
		ShmSize:         s.ShmSize,
		PID:             s.PID,
		IPC:             s.IPC,
		MemLimit:        s.MemLimit,
		CPUs:            s.CPUs,
		Configs:         s.Configs,
		Secrets:         s.Secrets,
		Profiles:        s.Profiles,
		Extends:         s.Extends,
		Egress:          s.Egress.toDocument(),
		Ingress:         s.Ingress.toDocument(),
		AgentForward:    s.AgentForward,
		Telemetry:       s.Telemetry.toDocument(),
	}
}

// ServiceSet is the read surface shared by Project (the complete model) and
// ProjectProfileView (its profile-filtered subset): the service map, the
// dependency order, and the translated deploy plans. A caller that only needs
// "the services to act on" — not whether the source is profile-filtered —
// can work uniformly over either through this interface; see OrderAndPlan.
type ServiceSet interface {
	Services() map[string]Service
	Order() ([]string, error)
	Plan(projectName string) (map[string]ServicePlan, error)
}

var (
	_ ServiceSet = (*Project)(nil)
	_ ServiceSet = (*ProjectProfileView)(nil)
)

// OrderAndPlan resolves both the dependency order and the per-service deploy
// plans for s in one call. composecli uses it for both halves of ps's fix:
// Cmd.load calls it on the profile-bound ProjectProfileView (rt.order/rt.plans,
// what up/down/build act on), and PsCmd.Run calls it on the complete Project
// (every service, regardless of the active profile set).
func OrderAndPlan(s ServiceSet, projectName string) ([]string, map[string]ServicePlan, error) {
	order, err := s.Order()
	if err != nil {
		return nil, nil, err
	}
	plans, err := s.Plan(projectName)
	if err != nil {
		return nil, nil, err
	}
	return order, plans, nil
}

// loadFile parses a single Compose file, resolves its services' `extends`
// directives, and folds in its top-level `include:` models, so each file is
// fully expanded before the multi-file deep-merge and profile filtering in
// LoadWithOptions. Parsing (YAML decode -> ${VAR} interpolation -> typed decode
// -> env_file application) lives in parseFile; extends expansion lives in
// resolveExtends (extends.go); include expansion lives in processInclude
// (include.go).
func loadFile(f string, envFiles []string, warn func(service, field string)) (*ProjectDocument, error) {
	p, err := parseFile(f, envFiles, warn)
	if err != nil {
		return nil, err
	}
	if err := resolveExtends(p, f, envFiles, warn); err != nil {
		return nil, err
	}
	// Fold in any top-level `include:` models (each fully expands its own
	// interpolation, extends, and nested includes; the including file wins on
	// conflict) before the multi-file merge in LoadWithOptions. See include.go.
	if err := processInclude(p, f, envFiles, warn); err != nil {
		return nil, err
	}
	return p, nil
}

// parseFile parses a single Compose file: YAML decode -> ${VAR} interpolation ->
// typed decode -> env_file application. It does NOT resolve `extends` (that is
// loadFile's job, via resolveExtends), so services returned here still carry
// their raw Extends directive.
func parseFile(f string, envFiles []string, warn func(service, field string)) (*ProjectDocument, error) {
	data, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(f)

	mapping, err := envMapping(dir, envFiles)
	if err != nil {
		return nil, fmt.Errorf("compose: %s: %w", f, err)
	}

	// Decode to a generic structure, interpolate string values, then decode into
	// the typed ProjectDocument via JSON (so the custom field unmarshalers still
	// run).
	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("compose: parse %s: %w", f, err)
	}
	generic, err = interpolate(generic, mapping)
	if err != nil {
		return nil, fmt.Errorf("compose: %s: %w", f, err)
	}
	jb, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	var p ProjectDocument
	if err := json.Unmarshal(jb, &p); err != nil {
		return nil, fmt.Errorf("compose: %s: %w", f, err)
	}

	if warn != nil {
		warnUnsupportedFields(generic, warn)
	}

	for name, svc := range p.Services {
		if err := applyEnvFiles(&svc, dir); err != nil {
			return nil, fmt.Errorf("compose: %s: service %q: %w", f, name, err)
		}
		p.Services[name] = svc
	}
	return &p, nil
}

// applyEnvFiles loads a service's env_file entries (relative to dir) and merges
// them into its environment. Inline environment: values take precedence.
func applyEnvFiles(svc *ServiceDocument, dir string) error {
	if len(svc.EnvFile) == 0 {
		return nil
	}
	merged := Environment{}
	for _, ref := range svc.EnvFile {
		path := ref.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) && !ref.Required {
				continue
			}
			return err
		}
		vars, err := parseEnvBytes(b)
		if err != nil {
			return fmt.Errorf("env_file %s: %w", ref.Path, err)
		}
		for k, v := range vars {
			merged[k] = v
		}
	}
	for k, v := range svc.Environment {
		merged[k] = v // inline environment wins
	}
	svc.Environment = merged
	return nil
}

// ResolveName returns the project name, defaulting to the sanitized base name of
// dir (matching how Docker Compose derives a default project name).
func (p *Project) ResolveName(dir string) string {
	if p.name != "" {
		return sanitizeName(p.name)
	}
	abs, err := filepath.Abs(dir)
	if err != nil || abs == "" {
		return "cornus"
	}
	return sanitizeName(filepath.Base(abs))
}

// Order returns service names in dependency order (dependencies first). It is a
// stable topological sort; independent services keep alphabetical order.
func (p *Project) Order() ([]string, error) {
	svcs := p.services
	names := make([]string, 0, len(svcs))
	for n := range svcs {
		names = append(names, n)
	}
	sort.Strings(names)

	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := make(map[string]int, len(names))
	var order []string
	var visit func(n string, stack []string) error
	visit = func(n string, stack []string) error {
		switch state[n] {
		case done:
			return nil
		case active:
			return fmt.Errorf("compose: dependency cycle: %s", strings.Join(append(stack, n), " -> "))
		}
		state[n] = active
		deps := svcs[n].DependsOn.Names()
		sort.Strings(deps)
		for _, dep := range deps {
			if _, ok := svcs[dep]; !ok {
				continue // unknown dependency: ignore
			}
			if err := visit(dep, append(stack, n)); err != nil {
				return err
			}
		}
		state[n] = done
		order = append(order, n)
		return nil
	}
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Plan translates every service into a ServicePlan, keyed by service name.
func (p *Project) Plan(projectName string) (map[string]ServicePlan, error) {
	plans := make(map[string]ServicePlan, len(p.services))
	for name, svc := range p.services {
		plan, err := translateService(projectName, name, svc, p.secrets, p.configs, p.volumes, p.networks)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		plans[name] = plan
	}
	// Project-level egress default: any service that declared no `egress:` of its
	// own inherits a fresh copy (a service block fully overrides — no field merge).
	// Validate the default once up front so a malformed one fails fast even when
	// every service overrides it.
	if p.egress != nil {
		if _, err := translateEgress(p.egress); err != nil {
			return nil, fmt.Errorf("project egress: %w", err)
		}
		for name, plan := range plans {
			if plan.Spec.Egress != nil {
				continue
			}
			es, _ := translateEgress(p.egress) // already validated above
			plan.Spec.Egress = es
			plans[name] = plan
		}
	}
	// Project-level telemetry default: any service that declared no
	// `x-cornus-telemetry:` of its own inherits a fresh copy (a service block fully
	// overrides — no field merge), so a whole project ships telemetry to one OTLP
	// backend with a single block. Validate the default once up front.
	if p.telemetry != nil {
		if _, err := translateTelemetry(p.telemetry); err != nil {
			return nil, fmt.Errorf("project telemetry: %w", err)
		}
		for name, plan := range plans {
			if plan.Spec.Telemetry != nil {
				continue
			}
			ts, _ := translateTelemetry(p.telemetry) // already validated above
			plan.Spec.Telemetry = ts
			plans[name] = plan
		}
	}
	// Project-level ingress defaults: unlike egress, this does NOT enable ingress —
	// it FIELD-merges the project domain / class / tls-issuer into each service that
	// already opted in (a service value wins). Anything still unset falls back to the
	// server defaults at deploy time.
	if p.ingress != nil {
		def, err := translateIngress(p.ingress)
		if err != nil {
			return nil, fmt.Errorf("project ingress: %w", err)
		}
		for name, plan := range plans {
			if plan.Spec.Ingress == nil {
				continue // ingress is opt-in per service
			}
			applyIngressDefaults(plan.Spec.Ingress, def)
			plans[name] = plan
		}
	}
	p.applyProxyPolicy(plans)
	// After the proxy control plane (a proxied service must not also get the DNS
	// caretaker): pin deterministic per-service IPs on Multus-realised networks
	// and derive the peer DNS records that make named traffic ride them.
	if err := applyUserNetIPs(plans); err != nil {
		return nil, err
	}
	return plans, nil
}

// serviceResourceName is the deployment resource name for a service:
// "<project>-<service>". Shared by translateService and PlanForStatus so the
// two never drift.
func serviceResourceName(projectName, name string) string {
	return projectName + "-" + name
}

// PlanForStatus is a read-only, failure-tolerant variant of Plan for status
// queries (compose ps): it returns every service in dependency order together
// with a best-effort ServicePlan, and never aborts on a bad service. A service
// that fails to translate gets a minimal plan (its resource name and spec
// image, which need no translation) so ps can still list it, and its error is
// collected in errs for optional reporting. A dependency cycle (which would
// fail Order) degrades to sorted-name order rather than erroring. The
// cross-service passes Plan runs (project egress/ingress defaults, proxy
// policy, user-net IPs) are skipped — they do not affect what ps displays and
// are exactly the passes a malformed project is most likely to trip on.
func (p *Project) PlanForStatus(projectName string) (order []string, plans map[string]ServicePlan, errs map[string]error) {
	order, err := p.Order()
	if err != nil {
		// A cycle can't be topo-sorted; fall back to a stable display order so
		// ps still lists every service.
		order = make([]string, 0, len(p.services))
		for name := range p.services {
			order = append(order, name)
		}
		sort.Strings(order)
	}
	plans = make(map[string]ServicePlan, len(p.services))
	for name, svc := range p.services {
		plan, e := translateService(projectName, name, svc, p.secrets, p.configs, p.volumes, p.networks)
		if e != nil {
			if errs == nil {
				errs = map[string]error{}
			}
			errs[name] = e
			// Minimal fallback: resource name + image need no translation, so the
			// row still renders (its status comes from the live backend list).
			resource := serviceResourceName(projectName, name)
			plan = ServicePlan{Service: name, Resource: resource, Spec: api.DeploySpec{Name: resource, Image: svc.Image}}
		}
		plans[name] = plan
	}
	return order, plans, errs
}

// applyProxyPolicy fills each service's ProxySpec.Allow with the peers it may
// reach. A service is proxied when ANY of its networks sets
// `driver_opts: {proxy: "true"}`; once proxied, ALL its egress is enforced and
// its allow-set is every OTHER service sharing ANY of its networks (so it can
// still reach every legitimate peer, and nothing else). The control plane lives
// here — at plan time, where the whole topology is known — not in the per-service
// backend Apply. The allow entries are bare service names, which resolve to each
// peer's headless Service (the `services` provider) inside the pod's namespace.
func (p *Project) applyProxyPolicy(plans map[string]ServicePlan) {
	// service -> set of proxy-enabled network names it joins, and the union of
	// its co-members across ALL its networks.
	proxied := map[string]bool{}
	mode := map[string]string{} // service -> proxy mode ("" => enforcing)
	peers := map[string]map[string]bool{}
	// network name -> members (compose service names).
	members := map[string]map[string]bool{}
	svcNets := map[string][]string{}
	for name, svc := range p.services {
		attach := svc.Networks
		if len(attach) == 0 {
			attach = ServiceNetworks{{Name: "default"}}
		}
		for _, sn := range attach {
			if members[sn.Name] == nil {
				members[sn.Name] = map[string]bool{}
			}
			members[sn.Name][name] = true
			svcNets[name] = append(svcNets[name], sn.Name)
			if def, ok := p.networks[sn.Name]; ok && def.DriverOpts["proxy"] == "true" {
				proxied[name] = true
				if m := def.DriverOpts["mode"]; m != "" {
					mode[name] = m
				}
			}
		}
	}
	for name := range p.services {
		if !proxied[name] {
			continue
		}
		peers[name] = map[string]bool{}
		for _, n := range svcNets[name] {
			for m := range members[n] {
				if m != name {
					peers[name][m] = true
				}
			}
		}
		allow := make([]string, 0, len(peers[name]))
		for m := range peers[name] {
			allow = append(allow, m)
		}
		sort.Strings(allow)
		ps := &api.ProxySpec{Mode: mode[name], Allow: allow}
		// Cooperative mode intercepts by DNS + per-port loopback listeners, so it
		// must know each peer's container ports up front (it can only listen on
		// ports it is told about). Enforcing mode captures by IP and needs none.
		if mode[name] == "cooperative" {
			ports := map[string][]int{}
			for _, m := range allow {
				if pp := serviceContainerPorts(p.services[m]); len(pp) > 0 {
					ports[m] = pp
				}
			}
			if len(ports) > 0 {
				ps.Ports = ports
			}
		}
		plan := plans[name]
		plan.Spec.Proxy = ps
		plans[name] = plan
	}
}

// serviceContainerPorts returns the deduped, sorted container ports a service
// advertises to peers — the union of its published `ports:` (container side)
// and `expose:` list. Used by the cooperative proxy to decide which loopback
// ports to listen on.
func serviceContainerPorts(svc Service) []int {
	seen := map[int]bool{}
	for _, p := range svc.Ports {
		if p.Container > 0 {
			seen[p.Container] = true
		}
	}
	for _, p := range svc.Expose {
		if p > 0 {
			seen[p] = true
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// volumeResourceName resolves a service-referenced named volume to its backing
// resource name using the top-level `volumes:` definitions: an explicit `name:`
// wins, an `external: true` volume keeps its literal name (cornus neither
// scopes nor provisions it), and everything else is project-scoped as Compose
// does (`<project>_<volume>`).
func volumeResourceName(projectName, source string, defs map[string]VolumeDef) string {
	if def, ok := defs[source]; ok {
		switch {
		case def.Name != "":
			return def.Name
		case def.External:
			return source
		}
	}
	return projectName + "_" + source
}

// VolumeResourceName resolves a top-level volume to its backing resource name —
// the identifier the deploy backend provisions, matching how a service
// references it. Exported for `compose down --volumes`, which must target
// exactly the volumes the project created (an external volume keeps its literal
// name, which the caller skips since cornus never provisioned it).
func VolumeResourceName(projectName, source string, defs map[string]VolumeDef) string {
	return volumeResourceName(projectName, source, defs)
}

// networkResourceName resolves a service-referenced network to its backing
// resource name using the top-level `networks:` definitions, mirroring
// volumeResourceName: an explicit `name:` wins, an `external: true` network
// keeps its literal name (cornus neither scopes nor provisions it), and
// everything else is project-scoped as Compose does (`<project>_<network>`).
func networkResourceName(projectName, source string, defs map[string]NetworkDef) string {
	if def, ok := defs[source]; ok {
		switch {
		case def.Name != "":
			return def.Name
		case def.External:
			return source
		}
	}
	return projectName + "_" + source
}

// networkAliases builds an attachment's DNS names: the service name first
// (Compose's implicit alias), then container_name and any declared aliases,
// deduplicated preserving order.
func networkAliases(service, containerName string, declared []string) []string {
	out := []string{service}
	seen := map[string]bool{service: true}
	for _, a := range append([]string{containerName}, declared...) {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// translateEgress converts a compose `egress:` block (service- or project-level)
// into an api.EgressSpec and validates it. Each call returns a fresh spec so an
// inherited project default is not aliased across services.
func translateEgress(e *Egress) (*api.EgressSpec, error) {
	es := &api.EgressSpec{
		Mode:       e.Mode,
		Gateway:    e.Gateway,
		Proxies:    e.Proxies,
		Script:     e.Script,
		Default:    e.Default,
		ListenPort: e.ListenPort,
	}
	for _, r := range e.Rules {
		es.Rules = append(es.Rules, api.EgressRule{Pattern: r.Pattern, Route: r.Route})
	}
	if err := es.Validate(); err != nil {
		return nil, err
	}
	return es, nil
}

// translateTelemetry converts a compose `x-cornus-telemetry:` block into an
// api.TelemetrySpec and validates it. The block's mere presence enables telemetry
// (Enabled is always set); a nil block leaves telemetry off.
func translateTelemetry(t *Telemetry) (*api.TelemetrySpec, error) {
	if t == nil {
		return nil, nil
	}
	ts := &api.TelemetrySpec{
		Enabled:            true,
		Endpoint:           t.Endpoint,
		Protocol:           t.Protocol,
		Headers:            t.Headers,
		Insecure:           t.Insecure,
		Signals:            t.Signals,
		ServiceName:        t.ServiceName,
		ResourceAttributes: t.ResourceAttributes,
		GRPCPort:           t.GRPCPort,
		HTTPPort:           t.HTTPPort,
		Debug:              t.Debug,
	}
	if err := ts.Validate(); err != nil {
		return nil, err
	}
	return ts, nil
}

// translateIngress converts a compose `x-cornus-ingress:` block into an
// api.IngressSpec and validates it. The block's mere presence enables ingress, so
// Enabled is always set (the bare `x-cornus-ingress: {}` / `true` form maps to an
// all-defaults spec whose host is auto-derived server-side).
func translateIngress(in *Ingress) (*api.IngressSpec, error) {
	// host scalar sugar is unioned with the hosts list (host first).
	var hosts []string
	if in.Host != "" {
		hosts = append(hosts, in.Host)
	}
	hosts = append(hosts, in.Hosts...)
	is := &api.IngressSpec{
		Enabled:     true,
		Hosts:       hosts,
		Domain:      in.Domain,
		Subdomain:   in.Subdomain,
		Path:        in.Path,
		PathType:    in.PathType,
		Port:        in.Port,
		ClassName:   in.ClassName,
		Annotations: in.Annotations,
	}
	if in.TLS != nil {
		is.TLS = &api.IngressTLS{
			SecretName:    in.TLS.SecretName,
			ClusterIssuer: in.TLS.ClusterIssuer,
		}
	}
	if err := is.Validate(); err != nil {
		return nil, err
	}
	return is, nil
}

// applyIngressDefaults fills a service ingress spec's unset infra fields (domain,
// class, tls-issuer) from a project-level default. The service value always wins;
// only empty fields inherit. It never enables ingress and never touches per-service
// intent (host, path, port) — those have no project-level meaning.
func applyIngressDefaults(svc, def *api.IngressSpec) {
	if svc.Domain == "" {
		svc.Domain = def.Domain
	}
	if svc.ClassName == "" {
		svc.ClassName = def.ClassName
	}
	if def.TLS != nil {
		if svc.TLS == nil {
			// The project asked for TLS; an opted-in service with no tls block of its
			// own inherits it (a fresh copy so services do not alias one struct).
			svc.TLS = &api.IngressTLS{SecretName: def.TLS.SecretName, ClusterIssuer: def.TLS.ClusterIssuer}
		} else {
			if svc.TLS.SecretName == "" {
				svc.TLS.SecretName = def.TLS.SecretName
			}
			if svc.TLS.ClusterIssuer == "" {
				svc.TLS.ClusterIssuer = def.TLS.ClusterIssuer
			}
		}
	}
}

func translateService(projectName, name string, svc Service, secrets map[string]SecretDef, configs map[string]ConfigDef, volumes map[string]VolumeDef, networks map[string]NetworkDef) (ServicePlan, error) {
	resource := serviceResourceName(projectName, name)

	spec := api.DeploySpec{
		Name:       resource,
		Image:      svc.Image,
		Entrypoint: []string(svc.Entrypoint),
		Command:    []string(svc.Command),
		Env:        map[string]string(svc.Environment),
	}
	// The compose short form carries the max-retry count inline (`on-failure:N`);
	// api.DeploySpec keeps the policy word and the count in separate fields, so
	// split it here. deploy.restart_policy below overrides both when present.
	spec.Restart, spec.RestartMaxAttempts = svc.Restart.split()
	spec.Privileged = svc.Privileged
	spec.User = svc.User
	spec.WorkingDir = svc.WorkingDir
	spec.Hostname = svc.Hostname
	// Labels: the container-level `labels:` and the swarm SERVICE-level
	// `deploy.labels:` both land in the single api.DeploySpec.Labels map (cornus
	// does not distinguish the two scopes). deploy.labels win on a key clash,
	// matching their more-specific intent.
	if labels := mergeDeployLabels(svc); len(labels) > 0 {
		spec.Labels = labels
	}
	spec.StopSignal = svc.StopSignal
	spec.StopGracePeriod = svc.StopGracePeriod
	spec.Init = svc.Init
	spec.TTY = svc.TTY
	spec.StdinOpen = svc.StdinOpen
	spec.ReadOnly = svc.ReadOnly
	// Security & networking keys (compose-spec 05-services.md) -> api.DeploySpec.
	// Note the compose `dns:` maps to DNSServers (NOT the unrelated caretaker
	// DNS *DNSSpec field). Empty slices stay nil so omitempty keeps them off.
	spec.CapAdd = svc.CapAdd
	spec.CapDrop = svc.CapDrop
	spec.SecurityOpt = svc.SecurityOpt
	spec.GroupAdd = svc.GroupAdd
	if len(svc.Sysctls) > 0 {
		spec.Sysctls = map[string]string(svc.Sysctls)
	}
	spec.ExtraHosts = []string(svc.ExtraHosts)
	spec.DNSServers = []string(svc.DNS)
	spec.DNSSearch = []string(svc.DNSSearch)
	spec.DNSOptions = []string(svc.DNSOpt)
	// Resource & host-namespace keys (compose-spec 05-services.md).
	spec.Ulimits = translateUlimits(svc.Ulimits)
	spec.Tmpfs = []string(svc.Tmpfs)
	spec.Devices = []string(svc.Devices)
	shm, err := parseSize(string(svc.ShmSize))
	if err != nil {
		return ServicePlan{}, fmt.Errorf("shm_size: %w", err)
	}
	spec.ShmSize = shm
	spec.PIDMode = svc.PID
	spec.IPCMode = svc.IPC
	if svc.Deploy != nil {
		spec.Replicas = svc.Deploy.Replicas
		// deploy.restart_policy.condition is authoritative over the service-level
		// `restart:` (compose-spec): map it onto spec.Restart, overriding what the
		// struct literal set from svc.Restart. max_attempts rides along for the
		// dockerhost backend; delay/window are swarm-only timing with no backend
		// equivalent and are dropped.
		if rp := svc.Deploy.RestartPolicy; rp != nil {
			if cond := mapRestartCondition(rp.Condition); cond != "" {
				spec.Restart = cond
			}
			spec.RestartMaxAttempts = rp.MaxAttempts
		}
		// deploy.update_config -> the (kubernetes-only) rolling-update strategy.
		if uc := svc.Deploy.UpdateConfig; uc != nil {
			spec.UpdateConfig = &api.UpdateConfig{Parallelism: uc.Parallelism, Order: uc.Order}
		}
	}
	// Resource limits: deploy.resources.limits is authoritative (compose-spec), and
	// the non-deploy mem_limit/cpus fill any axis it leaves unset. Both feed the
	// SAME api.Resources so every backend honouring Resources honours all four keys.
	res, err := serviceResources(svc)
	if err != nil {
		return ServicePlan{}, err
	}
	spec.Resources = res
	if hc := svc.Healthcheck; hc != nil {
		if test := hc.DockerTest(); len(test) > 0 {
			spec.Healthcheck = &api.Healthcheck{
				Test:          test,
				Interval:      hc.Interval,
				Timeout:       hc.Timeout,
				StartPeriod:   hc.StartPeriod,
				StartInterval: hc.StartInterval,
				Retries:       hc.Retries,
			}
		}
	}

	for _, port := range svc.Ports {
		spec.Ports = append(spec.Ports, api.PortMapping{
			Host:      port.Host,
			Container: port.Container,
			Protocol:  port.Protocol,
			HostIP:    port.HostIP,
		})
	}
	for _, vol := range svc.Volumes {
		switch {
		case vol.Source == "" && vol.Target != "":
			// Anonymous volume (compose short form "/data"): no source, only a
			// container target. Realised as a managed volume (a dynamically-
			// provisioned PVC on kubernetes, a Docker anonymous volume on
			// dockerhost). The short form carries no size/class, so leave the
			// backend defaults.
			spec.Volumes = append(spec.Volumes, api.VolumeSpec{
				Target:   vol.Target,
				ReadOnly: vol.ReadOnly,
			})
		case vol.Named:
			// Named volume (e.g. "cache:/var/cache"): a shared, project-scoped store
			// that persists across a single service's delete. Realised as a
			// persistent PVC on kubernetes / a Docker named volume on dockerhost.
			// The top-level `volumes:` def (when present) carries the driver /
			// driver_opts / labels onto the managed volume; dockerhost creates the
			// volume with them, k8s/containerd realise a subset.
			vs := api.VolumeSpec{
				Name:     volumeResourceName(projectName, vol.Source, volumes),
				Target:   vol.Target,
				ReadOnly: vol.ReadOnly,
			}
			if def, ok := volumes[vol.Source]; ok && !def.External {
				vs.Driver = def.Driver
				vs.DriverOpts = def.DriverOpts
				vs.Labels = def.Labels
			}
			spec.Volumes = append(spec.Volumes, vs)
		case vol.Source != "":
			spec.Mounts = append(spec.Mounts, api.Mount{
				Source:   vol.Source,
				Target:   vol.Target,
				ReadOnly: vol.ReadOnly,
				SELinux:  vol.SELinux,
			})
		}
	}

	// Service-level configs/secrets grants become read-only bind mounts of their
	// top-level file-based definitions, appended in declaration order (configs
	// then secrets) for a deterministic mount list. Source paths are set verbatim
	// and absolutized later by ServicePlan.ResolveMounts, exactly like bind
	// volumes. LIMITATIONS: only file-based defs are realised — inline `content:`,
	// `environment:`-backed, and `external: true` defs have no host file to bind
	// and are skipped with a warning; a grant's uid/gid/mode cannot be applied to
	// a bind mount and is ignored.
	cfgMounts, err := grantMounts(name, "config", "/", configGrantRefs(svc.Configs), func(s string) (string, bool) {
		def, ok := configs[s]
		return def.File, ok
	})
	if err != nil {
		return ServicePlan{}, err
	}
	spec.Mounts = append(spec.Mounts, cfgMounts...)
	secMounts, err := grantMounts(name, "secret", "/run/secrets/", secretGrantRefs(svc.Secrets), func(s string) (string, bool) {
		def, ok := secrets[s]
		return def.File, ok
	})
	if err != nil {
		return ServicePlan{}, err
	}
	spec.Mounts = append(spec.Mounts, secMounts...)

	// Network attachments. A service with no `networks:` joins the project's
	// implicit default network, matching Compose — so `web` can reach `db` by
	// name in the common no-networks-block case. Every attachment carries the
	// service name (plus container_name and declared aliases) as its DNS names.
	attach := svc.Networks
	if len(attach) == 0 {
		attach = ServiceNetworks{{Name: "default"}}
	}
	for _, sn := range attach {
		na := api.NetworkAttachment{
			Name:    networkResourceName(projectName, sn.Name, networks),
			Aliases: networkAliases(name, svc.ContainerName, sn.Aliases),
			// An explicit `ipv4_address` pin rides along as a plain IP;
			// applyUserNetIPs validates it against the network's subnet and
			// normalises it to CIDR form on Multus-realised networks.
			IP: sn.IPv4Address,
			// Service long-syntax endpoint settings (compose 05-services.md).
			IPv6:     sn.IPv6Address,
			MAC:      sn.MacAddress,
			Priority: sn.Priority,
		}
		if def, ok := networks[sn.Name]; ok {
			na.Driver = def.Driver
			na.DriverOpts = def.DriverOpts
			// Top-level network def (compose 06-networks.md): IPAM config, the
			// attachable/internal/enable_ipv6 toggles, and labels. Only the first
			// ipam.config entry is realised on single-subnet backends.
			if def.IPAM != nil && len(def.IPAM.Config) > 0 {
				c := def.IPAM.Config[0]
				na.Subnet, na.Gateway, na.IPRange = c.Subnet, c.Gateway, c.IPRange
			}
			na.Attachable = def.Attachable
			na.Internal = def.Internal
			na.EnableIPv6 = def.EnableIPv6
			na.Labels = def.Labels
		}
		spec.Networks = append(spec.Networks, na)
	}

	if svc.Egress != nil {
		es, err := translateEgress(svc.Egress)
		if err != nil {
			return ServicePlan{}, err
		}
		spec.Egress = es
	}

	if svc.Ingress != nil {
		in, err := translateIngress(svc.Ingress)
		if err != nil {
			return ServicePlan{}, err
		}
		// Namespace the auto-derived host per project: when no explicit host and no
		// subdomain override is given, derive "<service>.<project>" so two projects
		// deploying the same service get distinct hostnames under the base domain
		// (the backend appends ".<domain>"). The resource name is "<project>-<service>",
		// which flattens the two into one ambiguous label — the dotted form does not.
		if len(in.Hosts) == 0 && in.Subdomain == "" {
			in.Subdomain = name + "." + projectName
		}
		spec.Ingress = in
	}
	spec.AgentForward = svc.AgentForward
	ts, err := translateTelemetry(svc.Telemetry)
	if err != nil {
		return ServicePlan{}, fmt.Errorf("service %q: %w", name, err)
	}
	spec.Telemetry = ts

	plan := ServicePlan{Service: name, Resource: resource, Spec: spec}
	if svc.Build != nil {
		buildShm, err := parseSize(string(svc.Build.ShmSize))
		if err != nil {
			return ServicePlan{}, fmt.Errorf("build.shm_size: %w", err)
		}
		bp := &BuildPlan{
			Context:            svc.Build.Context,
			Dockerfile:         svc.Build.Dockerfile,
			Args:               svc.Build.Args,
			Target:             svc.Build.Target,
			CacheFrom:          svc.Build.CacheFrom,
			Image:              svc.Image,
			AdditionalContexts: svc.Build.AdditionalContexts,
			SSH:                svc.Build.SSH,
			Labels:             svc.Build.Labels,
			NoCache:            svc.Build.NoCache,
			Pull:               svc.Build.Pull,
			Platforms:          svc.Build.Platforms,
			Tags:               svc.Build.Tags,
			Network:            svc.Build.Network,
			CacheTo:            svc.Build.CacheTo,
			ExtraHosts:         []string(svc.Build.ExtraHosts),
			ShmSize:            buildShm,
			DockerfileInline:   svc.Build.DockerfileInline,
		}
		// Resolve referenced build secrets to their top-level file paths.
		// File-based secrets only; references without a file: are skipped. The
		// build-secret transport carries only the id, so a long-form grant's
		// target/uid/gid/mode cannot be honored yet — warn once when any is set.
		for _, sec := range svc.Build.Secrets {
			if sec.Target != "" || sec.UID != "" || sec.GID != "" || sec.Mode != "" {
				warnOnce("build secret target/uid/gid/mode are not yet honored; only the secret id is forwarded to the build")
			}
			def, ok := secrets[sec.Source]
			if !ok || def.File == "" {
				continue
			}
			if bp.Secrets == nil {
				bp.Secrets = map[string]string{}
			}
			bp.Secrets[sec.Source] = def.File
		}
		plan.Build = bp
	}
	return plan, nil
}

// configGrantRefs / secretGrantRefs normalise the typed service-level grant
// slices into the shared grantRef shape grantMounts consumes. ConfigRef and
// SecretRef are field-identical to grantRef, so the conversion is a direct cast.
func configGrantRefs(refs ConfigRefs) []grantRef {
	out := make([]grantRef, len(refs))
	for i, r := range refs {
		out[i] = grantRef(r)
	}
	return out
}

func secretGrantRefs(refs SecretRefs) []grantRef {
	out := make([]grantRef, len(refs))
	for i, r := range refs {
		out[i] = grantRef(r)
	}
	return out
}

// grantMounts translates a service's config/secret grant refs into read-only
// bind mounts. fileOf maps a source name to its top-level def's File and whether
// the source is defined at all: an undefined source is a hard error, while a
// defined-but-not-file-based source (empty File — i.e. content/environment/
// external) is skipped with a one-time warning since a bind mount needs a host
// file. kind ("config"/"secret") tags messages; targetPrefix is the container
// path prefix for a bare or relative target ("/" for configs — Compose's Linux
// default /<config_name> — and "/run/secrets/" for secrets). An absolute
// long-form target is used verbatim. A grant's uid/gid/mode cannot be applied to
// a bind mount and triggers a one-time warning.
func grantMounts(service, kind, targetPrefix string, refs []grantRef, fileOf func(source string) (file string, defined bool)) ([]api.Mount, error) {
	var out []api.Mount
	for _, ref := range refs {
		file, defined := fileOf(ref.Source)
		if !defined {
			return nil, fmt.Errorf("%s %q referenced by service %q is not defined at the top level", kind, ref.Source, service)
		}
		if file == "" {
			warnOnce(fmt.Sprintf("%s %q: only file-based %ss are supported; content/environment/external ignored", kind, ref.Source, kind))
			continue
		}
		if ref.UID != "" || ref.GID != "" || ref.Mode != "" {
			warnOnce(fmt.Sprintf("%s uid/gid/mode are not applied; %ss are realised as read-only bind mounts", kind, kind))
		}
		target := ref.Target
		switch {
		case target == "":
			target = targetPrefix + ref.Source
		case filepath.IsAbs(target):
			// Absolute long-form target: honour it verbatim.
		default:
			target = targetPrefix + target
		}
		out = append(out, api.Mount{Source: file, Target: target, ReadOnly: true})
	}
	return out, nil
}

// warnOnce logs a warning for a given message a single time per process,
// deduplicating repeated config/secret limitation warnings across services.
var (
	warnOnceMu   sync.Mutex
	warnOnceSeen = map[string]struct{}{}
)

func warnOnce(msg string) {
	warnOnceMu.Lock()
	defer warnOnceMu.Unlock()
	if _, ok := warnOnceSeen[msg]; ok {
		return
	}
	warnOnceSeen[msg] = struct{}{}
	// Plan-time helper with no request context; log against the process default.
	ctx := context.Background()
	logging.FromContext(ctx).WarnContext(ctx, msg, slog.String("component", "compose"))
}

// deployLimits returns a service's deploy.resources.limits, or nil when unset.
func deployLimits(d *Deploy) *ResourceLimits {
	if d == nil || d.Resources == nil {
		return nil
	}
	return d.Resources.Limits
}

// deployReservations returns a service's deploy.resources.reservations, or nil
// when unset.
func deployReservations(d *Deploy) *ResourceLimits {
	if d == nil || d.Resources == nil {
		return nil
	}
	return d.Resources.Reservations
}

// mapRestartCondition maps a compose deploy.restart_policy.condition to a Docker
// restart policy word: none->"no", on-failure->"on-failure", any->"always". An
// empty or unrecognised condition yields "" so the caller keeps the
// service-level `restart:` value.
func mapRestartCondition(c string) string {
	switch strings.TrimSpace(c) {
	case "none":
		return "no"
	case "on-failure":
		return "on-failure"
	case "any":
		return "always"
	}
	return ""
}

// mergeDeployLabels combines the container-level `labels:` and the swarm
// service-level `deploy.labels:` into one map. deploy.labels win on a key clash.
// Returns nil when neither is set, so an unset spec keeps no Labels.
func mergeDeployLabels(svc Service) map[string]string {
	var dl Labels
	if svc.Deploy != nil {
		dl = svc.Deploy.Labels
	}
	if len(svc.Labels) == 0 && len(dl) == 0 {
		return nil
	}
	out := make(map[string]string, len(svc.Labels)+len(dl))
	for k, v := range svc.Labels {
		out[k] = v
	}
	for k, v := range dl {
		out[k] = v
	}
	return out
}

// serviceResources maps a service's CPU/memory limits into api.Resources. It
// unifies the two compose sources for each axis: deploy.resources.limits (the
// swarm form) is AUTHORITATIVE per compose-spec, and the non-deploy mem_limit /
// cpus keys fill an axis deploy leaves unset. Routing both into the single
// api.Resources makes mem_limit/cpus real on every backend that already honours
// Resources (dockerhost + kubernetes) with no backend change. It returns nil
// when no limit is set on either axis.
func serviceResources(svc Service) (*api.Resources, error) {
	var cpu float64
	var mem int64
	var err error
	if lim := deployLimits(svc.Deploy); lim != nil {
		if cpu, err = parseCPUs(string(lim.Cpus)); err != nil {
			return nil, fmt.Errorf("deploy.resources.limits.cpus: %w", err)
		}
		if mem, err = parseSize(string(lim.Memory)); err != nil {
			return nil, fmt.Errorf("deploy.resources.limits.memory: %w", err)
		}
	}
	if cpu == 0 {
		if cpu, err = parseCPUs(string(svc.CPUs)); err != nil {
			return nil, fmt.Errorf("cpus: %w", err)
		}
	}
	if mem == 0 {
		if mem, err = parseSize(string(svc.MemLimit)); err != nil {
			return nil, fmt.Errorf("mem_limit: %w", err)
		}
	}
	// deploy.resources.reservations -> the request/floor axes. Reservations have
	// no non-deploy compose equivalent, so they come solely from deploy.
	var rcpu float64
	var rmem int64
	if res := deployReservations(svc.Deploy); res != nil {
		if rcpu, err = parseCPUs(string(res.Cpus)); err != nil {
			return nil, fmt.Errorf("deploy.resources.reservations.cpus: %w", err)
		}
		if rmem, err = parseSize(string(res.Memory)); err != nil {
			return nil, fmt.Errorf("deploy.resources.reservations.memory: %w", err)
		}
	}
	if cpu == 0 && mem == 0 && rcpu == 0 && rmem == 0 {
		return nil, nil
	}
	return &api.Resources{CPULimit: cpu, MemoryLimit: mem, ReservedCPU: rcpu, ReservedMemory: rmem}, nil
}

// translateUlimits converts compose ulimits into the api.Ulimit slice, preserving
// the loader's deterministic (name-sorted) order.
func translateUlimits(ul Ulimits) []api.Ulimit {
	if len(ul) == 0 {
		return nil
	}
	out := make([]api.Ulimit, len(ul))
	for i, u := range ul {
		out[i] = api.Ulimit{Name: u.Name, Soft: u.Soft, Hard: u.Hard}
	}
	return out
}

// sanitizeName lowercases and strips a name to the characters Docker Compose
// allows in project names.
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "cornus"
	}
	return out
}
