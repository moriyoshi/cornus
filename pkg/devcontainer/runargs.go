package devcontainer

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"cornus/pkg/compose"
)

// This file maps the two docker-CLI argv lists a devcontainer.json may carry —
// `runArgs` (a `docker run` argv, single-container flavor only) and
// `build.options` (a `docker build` argv) — onto the compose model cornus
// already deploys from. Everything the model cannot express is reported by flag
// name, so the compatibility boundary is always visible rather than silent.

// dockerFlag is one recognised docker-CLI flag: whether it consumes a value and
// the mutation it performs.
type dockerFlag struct {
	takesValue bool
	apply      func(val string)
}

// flagTable maps every spelling of a flag (long and short) to its handler.
type flagTable map[string]dockerFlag

// boolFlag registers a presence flag. val is "" for the bare form and the "=x"
// payload for `--flag=false`.
func (t flagTable) boolFlag(names []string, set func(bool)) {
	for _, n := range names {
		t[n] = dockerFlag{apply: func(val string) { set(truthy(val)) }}
	}
}

// valueFlag registers a flag that consumes a value, expanding devcontainer
// variables in it first.
func (t flagTable) valueFlag(names []string, sub func(string) string, set func(string)) {
	for _, n := range names {
		t[n] = dockerFlag{takesValue: true, apply: func(val string) { set(sub(val)) }}
	}
}

// ignore registers flags that are accepted but intentionally do nothing because
// cornus's model already implies them (e.g. `-d`).
func (t flagTable) ignore(names []string, takesValue bool) {
	for _, n := range names {
		t[n] = dockerFlag{takesValue: takesValue, apply: func(string) {}}
	}
}

// truthy interprets a boolean flag payload: bare (empty) means set.
func truthy(val string) bool {
	switch strings.ToLower(val) {
	case "", "1", "true", "yes":
		return true
	default:
		return false
	}
}

// scanDockerArgs walks a docker-CLI argv, dispatching recognised flags to table
// and returning the names of everything it could not handle. It understands
// `--flag`, `--flag=v`, `--flag v`, and the short spellings of the same. An
// unrecognised flag swallows a following non-flag token as its presumed value so
// the value is not reported as a second unknown flag.
func scanDockerArgs(args []string, table flagTable) []string {
	var unknown []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			unknown = append(unknown, a)
			continue
		}
		name, val, hasVal := strings.Cut(a, "=")
		f, ok := table[name]
		if !ok {
			unknown = append(unknown, name)
			if !hasVal && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++ // the unknown flag's value rides along
			}
			continue
		}
		if f.takesValue && !hasVal {
			if i+1 >= len(args) {
				unknown = append(unknown, name) // dangling flag, no value to apply
				continue
			}
			i++
			val = args[i]
		}
		if f.takesValue && val == "" {
			unknown = append(unknown, name) // empty value: nothing to apply
			continue
		}
		f.apply(val)
	}
	return unknown
}

