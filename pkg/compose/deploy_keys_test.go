package compose

import (
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// planSpec loads a one-file compose project and returns the named service's
// translated api.DeploySpec.
func planSpec(t *testing.T, content, service string) api.DeploySpec {
	t.Helper()
	file := writeCompose(t, content)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan, ok := plans[service]
	if !ok {
		t.Fatalf("service %q missing from plan", service)
	}
	return plan.Spec
}

// TestDeployRestartPolicyCondition asserts each restart_policy.condition maps to
// the expected restart word and that max_attempts is carried onto the spec.
func TestDeployRestartPolicyCondition(t *testing.T) {
	cases := map[string]string{
		"none":       "no",
		"on-failure": "on-failure",
		"any":        "always",
	}
	for cond, want := range cases {
		spec := planSpec(t, `
services:
  web:
    image: nginx
    deploy:
      restart_policy:
        condition: `+cond+`
        max_attempts: 5
`, "web")
		if spec.Restart != want {
			t.Errorf("condition %q -> Restart %q, want %q", cond, spec.Restart, want)
		}
		if spec.RestartMaxAttempts != 5 {
			t.Errorf("condition %q -> RestartMaxAttempts %d, want 5", cond, spec.RestartMaxAttempts)
		}
	}
}

// TestDeployRestartPolicyOverridesServiceRestart asserts deploy.restart_policy is
// authoritative over the service-level restart: when both are present.
func TestDeployRestartPolicyOverridesServiceRestart(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    restart: unless-stopped
    deploy:
      restart_policy:
        condition: on-failure
`, "web")
	if spec.Restart != "on-failure" {
		t.Errorf("Restart = %q, want on-failure (deploy.restart_policy wins)", spec.Restart)
	}
}

// TestDeployRestartPolicyEmptyConditionKeepsServiceRestart asserts that a
// restart_policy without a condition (e.g. only max_attempts) leaves the
// service-level restart: intact.
func TestDeployRestartPolicyEmptyConditionKeepsServiceRestart(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    restart: always
    deploy:
      restart_policy:
        max_attempts: 3
`, "web")
	if spec.Restart != "always" {
		t.Errorf("Restart = %q, want always (kept from service restart:)", spec.Restart)
	}
	if spec.RestartMaxAttempts != 3 {
		t.Errorf("RestartMaxAttempts = %d, want 3", spec.RestartMaxAttempts)
	}
}

// TestDeployReservations asserts deploy.resources.reservations populate the
// Resources reservation axes alongside the limits.
func TestDeployReservations(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 512m
        reservations:
          cpus: "0.25"
          memory: 128m
`, "web")
	r := spec.Resources
	if r == nil {
		t.Fatal("Resources = nil")
	}
	if r.CPULimit != 1.0 || r.MemoryLimit != 512*1024*1024 {
		t.Errorf("limits = cpu %v mem %d, want 1.0 / 512Mi", r.CPULimit, r.MemoryLimit)
	}
	if r.ReservedCPU != 0.25 || r.ReservedMemory != 128*1024*1024 {
		t.Errorf("reservations = cpu %v mem %d, want 0.25 / 128Mi", r.ReservedCPU, r.ReservedMemory)
	}
}

// TestDeployReservationsOnly asserts reservations alone (no limits) still yield a
// non-nil Resources.
func TestDeployReservationsOnly(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    deploy:
      resources:
        reservations:
          memory: 64m
`, "web")
	r := spec.Resources
	if r == nil || r.ReservedMemory != 64*1024*1024 {
		t.Fatalf("Resources = %+v, want ReservedMemory=64Mi", r)
	}
	if r.MemoryLimit != 0 || r.CPULimit != 0 {
		t.Errorf("limits should be unset, got cpu %v mem %d", r.CPULimit, r.MemoryLimit)
	}
}

// TestDeployLabelsMerge asserts deploy.labels and service labels both land in the
// single Labels map, with deploy.labels winning on a key clash.
func TestDeployLabelsMerge(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    labels:
      role: web
      shared: from-service
    deploy:
      labels:
        tier: frontend
        shared: from-deploy
`, "web")
	want := map[string]string{
		"role":   "web",
		"tier":   "frontend",
		"shared": "from-deploy",
	}
	if !reflect.DeepEqual(spec.Labels, want) {
		t.Errorf("Labels = %v, want %v", spec.Labels, want)
	}
}

// TestDeployLabelsListForm asserts the KEY=VALUE list form of deploy.labels also
// merges in.
func TestDeployLabelsListForm(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    deploy:
      labels:
        - tier=frontend
`, "web")
	if spec.Labels["tier"] != "frontend" {
		t.Errorf("Labels[tier] = %q, want frontend", spec.Labels["tier"])
	}
}

