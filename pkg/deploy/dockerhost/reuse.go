package dockerhost

// Idempotent apply: a repeat Apply whose desired configuration is byte-identical
// to what is already running must leave the containers alone.
//
// Before this, Apply removed every instance of the deployment and created it
// again on EVERY call, unconditionally. `docker compose up` recreates a container
// only when its configuration or its image changed — that is precisely what
// `--force-recreate` exists to override — so cornus diverged from the tool it
// emulates, and the divergence hurt most in the loop cornus exists to serve:
// `compose up --watch` reloads the whole project on every file save, so editing
// ONE service bounced every other service in the project, dropping connections
// and restarting work that had no reason to stop.
//
// The mechanism is a fingerprint stamped on each container at create time and
// re-derived on the next Apply:
//
//   - fingerprintSpec hashes the WHOLE api.DeploySpec (as canonical JSON) plus
//     the daemon's content id for the resolved image. Hashing the whole spec
//     rather than an enumerated list of fields is the deliberate choice: an
//     enumeration is a hand-maintained list parallel to a struct, which is the
//     exact shape that has already produced silent-drop defects in this backend
//     (see warnUnsupported). With the whole struct in the hash, a field ADDED to
//     api.DeploySpec is covered the day it is added, and the failure mode of the
//     conservative direction — a field this backend ignores changing, and forcing
//     a needless recreate — is exactly the pre-existing behaviour, so it can
//     never be a regression. TestApplyRecreatesForEveryChangedSpecField holds
//     that property to account field by field.
//   - the image CONTENT id, not just the ref, is in the hash because the ref is
//     usually a mutable tag. `compose up --build` rebuilds the same tag with new
//     content; comparing refs alone would then keep the old container running the
//     old image, which is a far worse bug than the one being fixed.
//   - reusableInstances is the whole decision, as a pure function over the live
//     container summaries, so it can be unit-tested and neutralized in isolation
//     with no destructive daemon call reachable from the test.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"cornus/pkg/api"
)

// specHashLabel carries the fingerprint of the configuration a container was
// created for. Apply compares the label on the live containers against the
// fingerprint of the spec it was just handed; equal means there is nothing to do.
const specHashLabel = "cornus.spec-hash"

// specHashVersion is bumped whenever the fingerprint INPUT changes in a way that
// should invalidate previously stamped labels (so containers created by an older
// cornus are recreated once, rather than being compared against a hash computed
// by different rules). It is part of the hashed payload.
const specHashVersion = 1

// fingerprintSpec returns the hex SHA-256 of the deployment's desired
// configuration: the full spec plus the daemon's content id for its image.
//
// spec must be the spec as it will actually be realized — after Apply's own
// rewrites (telemetry env merge, network priority sort) — so that two Applies of
// the same compose file agree. Both of those rewrites are deterministic functions
// of the spec, so hashing after them is stable across processes.
//
// imageID is the daemon's content id (the `Id` of GET /images/{ref}/json) for
// spec.Image, resolved AFTER the pull. An empty imageID is a caller error: the
// caller must treat "could not resolve the image" as "cannot tell", and recreate.
func fingerprintSpec(spec api.DeploySpec, imageID string) (string, error) {
	payload := struct {
		Version int            `json:"v"`
		ImageID string         `json:"imageID"`
		Spec    api.DeploySpec `json:"spec"`
	}{Version: specHashVersion, ImageID: imageID, Spec: spec}
	// encoding/json sorts map keys, so the encoding is canonical for a given
	// value; slices keep their (already deterministic) order.
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// reusableInstances decides whether the live containers of a deployment already
// realize the desired configuration, so Apply can keep them instead of removing
// and recreating them. It returns the containers to keep and whether they may be
// kept.
//
// It is a PURE function over what the daemon reported, deliberately: every one of
// its branches guards a teardown, and a pure predicate can be tested — and
// deliberately broken to prove the test observes it — with no container removal
// reachable from the test at all.
//
// Reuse requires ALL of:
//
//   - a non-empty desired hash. An empty one means the caller could not compute a
//     fingerprint (no resolvable image id, an unmarshalable spec); "cannot tell"
//     must mean "recreate", never "keep".
//   - exactly `replicas` live APP containers. A replica-count change must
//     recreate, and a deployment that has lost (or gained) a container is not the
//     deployment the fingerprint describes.
//   - no companion container in the live set. A companion (egress, mounts,
//     remote, telemetry collector) carries configuration that is NOT part of the
//     spec — client-local mount sources, an egress plan, a collector config — so
//     the spec fingerprint cannot speak for it. Refusing reuse whenever one is
//     present keeps the fast path to the plain co-located Apply, which is the
//     path `compose up` takes for an ordinary service.
//   - every app container carrying exactly the desired hash.
//   - no app container being this cornus server's own container. Reuse would be
//     harmless there (nothing is torn down), but Apply's self-preservation
//     contract is that recreating the server is an operation from OUTSIDE the
//     server, reported as one explicit refusal — quietly succeeding instead would
//     make that refusal depend on whether the spec happened to change, which is
//     the kind of conditional safety guarantee nobody can reason about. selfID
//     may be empty (not containerized, or could not tell), which matches nothing.
//
// The companion count is kept SEPARATE from the app count rather than folded
// into one "len(live) != replicas" test, and that is not a stylistic choice. With
// companions counted as instances, the presence of a companion could only ever be
// caught as an arithmetic accident of the replica count, so the companion rule
// would be dead code that no test could distinguish from its own absence — which
// is exactly what the first version of this function did, and what its tests
// certified while observing nothing. Counting app instances only also agrees with
// every other enumeration in this backend (Status, List, instanceID all filter
// companions out), so `replicas` means the same thing everywhere.
func reusableInstances(live []containerSummary, replicas int, hash, selfID string) ([]containerSummary, bool) {
	if hash == "" || replicas <= 0 {
		return nil, false
	}
	apps := make([]containerSummary, 0, len(live))
	companions := 0
	for _, c := range live {
		if isCompanion(c) {
			companions++
			continue
		}
		apps = append(apps, c)
	}
	if len(apps) != replicas {
		return nil, false
	}
	if companions > 0 {
		return nil, false
	}
	for _, c := range apps {
		if c.Labels[specHashLabel] != hash {
			return nil, false
		}
		if sameContainer(c.ID, selfID) {
			return nil, false
		}
	}
	return apps, true
}