// applyRunArgs maps a single-container devcontainer's `runArgs` (a `docker run`
// argv) onto the synthesized compose service. Flags with no cornus equivalent
// are named in a warning.
func applyRunArgs(svc *compose.ServiceDocument, args []string, sub func(string) string, warn func(string, ...any)) {
	if len(args) == 0 {
		return
	}
	t := flagTable{}

	t.boolFlag([]string{"--privileged"}, func(v bool) { svc.Privileged = v })
	t.boolFlag([]string{"--init"}, func(v bool) { svc.Init = &v })
	t.boolFlag([]string{"--read-only"}, func(v bool) { svc.ReadOnly = v })
	t.boolFlag([]string{"-t", "--tty"}, func(v bool) { svc.TTY = v })
	t.boolFlag([]string{"-i", "--interactive"}, func(v bool) { svc.StdinOpen = v })
	// cornus deploys are always detached, so `-d` names the behaviour it already
	// has. (`--rm` does not: it falls through to the unsupported list, since a
	// cornus deployment outlives its container's exit.)
	t.ignore([]string{"-d", "--detach"}, false)

	t.valueFlag([]string{"-u", "--user"}, sub, func(v string) { svc.User = v })
	t.valueFlag([]string{"-w", "--workdir"}, sub, func(v string) { svc.WorkingDir = v })
	t.valueFlag([]string{"-h", "--hostname"}, sub, func(v string) { svc.Hostname = v })
	t.valueFlag([]string{"--name"}, sub, func(v string) { svc.ContainerName = v })
	t.valueFlag([]string{"--entrypoint"}, sub, func(v string) { svc.Entrypoint = compose.Command{v} })
	t.valueFlag([]string{"--restart"}, sub, func(v string) { svc.Restart = compose.Restart(v) })
	t.valueFlag([]string{"--stop-signal"}, sub, func(v string) { svc.StopSignal = v })
	t.valueFlag([]string{"--stop-timeout"}, sub, func(v string) { svc.StopGracePeriod = v + "s" })
	// --network has no compose equivalent in cornus's model (services always join
	// the project network), and it is the single most common runArg, so it gets a
	// warning that says what happens instead rather than the generic list.
	t.valueFlag([]string{"--network", "--net"}, sub, func(v string) {
		warn("`runArgs` --network=%s is not supported and was ignored: the container joins the project's own cornus network instead", v)
	})
	t.valueFlag([]string{"--pid"}, sub, func(v string) { svc.PID = v })
	t.valueFlag([]string{"--ipc"}, sub, func(v string) { svc.IPC = v })

	t.valueFlag([]string{"--cap-add"}, sub, func(v string) { svc.CapAdd = append(svc.CapAdd, v) })
	t.valueFlag([]string{"--cap-drop"}, sub, func(v string) { svc.CapDrop = append(svc.CapDrop, v) })
	t.valueFlag([]string{"--security-opt"}, sub, func(v string) { svc.SecurityOpt = append(svc.SecurityOpt, v) })
	t.valueFlag([]string{"--group-add"}, sub, func(v string) { svc.GroupAdd = append(svc.GroupAdd, v) })
	t.valueFlag([]string{"--device"}, sub, func(v string) { svc.Devices = append(svc.Devices, v) })
	t.valueFlag([]string{"--tmpfs"}, sub, func(v string) { svc.Tmpfs = append(svc.Tmpfs, v) })
	t.valueFlag([]string{"--dns"}, sub, func(v string) { svc.DNS = append(svc.DNS, v) })
	t.valueFlag([]string{"--dns-search"}, sub, func(v string) { svc.DNSSearch = append(svc.DNSSearch, v) })
	t.valueFlag([]string{"--dns-option", "--dns-opt"}, sub, func(v string) { svc.DNSOpt = append(svc.DNSOpt, v) })
	t.valueFlag([]string{"--add-host"}, sub, func(v string) { svc.ExtraHosts = append(svc.ExtraHosts, v) })
	t.valueFlag([]string{"--expose"}, sub, func(v string) {
		port, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(v, "/tcp"), "/udp"))
		if err != nil {
			warn("`runArgs` --expose %q is not a plain port number and was ignored", v)
			return
		}
		svc.Expose = append(svc.Expose, port)
	})

	t.valueFlag([]string{"--sysctl"}, sub, func(v string) {
		k, val, _ := strings.Cut(v, "=")
		if svc.Sysctls == nil {
			svc.Sysctls = compose.Sysctls{}
		}
		svc.Sysctls[k] = val
	})
	t.valueFlag([]string{"-l", "--label"}, sub, func(v string) {
		k, val, _ := strings.Cut(v, "=")
		if svc.Labels == nil {
			svc.Labels = compose.Labels{}
		}
		svc.Labels[k] = val
	})
	t.valueFlag([]string{"-e", "--env"}, sub, func(v string) {
		k, val, ok := strings.Cut(v, "=")
		if !ok {
			// docker passes the host's current value through.
			val = os.Getenv(k)
			if val == "" {
				warn("`runArgs` -e %s takes its value from the host environment, which is unset; the variable is set to the empty string", k)
			}
		}
		if svc.Environment == nil {
			svc.Environment = compose.Environment{}
		}
		svc.Environment[k] = val
	})
	t.valueFlag([]string{"--ulimit"}, sub, func(v string) {
		u, err := parseUlimit(v)
		if err != nil {
			warn("`runArgs` --ulimit %q could not be parsed and was ignored: %v", v, err)
			return
		}
		svc.Ulimits = append(svc.Ulimits, u)
	})
	t.valueFlag([]string{"-p", "--publish"}, sub, func(v string) {
		ports, err := parseComposePorts(v)
		if err != nil {
			warn("`runArgs` --publish %q could not be parsed and was ignored: %v", v, err)
			return
		}
		svc.Ports = append(svc.Ports, ports...)
	})
	t.valueFlag([]string{"-v", "--volume"}, sub, func(v string) {
		vol, err := parseComposeVolume(v)
		if err != nil {
			warn("`runArgs` --volume %q could not be parsed and was ignored: %v", v, err)
			return
		}
		svc.Volumes = append(svc.Volumes, vol)
	})
	t.valueFlag([]string{"--mount"}, sub, func(v string) {
		m, unknownKeys, err := parseMountString(v)
		if err != nil {
			warn("`runArgs` --mount %q could not be parsed and was ignored: %v", v, err)
			return
		}
		if len(unknownKeys) > 0 {
			warn("`runArgs` --mount %q: option(s) %s have no cornus equivalent and were ignored", v, strings.Join(unknownKeys, " "))
		}
		if m.Type == "tmpfs" {
			svc.Tmpfs = append(svc.Tmpfs, m.Target)
			return
		}
		svc.Volumes = append(svc.Volumes, mountToVolume(m))
	})

	// Size/CPU fields use a compose-internal scalar type that cannot be named
	// from here; decode a one-key document and lift the field out.
	t.valueFlag([]string{"--shm-size"}, sub, func(v string) { svc.ShmSize = serviceScalar("shm_size", v).ShmSize })
	t.valueFlag([]string{"-m", "--memory"}, sub, func(v string) { svc.MemLimit = serviceScalar("mem_limit", v).MemLimit })
	t.valueFlag([]string{"--cpus"}, sub, func(v string) { svc.CPUs = serviceScalar("cpus", v).CPUs })

	if unknown := scanDockerArgs(args, t); len(unknown) > 0 {
		warn("`runArgs` entries have no cornus equivalent and were ignored: %s", strings.Join(dedup(unknown), " "))
	}
}

