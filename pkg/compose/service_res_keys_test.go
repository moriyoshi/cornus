package compose

import (
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// TestResourceKeysTranslate asserts the resource & host-namespace batch
// (ulimits, tmpfs, devices, shm_size, pid, ipc, mem_limit, cpus) parses and
// populates the matching api.DeploySpec fields.
func TestResourceKeysTranslate(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    ulimits:
      nproc: 65535
      nofile:
        soft: 20000
        hard: 40000
    tmpfs:
      - /run
      - /tmp:size=64m
    devices:
      - /dev/ttyUSB0:/dev/ttyUSB0:rwm
      - /dev/null
    shm_size: 128m
    pid: host
    ipc: shareable
    mem_limit: 512m
    cpus: 1.5
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	spec := plans["web"].Spec

	// ulimits: shorthand (soft==hard) and full form, sorted by name.
	want := []api.Ulimit{
		{Name: "nofile", Soft: 20000, Hard: 40000},
		{Name: "nproc", Soft: 65535, Hard: 65535},
	}
	if !reflect.DeepEqual(spec.Ulimits, want) {
		t.Errorf("Ulimits = %+v, want %+v", spec.Ulimits, want)
	}
	if !reflect.DeepEqual(spec.Tmpfs, []string{"/run", "/tmp:size=64m"}) {
		t.Errorf("Tmpfs = %v", spec.Tmpfs)
	}
	if !reflect.DeepEqual(spec.Devices, []string{"/dev/ttyUSB0:/dev/ttyUSB0:rwm", "/dev/null"}) {
		t.Errorf("Devices = %v", spec.Devices)
	}
	if spec.ShmSize != 128*1024*1024 {
		t.Errorf("ShmSize = %d, want %d", spec.ShmSize, 128*1024*1024)
	}
	if spec.PIDMode != "host" {
		t.Errorf("PIDMode = %q, want host", spec.PIDMode)
	}
	if spec.IPCMode != "shareable" {
		t.Errorf("IPCMode = %q, want shareable", spec.IPCMode)
	}
	// mem_limit / cpus route into the shared Resources limits.
	if spec.Resources == nil {
		t.Fatalf("Resources = nil, want mem_limit/cpus populated")
	}
	if spec.Resources.MemoryLimit != 512*1024*1024 {
		t.Errorf("Resources.MemoryLimit = %d, want %d", spec.Resources.MemoryLimit, 512*1024*1024)
	}
	if spec.Resources.CPULimit != 1.5 {
		t.Errorf("Resources.CPULimit = %v, want 1.5", spec.Resources.CPULimit)
	}
}

// TestUlimitsShorthandOnly covers the bare-integer shorthand alone.
func TestUlimitsShorthandOnly(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    ulimits:
      nofile: 1024
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plans["web"].Spec.Ulimits
	want := []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ulimits = %+v, want %+v", got, want)
	}
}

// TestTmpfsScalarForm asserts tmpfs accepts a single string (not just a list).
func TestTmpfsScalarForm(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    tmpfs: /run
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !reflect.DeepEqual(plans["web"].Spec.Tmpfs, []string{"/run"}) {
		t.Errorf("Tmpfs = %v, want [/run]", plans["web"].Spec.Tmpfs)
	}
}

// TestMemLimitCpusOnly asserts mem_limit/cpus populate Resources when there is
// no deploy.resources block at all.
func TestMemLimitCpusOnly(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    mem_limit: 256m
    cpus: "0.5"
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	r := plans["web"].Spec.Resources
	if r == nil || r.MemoryLimit != 256*1024*1024 || r.CPULimit != 0.5 {
		t.Fatalf("Resources = %+v, want mem=256Mi cpu=0.5", r)
	}
}

// TestDeployResourcesWinOverMemLimitCpus asserts deploy.resources.limits is
// authoritative when BOTH it and mem_limit/cpus are set (compose-spec).
func TestDeployResourcesWinOverMemLimitCpus(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    mem_limit: 512m
    cpus: 2.0
    deploy:
      resources:
        limits:
          memory: 256m
          cpus: "1.0"
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	r := plans["web"].Spec.Resources
	if r == nil {
		t.Fatal("Resources = nil")
	}
	// deploy.resources wins on both axes.
	if r.MemoryLimit != 256*1024*1024 {
		t.Errorf("MemoryLimit = %d, want %d (deploy wins)", r.MemoryLimit, 256*1024*1024)
	}
	if r.CPULimit != 1.0 {
		t.Errorf("CPULimit = %v, want 1.0 (deploy wins)", r.CPULimit)
	}
}

// TestDeployAndMemLimitPerAxis asserts the precedence is per-axis: deploy sets
// only cpus, so mem_limit still fills the memory axis.
func TestDeployAndMemLimitPerAxis(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    mem_limit: 512m
    deploy:
      resources:
        limits:
          cpus: "1.0"
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	r := plans["web"].Spec.Resources
	if r == nil || r.CPULimit != 1.0 || r.MemoryLimit != 512*1024*1024 {
		t.Fatalf("Resources = %+v, want cpu=1.0 (deploy) mem=512Mi (mem_limit)", r)
	}
}

// TestResourceKeysNoWarn asserts none of the eight keys is reported as an
// unsupported/ignored field by the loader.
func TestResourceKeysNoWarn(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    ulimits:
      nofile: 1024
    tmpfs:
      - /run
    devices:
      - /dev/null
    shm_size: 64m
    pid: host
    ipc: host
    mem_limit: 256m
    cpus: "0.5"
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, field := range []string{"ulimits", "tmpfs", "devices", "shm_size", "pid", "ipc", "mem_limit", "cpus"} {
		for _, ln := range lines {
			if strings.Contains(ln, `field "`+field+`"`) {
				t.Errorf("unexpected warning for supported field %q: %s", field, ln)
			}
		}
	}
}
