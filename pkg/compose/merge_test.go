package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// writeMergeFiles writes named files into a temp dir and returns their absolute
// paths in the given order, mirroring writeFiles but for multi-file merge tests.
func writeMergeFiles(t *testing.T, order []string, files map[string]string) []string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths := make([]string, len(order))
	for i, name := range order {
		paths[i] = filepath.Join(dir, name)
	}
	return paths
}

func TestMergeServiceDeep(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
name: shop
services:
  web:
    image: base/web:v1
    container_name: web1
    command: ["run", "base"]
    environment:
      SHARED: base
      ONLY_BASE: baseval
    ports:
      - "8080:80"
      - "1234:1234"
    volumes:
      - "./data:/data"
    deploy:
      replicas: 2
      resources:
        limits:
          cpus: "0.5"
          memory: 256M
    healthcheck:
      test: ["CMD", "base-probe"]
      interval: 10s
      retries: 3
    depends_on:
      db:
        condition: service_started
`,
		"override.yaml": `
services:
  web:
    command: ["run", "override"]
    environment:
      SHARED: override
      ONLY_OVERRIDE: yep
    ports:
      - "8080:80"
      - "9090:90"
    volumes:
      - "./more:/more"
    deploy:
      replicas: 5
      resources:
        limits:
          memory: 512M
    healthcheck:
      test: ["CMD", "override-probe"]
      timeout: 4s
    depends_on:
      db:
        condition: service_healthy
`,
	})

	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	web, ok := p.Services()["web"]
	if !ok {
		t.Fatal("web service missing")
	}

	// Scalar overridden by non-empty override.
	if web.Image != "base/web:v1" {
		t.Errorf("image: base scalar wiped, got %q", web.Image)
	}
	if web.ContainerName != "web1" {
		t.Errorf("container_name: base scalar wiped, got %q", web.ContainerName)
	}

	// Environment maps merge; override wins on conflict, base-only key kept.
	wantEnv := map[string]string{"SHARED": "override", "ONLY_BASE": "baseval", "ONLY_OVERRIDE": "yep"}
	if !reflect.DeepEqual(map[string]string(web.Environment), wantEnv) {
		t.Errorf("environment merge = %v, want %v", web.Environment, wantEnv)
	}

	// command replaced (not concatenated).
	if !reflect.DeepEqual([]string(web.Command), []string{"run", "override"}) {
		t.Errorf("command = %v, want [run override]", web.Command)
	}

	// ports append with the exact-equal "8080:80" deduped.
	var portKeys []string
	for _, pt := range web.Ports {
		portKeys = append(portKeys, portKey(pt))
	}
	sort.Strings(portKeys)
	wantPorts := []string{"1234->1234", "8080->80", "9090->90"}
	if !reflect.DeepEqual(portKeys, wantPorts) {
		t.Errorf("ports = %v, want %v", portKeys, wantPorts)
	}

	// volumes append (both present).
	if len(web.Volumes) != 2 {
		t.Errorf("volumes = %v, want 2 entries", web.Volumes)
	}

	// deploy.replicas overridden; resources.limits merge field-wise (cpus from
	// base kept, memory from override).
	if web.Deploy == nil || web.Deploy.Replicas != 5 {
		t.Errorf("deploy.replicas = %+v, want 5", web.Deploy)
	}
	lim := web.Deploy.Resources.Limits
	if string(lim.Cpus) != "0.5" {
		t.Errorf("deploy limits cpus = %q, want 0.5 (from base)", lim.Cpus)
	}
	if string(lim.Memory) != "512M" {
		t.Errorf("deploy limits memory = %q, want 512M (from override)", lim.Memory)
	}

	// healthcheck fields merge; test replaced, base interval/retries kept,
	// override timeout added.
	hc := web.Healthcheck
	if !reflect.DeepEqual([]string(hc.Test), []string{"CMD", "override-probe"}) {
		t.Errorf("healthcheck.test = %v, want [CMD override-probe]", hc.Test)
	}
	if hc.Interval != "10s" {
		t.Errorf("healthcheck.interval = %q, want 10s (from base)", hc.Interval)
	}
	if hc.Retries != 3 {
		t.Errorf("healthcheck.retries = %d, want 3 (from base)", hc.Retries)
	}
	if hc.Timeout != "4s" {
		t.Errorf("healthcheck.timeout = %q, want 4s (from override)", hc.Timeout)
	}

	// depends_on merges by name; override condition wins.
	if len(web.DependsOn) != 1 || web.DependsOn[0].Service != "db" {
		t.Fatalf("depends_on = %+v, want single db", web.DependsOn)
	}
	if web.DependsOn[0].Condition != DependsOnHealthy {
		t.Errorf("depends_on condition = %q, want %q", web.DependsOn[0].Condition, DependsOnHealthy)
	}
}

// TestMergeScalarNotWiped confirms a base scalar survives when the override omits
// it (override provides no image).
func TestMergeScalarNotWiped(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
services:
  web:
    image: base/web:v1
    restart: always
`,
		"override.yaml": `
services:
  web:
    environment:
      X: "1"
`,
	})
	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	web := p.Services()["web"]
	if web.Image != "base/web:v1" {
		t.Errorf("image = %q, want base/web:v1 (base kept when override omits)", web.Image)
	}
	if web.Restart != "always" {
		t.Errorf("restart = %q, want always (base kept)", web.Restart)
	}
	if web.Environment["X"] != "1" {
		t.Errorf("environment X = %q, want 1 (override added)", web.Environment["X"])
	}
}

// TestMergeNewServiceAdded confirms a service present only in the override file
// is added, not merged.
func TestMergeNewServiceAdded(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
services:
  web:
    image: web:v1
`,
		"override.yaml": `
services:
  cache:
    image: redis:7
`,
	})
	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Services()) != 2 {
		t.Fatalf("services = %v, want web and cache", p.Services())
	}
	if p.Services()["cache"].Image != "redis:7" {
		t.Errorf("cache image = %q", p.Services()["cache"].Image)
	}
}

// TestMergeServiceEgressIngress pins that a later file's service-level
// x-cornus-egress / x-cornus-ingress block is applied (wholesale) when merging
// over an earlier definition of the same service — an override block that the
// base lacks used to be silently dropped.
func TestMergeServiceEgressIngress(t *testing.T) {
	files := writeMergeFiles(t, []string{"base.yaml", "override.yaml"}, map[string]string{
		"base.yaml": `
services:
  web:
    image: web:v1
`,
		"override.yaml": `
services:
  web:
    x-cornus-egress:
      mode: proxy
    x-cornus-ingress:
      host: web.example.com
`,
	})
	p, err := Load(files...)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	web := p.Services()["web"]
	if web.Image != "web:v1" {
		t.Errorf("image = %q, want web:v1 (base scalar kept)", web.Image)
	}
	if web.Egress == nil || web.Egress.Mode != "proxy" {
		t.Errorf("egress = %+v, want mode proxy from the override file", web.Egress)
	}
	if web.Ingress == nil || web.Ingress.Host != "web.example.com" {
		t.Errorf("ingress = %+v, want host web.example.com from the override file", web.Ingress)
	}
}

// portKey renders a Port as "host->container" for order-independent comparison.
func portKey(p Port) string {
	return strconv.Itoa(p.Host) + "->" + strconv.Itoa(p.Container)
}