// applyBuildOptions maps a devcontainer `build.options` list (a `docker build`
// argv) onto the compose build. Flags with no cornus equivalent are named in a
// warning.
func applyBuildOptions(b *compose.Build, options []string, sub func(string) string, warn func(string, ...any)) {
	if len(options) == 0 {
		return
	}
	t := flagTable{}

	t.boolFlag([]string{"--no-cache"}, func(v bool) { b.NoCache = v })
	t.boolFlag([]string{"--pull"}, func(v bool) { b.Pull = v })
	// True no-ops under BuildKit, which is the only engine cornus builds with.
	// (`--load` is deliberately NOT here: cornus exports to its own registry, not
	// to a local docker image store, so it is reported rather than swallowed.)
	t.ignore([]string{"-q", "--quiet", "--rm", "--force-rm"}, false)

	t.valueFlag([]string{"--target"}, sub, func(v string) { b.Target = v })
	t.valueFlag([]string{"--network"}, sub, func(v string) { b.Network = v })
	t.valueFlag([]string{"--platform"}, sub, func(v string) {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				b.Platforms = append(b.Platforms, p)
			}
		}
	})
	t.valueFlag([]string{"--cache-from"}, sub, func(v string) { b.CacheFrom = append(b.CacheFrom, v) })
	t.valueFlag([]string{"--cache-to"}, sub, func(v string) { b.CacheTo = append(b.CacheTo, v) })
	t.valueFlag([]string{"--add-host"}, sub, func(v string) { b.ExtraHosts = append(b.ExtraHosts, v) })
	t.valueFlag([]string{"--ssh"}, sub, func(v string) { b.SSH = append(b.SSH, v) })
	t.valueFlag([]string{"-t", "--tag"}, sub, func(v string) { b.Tags = append(b.Tags, v) })
	t.valueFlag([]string{"--build-arg"}, sub, func(v string) {
		k, val, ok := strings.Cut(v, "=")
		if !ok {
			val = os.Getenv(k)
			if val == "" {
				warn("`build.options` --build-arg %s takes its value from the host environment, which is unset; the argument is set to the empty string", k)
			}
		}
		if b.Args == nil {
			b.Args = map[string]string{}
		}
		b.Args[k] = val
	})
	t.valueFlag([]string{"--label"}, sub, func(v string) {
		k, val, _ := strings.Cut(v, "=")
		if b.Labels == nil {
			b.Labels = map[string]string{}
		}
		b.Labels[k] = val
	})
	t.valueFlag([]string{"--shm-size"}, sub, func(v string) { b.ShmSize = buildScalar("shm_size", v).ShmSize })

	if unknown := scanDockerArgs(options, t); len(unknown) > 0 {
		warn("`build.options` entries have no cornus equivalent and were ignored: %s", strings.Join(dedup(unknown), " "))
	}
}

