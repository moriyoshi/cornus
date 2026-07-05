package devcontainer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cornus/pkg/compose"
)

// singleServiceName is the synthetic service name for a single-container
// devcontainer (image / build flavor). Deployments are named
// "<project>-devcontainer".
const singleServiceName = "devcontainer"

// keepAlive is the command used when overrideCommand keeps the container running
// without a long-lived process (matching the devcontainer CLI's default).
var keepAlive = []string{"/bin/sh", "-c", "while sleep 1000; do :; done"}

// Hooks are the container-side lifecycle commands for one service, run in order
// after the service is ready. User is the exec user (remoteUser, falling back to
// containerUser); empty means the image default. WorkDir is the working
// directory the commands run in (the container workspace folder).
type Hooks struct {
	User          string
	WorkDir       string
	OnCreate      *LifecycleCommand
	UpdateContent *LifecycleCommand
	PostCreate    *LifecycleCommand
	PostStart     *LifecycleCommand
	PostAttach    *LifecycleCommand
}

// empty reports whether the hooks carry no commands at all.
func (h *Hooks) empty() bool {
	return h == nil || (h.OnCreate == nil && h.UpdateContent == nil &&
		h.PostCreate == nil && h.PostStart == nil && h.PostAttach == nil)
}

// Result is a parsed devcontainer translated into cornus's model: a
// compose.Project (fed to the normal Order/Plan pipeline), the base directory
// relative paths resolve against, per-service lifecycle Hooks, an optional
// host-side Initialize command, and human-readable Warnings for every field that
// was recognised but not applied.
type Result struct {
	Project    *compose.Project
	BaseDir    string
	Hooks      map[string]*Hooks
	Initialize *LifecycleCommand
	Warnings   []string
}

