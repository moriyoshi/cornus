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

// loadMergeDoc writes files (in the given order) and returns the merged
// ProjectDocument — the direct output of the merge layer, before NewProject
// converts it, so a test can assert on the raw merged ServiceDocument.
func loadMergeDoc(t *testing.T, order []string, files map[string]string) *ProjectDocument {
	t.Helper()
	paths := writeMergeFiles(t, order, files)
	doc, err := LoadDocument(paths...)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	return doc
}

// TestMergeScalarPresenceWins covers the "an override key that is PRESENT wins,
// zero or not" rule: a later file can turn an inherited boolean back off and can
// explicitly clear a string, while an absent key still leaves the base alone.
func TestMergeScalarPresenceWins(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order []string
		files map[string]string
		want  func(t *testing.T, svc ServiceDocument)
	}{
		{
			name:  "privileged true turned off",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    privileged: true\n",
				"b.yaml": "services:\n  web:\n    privileged: false\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Privileged {
					t.Error("privileged = true, want false (later file turned it off)")
				}
				if svc.Image != "web:v1" {
					t.Errorf("image = %q, want web:v1 (untouched)", svc.Image)
				}
			},
		},
		{
			name:  "tty true turned off",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    tty: true\n",
				"b.yaml": "services:\n  web:\n    tty: false\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.TTY {
					t.Error("tty = true, want false")
				}
			},
		},
		{
			name:  "read_only true turned off",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    read_only: true\n",
				"b.yaml": "services:\n  web:\n    read_only: false\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.ReadOnly {
					t.Error("read_only = true, want false")
				}
			},
		},
		{
			name:  "stdin_open true turned off",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    stdin_open: true\n",
				"b.yaml": "services:\n  web:\n    stdin_open: false\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.StdinOpen {
					t.Error("stdin_open = true, want false")
				}
			},
		},
		{
			name:  "build.no_cache and healthcheck.disable turned off",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    build:\n      context: .\n      no_cache: true\n      pull: true\n    healthcheck:\n      test: [\"CMD\", \"p\"]\n      disable: true\n",
				"b.yaml": "services:\n  web:\n    build:\n      no_cache: false\n    healthcheck:\n      disable: false\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Build == nil || svc.Build.NoCache {
					t.Errorf("build.no_cache = %+v, want false", svc.Build)
				}
				if svc.Build.Context != "." || !svc.Build.Pull {
					t.Errorf("build = %+v, want context . and pull kept from the base", svc.Build)
				}
				if svc.Healthcheck == nil || svc.Healthcheck.Disable {
					t.Errorf("healthcheck.disable = %+v, want false", svc.Healthcheck)
				}
			},
		},
		{
			name:  "explicitly false then absent stays false",
			order: []string{"a.yaml", "b.yaml", "c.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    privileged: true\n",
				"b.yaml": "services:\n  web:\n    privileged: false\n",
				"c.yaml": "services:\n  web:\n    environment:\n      X: \"1\"\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Privileged {
					t.Error("privileged = true, want false (a later silent file must not resurrect it)")
				}
				if svc.Environment["X"] != "1" {
					t.Errorf("environment = %v, want X=1", svc.Environment)
				}
			},
		},
		{
			name:  "absent then absent stays zero",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n",
				"b.yaml": "services:\n  web:\n    restart: always\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Privileged || svc.TTY || svc.ReadOnly || svc.StdinOpen {
					t.Errorf("bool toggles = %+v, want all false", svc)
				}
				if svc.Init != nil {
					t.Errorf("init = %v, want nil", *svc.Init)
				}
			},
		},
		{
			name:  "explicit empty scalar clears the base",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    container_name: web1\n    user: root\n    hostname: h1\n",
				"b.yaml": "services:\n  web:\n    container_name: \"\"\n    user: \"\"\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.ContainerName != "" {
					t.Errorf("container_name = %q, want cleared", svc.ContainerName)
				}
				if svc.User != "" {
					t.Errorf("user = %q, want cleared", svc.User)
				}
				if svc.Hostname != "h1" {
					t.Errorf("hostname = %q, want h1 (not written by the override)", svc.Hostname)
				}
			},
		},
		{
			name:  "normal last-wins override is unchanged",
			order: []string{"a.yaml", "b.yaml"},
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    restart: always\n    user: root\n",
				"b.yaml": "services:\n  web:\n    image: web:v2\n    restart: on-failure\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Image != "web:v2" {
					t.Errorf("image = %q, want web:v2", svc.Image)
				}
				if svc.Restart != "on-failure" {
					t.Errorf("restart = %q, want on-failure", svc.Restart)
				}
				if svc.User != "root" {
					t.Errorf("user = %q, want root (base kept)", svc.User)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := loadMergeDoc(t, tc.order, tc.files)
			svc, ok := doc.Services["web"]
			if !ok {
				t.Fatal("web service missing")
			}
			tc.want(t, svc)
		})
	}
}