// TestDeployUpdateConfig asserts update_config parses and its mappable fields
// (parallelism, order) reach the api.UpdateConfig on the spec.
func TestDeployUpdateConfig(t *testing.T) {
	spec := planSpec(t, `
services:
  web:
    image: nginx
    deploy:
      update_config:
        parallelism: 2
        order: start-first
        delay: 10s
        monitor: 30s
        max_failure_ratio: 0.1
`, "web")
	uc := spec.UpdateConfig
	if uc == nil {
		t.Fatal("UpdateConfig = nil")
	}
	if uc.Parallelism != 2 {
		t.Errorf("Parallelism = %d, want 2", uc.Parallelism)
	}
	if uc.Order != "start-first" {
		t.Errorf("Order = %q, want start-first", uc.Order)
	}
}

// TestDeployKeysNoWarn asserts the newly-supported deploy sub-keys are not
// reported as ignored fields.
func TestDeployKeysNoWarn(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    deploy:
      replicas: 2
      restart_policy:
        condition: on-failure
      labels:
        tier: frontend
      update_config:
        parallelism: 1
      resources:
        limits:
          cpus: "0.5"
        reservations:
          memory: 64m
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, field := range []string{"deploy.restart_policy", "deploy.labels", "deploy.update_config", "deploy.resources"} {
		for _, ln := range lines {
			if strings.Contains(ln, `field "`+field+`"`) {
				t.Errorf("unexpected warning for supported field %q: %s", field, ln)
			}
		}
	}
}

// TestDeploySwarmOnlyKeysStillWarn asserts the four swarm-orchestrator-only
// deploy sub-keys remain in the warn-not-drop path (they are honestly reported as
// unsupported, not silently dropped, and not added to supportedDeployFields).
func TestDeploySwarmOnlyKeysStillWarn(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    deploy:
      mode: replicated
      endpoint_mode: vip
      placement:
        constraints: [node.role == manager]
      rollback_config:
        parallelism: 1
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, field := range []string{"deploy.mode", "deploy.endpoint_mode", "deploy.placement", "deploy.rollback_config"} {
		found := false
		for _, ln := range lines {
			if strings.Contains(ln, `field "`+field+`"`) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a warning for swarm-only field %q, got: %v", field, lines)
		}
	}
}

// TestDeployMergeNewFields asserts a two-file merge combines the new deploy
// sub-keys per the compose-spec merge rules.
func TestDeployMergeNewFields(t *testing.T) {
	base := mergeDeploy(
		&Deploy{
			RestartPolicy: &DeployRestartPolicy{Condition: "on-failure", MaxAttempts: 3},
			Labels:        Labels{"a": "1", "shared": "base"},
			UpdateConfig:  &UpdateConfig{Parallelism: 1, Order: "stop-first"},
			Resources:     &DeployResources{Reservations: &ResourceLimits{Memory: "64m"}},
		},
		&Deploy{
			RestartPolicy: &DeployRestartPolicy{MaxAttempts: 9},
			Labels:        Labels{"b": "2", "shared": "override"},
			UpdateConfig:  &UpdateConfig{Order: "start-first"},
			Resources:     &DeployResources{Reservations: &ResourceLimits{Cpus: "0.5"}},
		},
	)
	if base.RestartPolicy.Condition != "on-failure" || base.RestartPolicy.MaxAttempts != 9 {
		t.Errorf("restart_policy merge = %+v, want condition on-failure, max_attempts 9", base.RestartPolicy)
	}
	if base.Labels["a"] != "1" || base.Labels["b"] != "2" || base.Labels["shared"] != "override" {
		t.Errorf("labels merge = %v, want a=1 b=2 shared=override", base.Labels)
	}
	if base.UpdateConfig.Parallelism != 1 || base.UpdateConfig.Order != "start-first" {
		t.Errorf("update_config merge = %+v, want parallelism 1, order start-first", base.UpdateConfig)
	}
	if base.Resources.Reservations.Memory != "64m" || base.Resources.Reservations.Cpus != "0.5" {
		t.Errorf("reservations merge = %+v, want memory 64m, cpus 0.5", base.Resources.Reservations)
	}
}
