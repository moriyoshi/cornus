package composecli

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"cornus/cmd/cornus/internal/cliout"
)

// Docker Compose flag compatibility: the flags cornus accepts for parity but
// cannot fully honor, and how it says so.
//
// The rule this file exists to enforce is: NEVER accept a flag silently when it
// does nothing. A user who passes --force-recreate and gets no recreation, with
// no signal, is worse off than one who gets an error. Every flag cornus takes
// falls into exactly one of four buckets, and each bucket has a visible
// behavior:
//
//  1. Honored — the flag maps onto the deploy-to-a-server model and is
//     implemented (up --no-deps / --force-recreate, logs --no-color / --index,
//     build --pull / --quiet). Nothing here.
//  2. Accepted, cannot be honored — Docker takes it, cornus structurally cannot
//     (the deploy API has no such knob), and refusing would block an otherwise
//     correct command. It warns ONCE, naming the reason and the thing to do
//     instead: warnShutdownTimeout, warnRemoveImages.
//  3. Already the default — the flag asks for what cornus always does. It is
//     accepted and NOTED (build --push), not silently swallowed, because the
//     Docker meaning ("push to the registry in the tag") and the cornus meaning
//     ("push to the cornus registry") are not identical.
//  4. Deliberate divergence — the spelling or default differs and is documented
//     rather than emulated (logs has no -f short; up --no-attach is a
//     project-wide bool; ps has its own column set). Where the divergence can
//     make the SAME command line mean two different things, it warns:
//     warnNoAttachDivergence.
//
// A fifth bucket, "reject with an actionable error", is deliberately empty for
// these flags: every one of them is either honorable or orthogonal to what the
// command actually does, so refusing the whole command would cost the user the
// teardown/deploy they asked for to protect them from a side effect that never
// happens anyway.

// timeoutUnset is the sentinel for "-t/--timeout was not passed". Docker's own
// CLI uses -1 for the same purpose, so a user who explicitly passes `-t 0`
// (stop immediately) is still distinguishable from one who passed nothing.
const timeoutUnset = -1

// forceRecreateLabel is the label `up --force-recreate` stamps on every selected
// service's spec to guarantee the workload is actually replaced.
//
// It works on every backend because of how each Apply behaves:
//
//   - dockerhost / containerd / bare / incus remove the existing instances and
//     create them again on EVERY Apply, so they already recreate unconditionally
//     and the label changes nothing for them (it just rides along as a container
//     label).
//   - kubernetes patches a Deployment. An Apply whose pod template is
//     byte-identical to the live one is a no-op — the Deployment controller has
//     no reason to roll pods — which is exactly the case --force-recreate is for.
//     Compose labels land in the pod template's ANNOTATIONS (never in the
//     selector, see the kubernetes backend's deployment()), so changing this one
//     changes the pod template and forces a fresh ReplicaSet: the same mechanism
//     `kubectl rollout restart` uses.
const forceRecreateLabel = "cornus.force-recreate"

// recreateToken is the value stamped into forceRecreateLabel: one value per
// cornus process, resolved at package init.
//
// Per-process rather than per-service so a project's services all recreate as
// one unit, and rather than per-Apply so a foreground `up --watch --force-recreate`
// reload does NOT re-roll every service on every file save — a reload already
// recreates what genuinely changed, and --force-recreate's promise is about the
// up the user typed, not about every subsequent reconcile of it.
var recreateToken = time.Now().UTC().Format(time.RFC3339Nano)

// warnShutdownTimeout reports that -t/--timeout cannot be honored, and where the
// shutdown grace period actually comes from.
//
// Bucket 2. cornus's lifecycle API is POST /.cornus/v1/deploy/{name}/{start,stop,
// restart} and DELETE /.cornus/v1/deploy/{name} — none of them carries a
// timeout, and deploy.Backend's Start/Stop/Restart/Delete take only a name. The
// server owns lifecycle timing, and the per-workload grace period is a property
// of the SPEC (api.DeploySpec.StopGracePeriod, from compose stop_grace_period),
// applied by the backend at create time as Docker's StopTimeout or the pod's
// terminationGracePeriodSeconds. So the knob exists — it just lives in the
// compose file, not on the command line, and the message says so.
func warnShutdownTimeout(d *cliout.Driver, seconds int, action string) {
	if d == nil || seconds < 0 {
		return
	}
	d.Warn("-t/--timeout is accepted for docker compose compatibility but cannot be honored on %s: the cornus deploy API carries no per-call shutdown timeout (the server owns lifecycle timing). The grace period is a property of the service — set `stop_grace_period: %ds` in the Compose file, which the backend applies as the container stop timeout / pod terminationGracePeriodSeconds.", action, seconds)
}