// TestMergeResetTag covers the compose-spec `!reset` tag: the override drops the
// inherited value entirely, for scalars, sequences, mappings, and whole blocks.
func TestMergeResetTag(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  func(t *testing.T, svc ServiceDocument)
	}{
		{
			name: "scalar",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    container_name: web1\n    privileged: true\n",
				"b.yaml": "services:\n  web:\n    container_name: !reset\n    privileged: !reset\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.ContainerName != "" {
					t.Errorf("container_name = %q, want reset", svc.ContainerName)
				}
				if svc.Privileged {
					t.Error("privileged = true, want reset to false")
				}
				if svc.Image != "web:v1" {
					t.Errorf("image = %q, want web:v1", svc.Image)
				}
			},
		},
		{
			name: "init pointer",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    init: true\n",
				"b.yaml": "services:\n  web:\n    init: !reset\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Init != nil {
					t.Errorf("init = %v, want nil", *svc.Init)
				}
			},
		},
		{
			name: "sequence and mapping",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    ports:\n      - \"8080:80\"\n    volumes:\n      - \"./d:/d\"\n    environment:\n      A: \"1\"\n    labels:\n      k: v\n",
				"b.yaml": "services:\n  web:\n    ports: !reset\n    environment: !reset\n    labels: !reset\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if len(svc.Ports) != 0 {
					t.Errorf("ports = %v, want reset", svc.Ports)
				}
				if len(svc.Environment) != 0 {
					t.Errorf("environment = %v, want reset", svc.Environment)
				}
				if len(svc.Labels) != 0 {
					t.Errorf("labels = %v, want reset", svc.Labels)
				}
				if len(svc.Volumes) != 1 {
					t.Errorf("volumes = %v, want the base's single entry (not reset)", svc.Volumes)
				}
			},
		},
		{
			name: "command",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    command: [\"run\"]\n    entrypoint: [\"/e\"]\n",
				"b.yaml": "services:\n  web:\n    command: !reset\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if len(svc.Command) != 0 {
					t.Errorf("command = %v, want reset", svc.Command)
				}
				if len(svc.Entrypoint) != 1 {
					t.Errorf("entrypoint = %v, want the base's (not reset)", svc.Entrypoint)
				}
			},
		},
		{
			name: "nested blocks",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    build:\n      context: .\n    deploy:\n      replicas: 3\n    healthcheck:\n      test: [\"CMD\", \"p\"]\n",
				"b.yaml": "services:\n  web:\n    build: !reset\n    healthcheck: !reset\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Build != nil {
					t.Errorf("build = %+v, want reset to nil", svc.Build)
				}
				if svc.Healthcheck != nil {
					t.Errorf("healthcheck = %+v, want reset to nil", svc.Healthcheck)
				}
				if svc.Deploy == nil || svc.Deploy.Replicas != 3 {
					t.Errorf("deploy = %+v, want the base's replicas 3 (not reset)", svc.Deploy)
				}
			},
		},
		{
			name: "nested scalar inside deploy",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    deploy:\n      replicas: 3\n      resources:\n        limits:\n          cpus: \"0.5\"\n          memory: 256M\n",
				"b.yaml": "services:\n  web:\n    deploy:\n      replicas: !reset\n      resources:\n        limits:\n          memory: !reset\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Deploy == nil || svc.Deploy.Replicas != 0 {
					t.Errorf("deploy.replicas = %+v, want reset to 0", svc.Deploy)
				}
				lim := svc.Deploy.Resources.Limits
				if lim.Memory != "" {
					t.Errorf("limits.memory = %q, want reset", lim.Memory)
				}
				if lim.Cpus != "0.5" {
					t.Errorf("limits.cpus = %q, want 0.5 (untouched)", lim.Cpus)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := loadMergeDoc(t, []string{"a.yaml", "b.yaml"}, tc.files)
			tc.want(t, doc.Services["web"])
		})
	}
}