// Load reads a devcontainer definition and translates it. path may be a
// devcontainer.json file or a directory to search (see resolveConfig).
func Load(path string) (*Result, error) {
	file, root, err := resolveConfig(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var s spec
	if err := parseJSONC(data, &s); err != nil {
		return nil, fmt.Errorf("devcontainer %s: %w", file, err)
	}

	configDir := filepath.Dir(file)
	localWorkspaceFolder, err := filepath.Abs(root)
	if err != nil {
		localWorkspaceFolder = root
	}

	// Resolve the workspace folder (the container path source is mounted at),
	// then finalize the variable context so mounts/env can reference it.
	vc := varContext{
		localWorkspaceFolder:         localWorkspaceFolder,
		localWorkspaceFolderBasename: filepath.Base(localWorkspaceFolder),
	}
	workspaceFolder := s.WorkspaceFolder
	if workspaceFolder == "" {
		workspaceFolder = "/workspaces/" + vc.localWorkspaceFolderBasename
	} else {
		workspaceFolder, _ = vc.substitute(workspaceFolder)
	}
	vc.containerWorkspaceFolder = workspaceFolder

	var warnings []string
	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	// track substitution failures once per distinct token.
	unresolvedSeen := map[string]bool{}
	sub := func(in string) string {
		out, un := vc.substitute(in)
		for _, u := range un {
			if !unresolvedSeen[u] {
				unresolvedSeen[u] = true
				warn("unresolved variable %s left as-is", u)
			}
		}
		return out
	}
	// subQuiet substitutes without reporting leftovers, for strings a shell
	// expands later (lifecycle commands).
	subQuiet := func(in string) string {
		out, _ := vc.substitute(in)
		return out
	}

	if len(s.Features) > 0 && string(s.Features) != "null" && string(s.Features) != "{}" {
		warn("`features` is not supported and was ignored: the container is created from `image`/`build` alone, with no feature packages installed. Bake what you need into the image (or a `build.dockerfile`) instead")
	}
	if len(s.Customizations) > 0 && string(s.Customizations) != "null" {
		warn("`customizations` is editor-specific and was ignored: no editor settings, extensions, or editor-provided port forwarding are applied")
	}
	if len(s.HostRequirements) > 0 && string(s.HostRequirements) != "null" {
		warn("`hostRequirements` is not supported and was ignored: cornus does not check the target's CPU, memory, storage, or GPU before deploying")
	}

	// Lifecycle commands are variable-expanded per the spec (${containerWorkspaceFolder}
	// and friends) before they reach the shell.
	for _, c := range []*LifecycleCommand{
		s.InitializeCommand, s.OnCreateCommand, s.UpdateContentCommand,
		s.PostCreateCommand, s.PostStartCommand, s.PostAttachCommand,
	} {
		c.substitute(subQuiet)
	}
	s.Image = sub(s.Image)
	s.Name = sub(s.Name)

	// The workspace bind mount is the defining devcontainer behaviour: bind the
	// local project into the container at workspaceFolder.
	workspaceMount, err := resolveWorkspaceMount(s.WorkspaceMount, localWorkspaceFolder, workspaceFolder, sub, warn)
	if err != nil {
		return nil, err
	}

	hooks := &Hooks{
		User:          firstNonEmpty(s.RemoteUser, s.ContainerUser),
		WorkDir:       workspaceFolder,
		OnCreate:      s.OnCreateCommand,
		UpdateContent: s.UpdateContentCommand,
		PostCreate:    s.PostCreateCommand,
		PostStart:     s.PostStartCommand,
		PostAttach:    s.PostAttachCommand,
	}

	res := &Result{
		Initialize: s.InitializeCommand,
		Hooks:      map[string]*Hooks{},
	}

	if len(s.DockerComposeFile) > 0 {
		if err := loadComposeBased(res, &s, configDir, workspaceMount, hooks, sub, warn); err != nil {
			return nil, err
		}
		res.BaseDir = filepath.Dir(firstComposePath(s.DockerComposeFile, configDir))
	} else {
		if err := loadSingleContainer(res, &s, configDir, workspaceMount, hooks, sub, warn); err != nil {
			return nil, err
		}
		res.BaseDir = localWorkspaceFolder
	}
	res.Warnings = warnings
	return res, nil
}

// loadSingleContainer synthesizes a one-service project from an image/build
// devcontainer.
func loadSingleContainer(res *Result, s *spec, configDir string, workspace compose.Volume, hooks *Hooks, sub func(string) string, warn func(string, ...any)) error {
	if s.Image == "" && s.Build == nil {
		return fmt.Errorf("devcontainer: needs `image`, `build`, or `dockerComposeFile`")
	}
	svc := compose.ServiceDocument{
		Image:       s.Image,
		Environment: mergeEnv(s.ContainerEnv, s.RemoteEnv, sub),
		// containerUser is the user the container's own processes run as, which is
		// exactly compose's `user:`. runArgs `--user` may still override it below.
		User: s.ContainerUser,
	}
	if s.Build != nil {
		b, err := buildFromSpec(s.Build, configDir, sub, warn)
		if err != nil {
			return err
		}
		svc.Build = b
	}

	volumes, tmpfs := mountsToVolumes(s.Mounts, sub, warn)
	svc.Volumes = append([]compose.Volume{workspace}, volumes...)
	svc.Tmpfs = append(svc.Tmpfs, tmpfs...)
	svc.Ports = portsToCompose(append(append(PortList{}, s.AppPort...), s.ForwardPorts...))
	applyRunArgs(&svc, s.RunArgs, sub, warn)
	if override := s.OverrideCommand == nil || *s.OverrideCommand; override && len(svc.Command) == 0 && len(svc.Entrypoint) == 0 {
		// overrideCommand runs the keep-alive loop as the container's ENTRY POINT
		// (per the Dev Container spec), replacing the image's own entrypoint —
		// carried as spec.Entrypoint so it works even on an image with a
		// non-trivial ENTRYPOINT. Setting it as Command would only append args to
		// that entrypoint, so the keep-alive would never run.
		svc.Entrypoint = compose.Command(keepAlive)
	}

	res.Project = compose.NewProject(&compose.ProjectDocument{
		Name:     s.Name,
		Services: map[string]compose.ServiceDocument{singleServiceName: svc},
	})
	if !hooks.empty() || hooks.User != "" {
		res.Hooks[singleServiceName] = hooks
	}
	return nil
}

// loadComposeBased loads the referenced Compose file(s) and overlays the
// devcontainer's workspace mount, env, ports, and command on the target service.
func loadComposeBased(res *Result, s *spec, configDir string, workspace compose.Volume, hooks *Hooks, sub func(string) string, warn func(string, ...any)) error {
	if s.Service == "" {
		return fmt.Errorf("devcontainer: `dockerComposeFile` requires `service`")
	}
	files := make([]string, 0, len(s.DockerComposeFile))
	for _, f := range s.DockerComposeFile {
		if !filepath.IsAbs(f) {
			f = filepath.Join(configDir, f)
		}
		files = append(files, f)
	}
	doc, err := compose.LoadDocument(files...)
	if err != nil {
		return err
	}
	target, ok := doc.Services[s.Service]
	if !ok {
		return fmt.Errorf("devcontainer: service %q not found in the compose file", s.Service)
	}

	if s.Image != "" {
		warn("`image` is ignored for compose-based devcontainers: service %q runs the image its compose file declares", s.Service)
	}
	if s.Build != nil {
		warn("`build` is ignored for compose-based devcontainers: service %q is built from the compose file's own `build` section, if it has one", s.Service)
	}

	// Overlay devcontainer settings onto the target service.
	volumes, tmpfs := mountsToVolumes(s.Mounts, sub, warn)
	target.Volumes = append(target.Volumes, workspace)
	target.Volumes = append(target.Volumes, volumes...)
	target.Tmpfs = append(target.Tmpfs, tmpfs...)
	if s.ContainerUser != "" {
		target.User = s.ContainerUser
	}
	target.Environment = overlayEnv(target.Environment, mergeEnv(s.ContainerEnv, s.RemoteEnv, sub))
	target.Ports = append(target.Ports, portsToCompose(append(append(PortList{}, s.AppPort...), s.ForwardPorts...))...)
	// overrideCommand defaults to false for compose-based devcontainers (the
	// compose service's own command is respected). When set, the keep-alive loop
	// replaces the container's ENTRY POINT (per the Dev Container spec), carried
	// as the compose entrypoint so it runs regardless of the image's own
	// ENTRYPOINT.
	if s.OverrideCommand != nil && *s.OverrideCommand {
		target.Entrypoint = compose.Command(keepAlive)
		target.Command = nil // fully override; don't append the service's own command as args
	}
	if len(s.RunArgs) > 0 {
		// Per the Dev Container spec runArgs applies to the single-container
		// flavor only; the compose file is the place to express these.
		warn("`runArgs` is ignored for compose-based devcontainers (%s): set the equivalent keys on service %q in the compose file instead", strings.Join(dedup(s.RunArgs), " "), s.Service)
	}
	doc.Services[s.Service] = target

	// runServices limits which services start (the main service always does).
	if len(s.RunServices) > 0 {
		keep := map[string]bool{s.Service: true}
		for _, r := range s.RunServices {
			if _, ok := doc.Services[r]; !ok {
				warn("`runServices` names %q, which is not a service in the compose file; it was ignored", r)
				continue
			}
			keep[r] = true
		}
		for name := range doc.Services {
			if !keep[name] {
				delete(doc.Services, name)
			}
		}
	}
	if doc.Name == "" {
		doc.Name = s.Name
	}
	res.Project = compose.NewProject(doc)
	if !hooks.empty() || hooks.User != "" {
		res.Hooks[s.Service] = hooks
	}
	return nil
}

// buildFromSpec translates a devcontainer build object into a compose.Build,
// resolving context/dockerfile relative to the devcontainer.json directory as
// the spec prescribes.
func buildFromSpec(b *buildSpec, configDir string, sub func(string) string, warn func(string, ...any)) (*compose.Build, error) {
	contextDir := b.Context
	if contextDir == "" {
		contextDir = configDir
	} else {
		contextDir = sub(contextDir)
		if !filepath.IsAbs(contextDir) {
			contextDir = filepath.Join(configDir, contextDir)
		}
	}
	out := &compose.Build{Context: contextDir}
	if b.Target != "" {
		out.Target = sub(b.Target)
	}
	for _, ref := range b.CacheFrom {
		if r := sub(ref); r != "" {
			out.CacheFrom = append(out.CacheFrom, r)
		}
	}
	if b.Dockerfile != "" {
		dockerfileAbs := b.Dockerfile
		if !filepath.IsAbs(dockerfileAbs) {
			dockerfileAbs = filepath.Join(configDir, dockerfileAbs)
		}
		// The build engine resolves the Dockerfile relative to the context.
		if rel, err := filepath.Rel(contextDir, dockerfileAbs); err == nil {
			out.Dockerfile = rel
		} else {
			out.Dockerfile = b.Dockerfile
		}
	}
	if len(b.Args) > 0 {
		out.Args = map[string]string{}
		for k, v := range b.Args {
			out.Args[k] = sub(v)
		}
	}
	// build.options is a `docker build` argv; apply it last so an option repeating
	// a sibling key (e.g. --target) wins, as it would on the docker CLI.
	applyBuildOptions(out, b.Options, sub, warn)
	return out, nil
}

// resolveWorkspaceMount builds the workspace bind mount. An explicit
// workspaceMount (docker mount string) overrides the default bind of the local
// workspace folder at the container workspace folder.
func resolveWorkspaceMount(spec, localFolder, containerFolder string, sub func(string) string, warn func(string, ...any)) (compose.Volume, error) {
	if spec == "" {
		return compose.Volume{Source: localFolder, Target: containerFolder}, nil
	}
	m, unsupported, err := parseMountString(sub(spec))
	if err != nil {
		return compose.Volume{}, fmt.Errorf("workspaceMount: %w", err)
	}
	if len(unsupported) > 0 {
		warn("`workspaceMount` option(s) %s have no cornus equivalent and were ignored; the workspace is still mounted at %s", strings.Join(dedup(unsupported), " "), m.Target)
	}
	if !supportedMountType(m.Type) {
		warn("`workspaceMount` type %q is not supported; the workspace is mounted as a bind mount at %s instead", m.Type, m.Target)
	}
	return mountToVolume(m), nil
}

// mountsToVolumes converts devcontainer mounts to compose volumes (bind ->
// host mount, type=volume -> named/anonymous managed volume) and tmpfs targets
// (type=tmpfs -> the service's tmpfs list). Entries carrying options or a type
// cornus cannot honour are reported.
func mountsToVolumes(mounts MountList, sub func(string) string, warn func(string, ...any)) (volumes []compose.Volume, tmpfs []string) {
	for _, m := range mounts {
		m.Source = sub(m.Source)
		m.Target = sub(m.Target)
		if len(m.Unsupported) > 0 {
			warn("`mounts` entry %q: option(s) %s have no cornus equivalent and were ignored; the rest of the mount is applied", m.Spec, strings.Join(dedup(m.Unsupported), " "))
		}
		if strings.EqualFold(m.Type, "tmpfs") {
			tmpfs = append(tmpfs, m.Target)
			continue
		}
		if !supportedMountType(m.Type) {
			warn("`mounts` entry %q: type %q is not supported; it was applied as a bind mount at %s instead", m.Spec, m.Type, m.Target)
		}
		volumes = append(volumes, mountToVolume(m))
	}
	return volumes, tmpfs
}

// supportedMountType reports whether a devcontainer mount type maps onto a
// cornus mount. An empty type means bind (the docker default).
func supportedMountType(t string) bool {
	switch strings.ToLower(t) {
	case "", "bind", "volume", "tmpfs":
		return true
	default:
		return false
	}
}

func mountToVolume(m Mount) compose.Volume {
	v := compose.Volume{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly}
	if m.Type == "volume" {
		v.Named = true // Source (if any) is a volume name; empty => anonymous
	}
	return v
}

func portsToCompose(ports PortList) []compose.Port {
	var out []compose.Port
	for _, p := range ports {
		out = append(out, compose.Port{Host: p.Host, Container: p.Container, Protocol: "tcp"})
	}
	return out
}

// mergeEnv builds a compose.Environment from containerEnv overlaid with
// remoteEnv (remoteEnv wins), substituting variables in every value. Both are
// applied to the container so lifecycle commands see them too.
func mergeEnv(containerEnv, remoteEnv StringMap, sub func(string) string) compose.Environment {
	if len(containerEnv) == 0 && len(remoteEnv) == 0 {
		return nil
	}
	out := compose.Environment{}
	for k, v := range containerEnv {
		out[k] = sub(v)
	}
	for k, v := range remoteEnv {
		out[k] = sub(v)
	}
	return out
}

// overlayEnv merges add over base (add wins), returning base when add is empty.
func overlayEnv(base, add compose.Environment) compose.Environment {
	if len(add) == 0 {
		return base
	}
	if base == nil {
		base = compose.Environment{}
	}
	for k, v := range add {
		base[k] = v
	}
	return base
}

// resolveConfig locates the devcontainer.json for path and the workspace root
// (the folder that contains .devcontainer). path may be the file itself or a
// directory to search.
func resolveConfig(path string) (file, root string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return path, workspaceRootFor(path), nil
	}
	// Directory: search the standard locations.
	candidates := []string{
		filepath.Join(path, ".devcontainer", "devcontainer.json"),
		filepath.Join(path, ".devcontainer.json"),
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c, path, nil
		}
	}
	// A single .devcontainer/<sub>/devcontainer.json is also allowed.
	subdir := filepath.Join(path, ".devcontainer")
	if entries, e := os.ReadDir(subdir); e == nil {
		var found []string
		for _, entry := range entries {
			if entry.IsDir() {
				c := filepath.Join(subdir, entry.Name(), "devcontainer.json")
				if fileExists(c) {
					found = append(found, c)
				}
			}
		}
		sort.Strings(found)
		if len(found) == 1 {
			return found[0], path, nil
		}
		if len(found) > 1 {
			return "", "", fmt.Errorf("devcontainer: multiple definitions under %s; pass --devcontainer with a specific file", subdir)
		}
	}
	return "", "", fmt.Errorf("no devcontainer.json found under %s (looked for .devcontainer/devcontainer.json, .devcontainer.json, .devcontainer/*/devcontainer.json)", path)
}

// workspaceRootFor returns the workspace folder for a devcontainer.json file:
// the parent of a .devcontainer directory component if present, else the file's
// own directory.
func workspaceRootFor(file string) string {
	dir := filepath.Dir(file)
	if filepath.Base(dir) == ".devcontainer" {
		return filepath.Dir(dir)
	}
	// .devcontainer/<sub>/devcontainer.json -> two levels up.
	if filepath.Base(filepath.Dir(dir)) == ".devcontainer" {
		return filepath.Dir(filepath.Dir(dir))
	}
	return dir
}

func firstComposePath(files StringList, configDir string) string {
	f := files[0]
	if !filepath.IsAbs(f) {
		f = filepath.Join(configDir, f)
	}
	return f
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