// parseUlimit parses docker's `--ulimit name=soft[:hard]`.
func parseUlimit(s string) (compose.Ulimit, error) {
	name, limits, ok := strings.Cut(s, "=")
	if !ok {
		return compose.Ulimit{}, fmt.Errorf("expected name=soft[:hard]")
	}
	softStr, hardStr, hasHard := strings.Cut(limits, ":")
	soft, err := strconv.ParseInt(strings.TrimSpace(softStr), 10, 64)
	if err != nil {
		return compose.Ulimit{}, err
	}
	hard := soft
	if hasHard {
		if hard, err = strconv.ParseInt(strings.TrimSpace(hardStr), 10, 64); err != nil {
			return compose.Ulimit{}, err
		}
	}
	return compose.Ulimit{Name: strings.TrimSpace(name), Soft: soft, Hard: hard}, nil
}

// parseComposePorts parses a docker `--publish` spec by handing it to the
// compose port decoder, so ranges, protocols, and host IPs behave identically to
// a compose `ports:` entry.
func parseComposePorts(s string) (compose.Ports, error) {
	data, err := json.Marshal([]string{s})
	if err != nil {
		return nil, err
	}
	var ports compose.Ports
	if err := json.Unmarshal(data, &ports); err != nil {
		return nil, err
	}
	return ports, nil
}

// parseComposeVolume parses a docker `--volume` spec through the compose volume
// decoder (src:dst[:ro][,z] and the named-volume form).
func parseComposeVolume(s string) (compose.Volume, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return compose.Volume{}, err
	}
	var v compose.Volume
	if err := json.Unmarshal(data, &v); err != nil {
		return compose.Volume{}, err
	}
	return v, nil
}

// serviceScalar decodes {key: val} into a throwaway service so a scalar field
// whose type is unexported by pkg/compose can be lifted out and assigned.
func serviceScalar(key, val string) compose.ServiceDocument {
	var tmp compose.ServiceDocument
	data, err := json.Marshal(map[string]string{key: val})
	if err == nil {
		_ = json.Unmarshal(data, &tmp)
	}
	return tmp
}

// buildScalar is serviceScalar for compose.Build's scalar fields.
func buildScalar(key, val string) compose.Build {
	var tmp compose.Build
	data, err := json.Marshal(map[string]string{key: val})
	if err == nil {
		_ = json.Unmarshal(data, &tmp)
	}
	return tmp
}