// TestMergeOverrideTag covers the compose-spec `!override` tag: the override
// REPLACES the inherited value instead of merging into it.
func TestMergeOverrideTag(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  func(t *testing.T, svc ServiceDocument)
	}{
		{
			name: "sequence replaces instead of appending",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    ports:\n      - \"8080:80\"\n      - \"1234:1234\"\n    volumes:\n      - \"./d:/d\"\n",
				"b.yaml": "services:\n  web:\n    ports: !override\n      - \"9090:90\"\n    volumes:\n      - \"./e:/e\"\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				var keys []string
				for _, p := range svc.Ports {
					keys = append(keys, portKey(p))
				}
				if !reflect.DeepEqual(keys, []string{"9090->90"}) {
					t.Errorf("ports = %v, want only 9090->90", keys)
				}
				if len(svc.Volumes) != 2 {
					t.Errorf("volumes = %v, want 2 (untagged, still additive)", svc.Volumes)
				}
			},
		},
		{
			name: "mapping replaces instead of merging",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    environment:\n      A: \"1\"\n      B: \"2\"\n    labels:\n      k: v\n",
				"b.yaml": "services:\n  web:\n    environment: !override\n      C: \"3\"\n    labels:\n      k2: v2\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				want := map[string]string{"C": "3"}
				if !reflect.DeepEqual(map[string]string(svc.Environment), want) {
					t.Errorf("environment = %v, want %v", svc.Environment, want)
				}
				if len(svc.Labels) != 2 {
					t.Errorf("labels = %v, want both (untagged, still merged)", svc.Labels)
				}
			},
		},
		{
			name: "nested block replaces instead of field-merging",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    build:\n      context: .\n      target: dev\n      args:\n        A: \"1\"\n    deploy:\n      replicas: 3\n      labels:\n        k: v\n",
				"b.yaml": "services:\n  web:\n    build: !override\n      context: ./other\n    deploy:\n      labels:\n        k2: v2\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if svc.Build == nil || svc.Build.Context != "./other" {
					t.Fatalf("build = %+v, want context ./other", svc.Build)
				}
				if svc.Build.Target != "" || len(svc.Build.Args) != 0 {
					t.Errorf("build = %+v, want the base's target/args dropped", svc.Build)
				}
				if svc.Deploy == nil || svc.Deploy.Replicas != 3 || len(svc.Deploy.Labels) != 2 {
					t.Errorf("deploy = %+v, want field-merged (untagged)", svc.Deploy)
				}
			},
		},
		{
			name: "depends_on replaces instead of merging by name",
			files: map[string]string{
				"a.yaml": "services:\n  web:\n    image: web:v1\n    depends_on:\n      db:\n        condition: service_started\n      cache:\n        condition: service_started\n  db:\n    image: db\n  cache:\n    image: cache\n",
				"b.yaml": "services:\n  web:\n    depends_on: !override\n      db:\n        condition: service_healthy\n",
			},
			want: func(t *testing.T, svc ServiceDocument) {
				if len(svc.DependsOn) != 1 || svc.DependsOn[0].Service != "db" {
					t.Fatalf("depends_on = %+v, want only db", svc.DependsOn)
				}
				if svc.DependsOn[0].Condition != DependsOnHealthy {
					t.Errorf("depends_on condition = %q", svc.DependsOn[0].Condition)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := loadMergeDoc(t, []string{"a.yaml", "b.yaml"}, tc.files)
			tc.want(t, doc.Services["web"])
		})
	}
}