// warnRemoveImages reports that `down --rmi` removed no images.
//
// Bucket 2. Nothing in the stack can remove an image: deploy.Backend has no
// image API at all (only workloads and volumes), and a built image lives in the
// cornus registry on the SERVER, which the compose client has no delete route
// to. The teardown the user asked for still happens — refusing it over an
// unreclaimed layer would be the worse trade — so this warns and points at the
// one thing that does report server-side image usage.
func warnRemoveImages(d *cliout.Driver, mode string) {
	if d == nil || mode == "" {
		return
	}
	d.Warn("--rmi=%s is accepted for docker compose compatibility but removes nothing: cornus has no image-removal API (the deploy backends expose only workloads and volumes, and built images live in the cornus registry on the server). The workloads are still removed; use `cornus storage` to see what the server is holding, and reclaim image space on the backend host itself.", mode)
}

// warnNoAttachDivergence fires when `up --no-attach` is combined with an
// explicit service list — the one place where the divergence in bucket 4 makes
// the SAME command line mean two different things.
//
// `docker compose up --no-attach web` brings up the WHOLE project and suppresses
// only web's log stream (compose's --no-attach is a repeatable stringArray of
// service names). cornus's --no-attach is a project-wide bool, so the same line
// brings up ONLY web and suppresses every log stream. A user pasting a docker
// command would silently deploy a different set of services, so the ambiguity is
// named out loud rather than resolved by guessing.
func warnNoAttachDivergence(d *cliout.Driver, services []string) {
	if d == nil || len(services) == 0 {
		return
	}
	d.Warn("--no-attach is a project-wide switch in cornus (it suppresses log streaming for every service) and the positional arguments select WHICH services to bring up, so this brings up only %v. docker compose reads --no-attach's operand as a service to leave unattached and would bring up the whole project; if that is what you meant, drop the service list.", services)
}

// noteAlwaysPush records that build --push asked for what cornus already does.
//
// Bucket 3. Every compose build request is issued with Push: true (see
// resolveBuildRequest) — the built image has to reach the registry the deploy
// then pulls from, so a compose build that did not push would produce an image
// no backend could run. The flag is therefore always satisfied, but it is NOT
// silently swallowed: Docker's --push pushes to the registry named in the image
// tag, while cornus always pushes to ITS registry, so the note names the host
// the image actually went to.
func noteAlwaysPush(d *cliout.Driver, registry string) {
	if d == nil {
		return
	}
	d.Info("--push is already the default: every cornus compose build pushes its image to the cornus registry (%s), which is where the deploy pulls it from.", registry)
}

// explainFileFlagError turns the one confusing consequence of the -f divergence
// into an actionable message.
//
// `cornus compose logs` has no -f short for --follow: the compose GROUP owns
// -f/--file (every subcommand inherits it), and kong — unlike cobra, which lets
// a subcommand shadow a persistent flag — rejects a duplicate short outright, so
// the parse tree cannot be built at all. That is a documented divergence, but
// its failure mode is not: `cornus compose logs -f web` reads "web" as a compose
// file and fails with `open web: no such file or directory`, which says nothing
// about the flag that caused it.
//
// So when a -f value that does not look like a path at all (no separator, no
// extension) is missing, the error explains what -f actually means here. The
// heuristic is deliberately narrow: a real typo'd path like ./compose.yaml keeps
// its plain not-found error.
func explainFileFlagError(files []string, err error) error {
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, f := range files {
		if strings.ContainsRune(f, filepath.Separator) || filepath.Ext(f) != "" {
			continue
		}
		return fmt.Errorf("%w: -f/--file names the COMPOSE FILE for every `cornus compose` subcommand (the group owns it), so %q was read as a file name. It is not a short option for anything else — spell `logs --follow` in full", err, f)
	}
	return err
}

// expandDependencies grows an explicit service selection into the transitive
// closure of its depends_on dependencies, in the project's dependency order,
// and reports which services that added.
//
// This is what makes `up --no-deps` meaningful: `docker compose up web` starts
// web AND everything web depends on, and cornus previously started web alone —
// so a project whose web needs db came up broken, silently, with no flag
// involved. Expansion is now the default (matching compose and the documented
// "services are brought up in dependency order, honoring depends_on") and
// --no-deps opts out.
//
// Only services present in rt.plans are pulled in, so a dependency excluded by
// the active --profile / COMPOSE_PROFILES set is left out rather than resurrected
// (the same rule Order and waitForDependencies already apply to unknown
// dependencies). Pure apart from reading rt, so it is unit-testable.
func expandDependencies(rt *runtime, names []string) (selected, added []string) {
	want := make(map[string]bool, len(names))
	original := make(map[string]bool, len(names))
	queue := make([]string, 0, len(names))
	for _, n := range names {
		want[n] = true
		original[n] = true
		queue = append(queue, n)
	}
	svcs := rt.project.Services()
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		svc, ok := svcs[n]
		if !ok {
			continue
		}
		for _, dep := range svc.DependsOn {
			if want[dep.Service] {
				continue
			}
			if _, ok := rt.plans[dep.Service]; !ok {
				continue // not part of this project's active selection
			}
			want[dep.Service] = true
			queue = append(queue, dep.Service)
		}
	}
	for _, n := range rt.order {
		if !want[n] {
			continue
		}
		selected = append(selected, n)
		if !original[n] {
			added = append(added, n)
		}
	}
	return selected, added
}