// TestMergeCollectionsUnchangedWithoutTags is the regression guard for the
// scope rule "do not change how lists/maps merge": without a `!reset` /
// `!override` tag, an override that writes an EMPTY sequence or mapping still
// merges (adding nothing) rather than clearing, and a non-empty one appends /
// overlays exactly as before.
func TestMergeCollectionsUnchangedWithoutTags(t *testing.T) {
	doc := loadMergeDoc(t, []string{"a.yaml", "b.yaml"}, map[string]string{
		"a.yaml": `
services:
  web:
    image: web:v1
    ports:
      - "8080:80"
      - "1234:1234"
    volumes:
      - "./d:/d"
    environment:
      A: base
      SHARED: base
    labels:
      k: v
    cap_add:
      - NET_ADMIN
    sysctls:
      net.core.somaxconn: "1024"
    networks:
      backend:
        aliases: [a1]
        ipv4_address: 10.0.0.5
    depends_on:
      db:
        condition: service_started
  db:
    image: db
networks:
  backend: {}
`,
		"b.yaml": `
services:
  web:
    ports: []
    volumes:
      - "./d:/d"
      - "./e:/e"
    environment: {}
    labels: {}
    cap_add: []
    sysctls: {}
    networks:
      backend:
        aliases: [a2]
    depends_on:
      db:
        condition: service_healthy
`,
	})
	svc := doc.Services["web"]

	var portKeys []string
	for _, p := range svc.Ports {
		portKeys = append(portKeys, portKey(p))
	}
	sort.Strings(portKeys)
	if !reflect.DeepEqual(portKeys, []string{"1234->1234", "8080->80"}) {
		t.Errorf("ports = %v, want the base's two kept (empty override list must not clear)", portKeys)
	}
	if len(svc.Volumes) != 2 {
		t.Errorf("volumes = %v, want 2 (append with the exact dupe dropped)", svc.Volumes)
	}
	wantEnv := map[string]string{"A": "base", "SHARED": "base"}
	if !reflect.DeepEqual(map[string]string(svc.Environment), wantEnv) {
		t.Errorf("environment = %v, want %v (empty override map must not clear)", svc.Environment, wantEnv)
	}
	if len(svc.Labels) != 1 || svc.Labels["k"] != "v" {
		t.Errorf("labels = %v, want k=v kept", svc.Labels)
	}
	if !reflect.DeepEqual(svc.CapAdd, []string{"NET_ADMIN"}) {
		t.Errorf("cap_add = %v, want NET_ADMIN kept", svc.CapAdd)
	}
	if svc.Sysctls["net.core.somaxconn"] != "1024" {
		t.Errorf("sysctls = %v, want the base entry kept", svc.Sysctls)
	}
	if len(svc.Networks) != 1 {
		t.Fatalf("networks = %+v, want a single backend attachment", svc.Networks)
	}
	if !reflect.DeepEqual(svc.Networks[0].Aliases, []string{"a1", "a2"}) {
		t.Errorf("network aliases = %v, want [a1 a2]", svc.Networks[0].Aliases)
	}
	if svc.Networks[0].IPv4Address != "10.0.0.5" {
		t.Errorf("network ipv4_address = %q, want the base's", svc.Networks[0].IPv4Address)
	}
	if len(svc.DependsOn) != 1 || svc.DependsOn[0].Condition != DependsOnHealthy {
		t.Errorf("depends_on = %+v, want db/service_healthy", svc.DependsOn)
	}
}

// TestMergeServiceGrantsAndTelemetry pins that an override file's service-level
// configs/secrets grants and x-cornus-telemetry block survive the merge — they
// used to be dropped because mergeService never re-applied them.
func TestMergeServiceGrantsAndTelemetry(t *testing.T) {
	doc := loadMergeDoc(t, []string{"a.yaml", "b.yaml"}, map[string]string{
		"a.yaml": "services:\n  web:\n    image: web:v1\n    configs:\n      - src\nconfigs:\n  src:\n    content: hi\n",
		"b.yaml": "services:\n  web:\n    secrets:\n      - tok\n    x-cornus-agent-forward: true\nsecrets:\n  tok:\n    environment: TOK\n",
	})
	svc := doc.Services["web"]
	if len(svc.Configs) != 1 || svc.Configs[0].Source != "src" {
		t.Errorf("configs = %+v, want the base's src grant kept", svc.Configs)
	}
	if len(svc.Secrets) != 1 || svc.Secrets[0].Source != "tok" {
		t.Errorf("secrets = %+v, want the override's tok grant applied", svc.Secrets)
	}
	if !svc.AgentForward {
		t.Error("x-cornus-agent-forward = false, want the override's true applied")
	}
}

// TestMergeExtendsPresence pins that the presence rule reaches `extends` too:
// an extending service that explicitly writes `privileged: false` turns the base
// service's `true` off, and `!reset` clears an inherited value.
func TestMergeExtendsPresence(t *testing.T) {
	paths := writeMergeFiles(t, []string{"compose.yaml"}, map[string]string{
		"compose.yaml": `
services:
  base:
    image: web:v1
    privileged: true
    container_name: base1
    environment:
      A: "1"
  web:
    extends:
      service: base
    privileged: false
    container_name: !reset
`,
	})
	doc, err := LoadDocument(paths...)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	web := doc.Services["web"]
	if web.Privileged {
		t.Error("privileged = true, want false (the extending service turned it off)")
	}
	if web.ContainerName != "" {
		t.Errorf("container_name = %q, want !reset to have cleared it", web.ContainerName)
	}
	if web.Image != "web:v1" || web.Environment["A"] != "1" {
		t.Errorf("web = %+v, want the base's image/environment inherited", web)
	}
}

// TestMergeTopLevelDefPresence covers the same presence rule on the top-level
// secrets/configs/volumes/networks definitions.
func TestMergeTopLevelDefPresence(t *testing.T) {
	doc := loadMergeDoc(t, []string{"a.yaml", "b.yaml"}, map[string]string{
		"a.yaml": `
services:
  web:
    image: web:v1
networks:
  backend:
    external: true
    attachable: true
    internal: true
    labels:
      k: v
volumes:
  data:
    external: true
    driver: local
`,
		"b.yaml": `
networks:
  backend:
    external: false
    attachable: false
volumes:
  data:
    external: false
`,
	})
	net := doc.Networks["backend"]
	if net.External {
		t.Error("network external = true, want false (later file turned it off)")
	}
	if net.Attachable {
		t.Error("network attachable = true, want false")
	}
	if !net.Internal {
		t.Error("network internal = false, want true (not written by the override)")
	}
	if net.Labels["k"] != "v" {
		t.Errorf("network labels = %v, want k=v kept", net.Labels)
	}
	vol := doc.Volumes["data"]
	if vol.External {
		t.Error("volume external = true, want false")
	}
	if vol.Driver != "local" {
		t.Errorf("volume driver = %q, want local kept", vol.Driver)
	}
}

// TestParseMergeMeta unit-tests the presence/tag harvest itself, including the
// YAML merge key (`<<:`), which contributes the anchored mapping's keys.
func TestParseMergeMeta(t *testing.T) {
	src := `
x-common: &common
  privileged: true
  user: root
services:
  web:
    <<: *common
    image: web:v1
    tty: false
    ports: !override
      - "1:1"
    labels: !reset
`
	m, clean, err := parseMergeMeta([]byte(src))
	if err != nil {
		t.Fatalf("parseMergeMeta: %v", err)
	}
	web := m.at("services").at("web")
	for _, k := range []string{"image", "tty", "ports", "labels", "privileged", "user"} {
		if !web.has(k) {
			t.Errorf("key %q not recorded as present", k)
		}
	}
	if web.has("restart") {
		t.Error("key \"restart\" recorded as present but never written")
	}
	if !web.at("ports").replaced() {
		t.Error("ports: !override not recorded")
	}
	if !web.at("labels").cleared() {
		t.Error("labels: !reset not recorded")
	}
	if string(clean) == src {
		t.Error("tagged document was not re-emitted with its tags resolved")
	}
	// Nil metadata is inert: every accessor is safe and reports nothing.
	var nilMeta *yamlMeta
	if nilMeta.has("x") || nilMeta.cleared() || nilMeta.replaced() || nilMeta.at("x") != nil {
		t.Error("nil *yamlMeta must report no presence and no tags")
	}
	// An untagged document is handed to the typed decode byte-for-byte.
	plain := "services:\n  web:\n    image: web:v1\n"
	_, same, err := parseMergeMeta([]byte(plain))
	if err != nil {
		t.Fatalf("parseMergeMeta: %v", err)
	}
	if string(same) != plain {
		t.Errorf("untagged document was re-emitted:\n%s", same)
	}
}
