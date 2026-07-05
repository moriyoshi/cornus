package compose

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// captureWarnings runs fn with slog's default logger redirected to a buffer and
// returns the lines it logged, so tests can assert on unsupported-field warnings.
func captureWarnings(t *testing.T, fn func()) []string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	var lines []string
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if ln != "" {
			// slog's TextHandler escapes the inner quotes (\"); unescape so tests
			// can match the human-readable message.
			lines = append(lines, strings.ReplaceAll(ln, `\"`, `"`))
		}
	}
	return lines
}

// warnedFields returns the set of `service:field` pairs mentioned in warnings.
func warnedFields(lines []string) map[string]bool {
	out := map[string]bool{}
	for _, ln := range lines {
		out[ln] = true
	}
	return out
}

func TestLoadWarnsUnsupportedFields(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    healthcheck:
      test: ["CMD", "true"]
    user: "1000"
    working_dir: /app
    labels:
      a: b
    cgroup_parent: /custom
    deploy:
      replicas: 2
      placement:
        constraints: [node.role == manager]
      resources:
        limits:
          cpus: "0.5"
  api:
    image: api
    cgroup_parent: /custom
`)
	var proj *Project
	lines := captureWarnings(t, func() {
		var err error
		proj, err = Load(file)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if proj == nil {
		t.Fatal("nil project")
	}

	warns := warnedFields(lines)
	mustWarn := func(substr string) {
		t.Helper()
		found := false
		for ln := range warns {
			if strings.Contains(ln, substr) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a warning containing %q, got: %v", substr, lines)
		}
	}
	mustNotWarn := func(substr string) {
		t.Helper()
		for ln := range warns {
			if strings.Contains(ln, substr) {
				t.Errorf("unexpected warning containing %q: %v", substr, lines)
			}
		}
	}
	// Supported fields must not warn.
	for ln := range warns {
		if strings.Contains(ln, `field "image"`) || strings.Contains(ln, `field "deploy"`) {
			t.Errorf("unexpected warning for supported field: %s", ln)
		}
	}
	// healthcheck and deploy.resources are now supported and must not warn.
	mustNotWarn(`field "healthcheck"`)
	mustNotWarn(`field "deploy.resources"`)
	// user, working_dir, and labels are now plumbed through and must not warn.
	mustNotWarn(`field "user"`)
	mustNotWarn(`field "working_dir"`)
	mustNotWarn(`field "labels"`)
	mustWarn(`service "web": field "cgroup_parent"`)
	mustWarn(`service "web": field "deploy.placement"`)
	// Same field on a different service warns under that service's name.
	mustWarn(`service "api": field "cgroup_parent"`)

	// The same (service, field) is reported at most once.
	count := 0
	for _, ln := range lines {
		if strings.Contains(ln, `service "web": field "cgroup_parent"`) {
			count++
		}
	}
	if count != 1 {
		t.Errorf("web cgroup_parent warned %d times, want 1", count)
	}
}

func TestLoadCleanFileNoWarnings(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    command: ["nginx", "-g", "daemon off;"]
    ports:
      - "8080:80"
    environment:
      LOG_LEVEL: info
    volumes:
      - ./data:/data
    deploy:
      replicas: 3
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if len(lines) != 0 {
		t.Errorf("expected no warnings for a clean file, got: %v", lines)
	}
}

func TestResolveMounts(t *testing.T) {
	plan := ServicePlan{Spec: api.DeploySpec{Mounts: []api.Mount{
		{Source: "./data", Target: "/data", ReadOnly: true},
		{Source: "sub/dir", Target: "/sub"},
		{Source: "/abs/keep", Target: "/keep"},
	}}}
	plan.ResolveMounts("/project/dir")
	if plan.Spec.Mounts[0].Source != "/project/dir/data" {
		t.Errorf("relative './data' = %q, want /project/dir/data", plan.Spec.Mounts[0].Source)
	}
	if plan.Spec.Mounts[1].Source != "/project/dir/sub/dir" {
		t.Errorf("relative 'sub/dir' = %q, want /project/dir/sub/dir", plan.Spec.Mounts[1].Source)
	}
	if plan.Spec.Mounts[2].Source != "/abs/keep" {
		t.Errorf("absolute source changed: %q", plan.Spec.Mounts[2].Source)
	}
}

func writeCompose(t *testing.T, content string) string {
	t.Helper()
	return writeFiles(t, map[string]string{"compose.yaml": content})
}

// writeFiles writes the given files into a temp dir and returns the compose.yaml
// path within it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "compose.yaml")
}

func TestTranslate(t *testing.T) {
	file := writeCompose(t, `
name: shop
services:
  web:
    image: localhost:5000/web:v1
    command: ["nginx", "-g", "daemon off;"]
    environment:
      LOG_LEVEL: info
      DEBUG: "true"
    ports:
      - "8080:80"
      - "443:443/tcp"
    volumes:
      - "./data:/data:ro"
      - "cache:/var/cache"
    restart: unless-stopped
    deploy:
      replicas: 3
    depends_on:
      - db
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=secret
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if project.Name() != "shop" {
		t.Fatalf("name = %q", project.Name())
	}

	// db must come before web in dependency order.
	order, err := project.Order()
	if err != nil {
		t.Fatalf("Order: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"db", "web"}) {
		t.Fatalf("order = %v, want [db web]", order)
	}

	plans, err := project.Plan("shop")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	web := plans["web"].Spec
	if plans["web"].Resource != "shop-web" {
		t.Fatalf("resource = %q", plans["web"].Resource)
	}
	if web.Image != "localhost:5000/web:v1" {
		t.Fatalf("image = %q", web.Image)
	}
	if !reflect.DeepEqual([]string(web.Command), []string{"nginx", "-g", "daemon off;"}) {
		t.Fatalf("command = %v", web.Command)
	}
	if web.Env["LOG_LEVEL"] != "info" || web.Env["DEBUG"] != "true" {
		t.Fatalf("env = %v", web.Env)
	}
	if web.Restart != "unless-stopped" || web.Replicas != 3 {
		t.Fatalf("restart/replicas = %q %d", web.Restart, web.Replicas)
	}
	if len(web.Ports) != 2 || web.Ports[0].Host != 8080 || web.Ports[0].Container != 80 || web.Ports[1].Container != 443 {
		t.Fatalf("ports = %+v", web.Ports)
	}
	// Only the bind mount survives; the named volume "cache" is dropped.
	if len(web.Mounts) != 1 || web.Mounts[0].Source != "./data" || web.Mounts[0].Target != "/data" || !web.Mounts[0].ReadOnly {
		t.Fatalf("mounts = %+v", web.Mounts)
	}

	db := plans["db"].Spec
	if db.Env["POSTGRES_PASSWORD"] != "secret" {
		t.Fatalf("db env (list form) = %v", db.Env)
	}
}

func TestAnonymousVolume(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    image: app
    volumes:
      - "/data"
      - "./h:/c"
      - "vol:/named"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	spec := plans["app"].Spec

	// The anonymous volume "/data" (empty Name) and the named "vol:/named"
	// (project-scoped Name) both become managed VolumeSpecs, not bind mounts.
	if len(spec.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want two (anon /data + named vol)", spec.Volumes)
	}
	var anon, named *api.VolumeSpec
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == "" {
			anon = &spec.Volumes[i]
		} else {
			named = &spec.Volumes[i]
		}
	}
	if anon == nil || anon.Target != "/data" || anon.Size != "" || anon.StorageClass != "" {
		t.Fatalf("anon volume = %+v, want {Target:/data} with no size/class", anon)
	}
	if named == nil || named.Name != "p_vol" || named.Target != "/named" {
		t.Fatalf("named volume = %+v, want {Name:p_vol, Target:/named}", named)
	}
	// The bind "./h:/c" is still a Mount.
	if len(spec.Mounts) != 1 || spec.Mounts[0].Source != "./h" || spec.Mounts[0].Target != "/c" {
		t.Fatalf("mounts = %+v, want one bind ./h:/c", spec.Mounts)
	}
}

// TestNetworks covers `networks:` parsing and translation: list and map (with
// aliases) service forms, top-level resolution (project scoping, `name:`
// override, `external:`), driver/driver_opts threading (incl. scalar
// stringification), and the implicit default network for services that list
// none.
func TestNetworks(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: web
    container_name: webbox
    networks:
      frontend:
        aliases: [www, web-alias]
      backend:
  db:
    image: db
    networks: [backend, ext]
  lonely:
    image: lonely
networks:
  frontend:
    driver: bridge
    driver_opts:
      mtu: 1450
  backend:
    name: literal-back
  ext:
    external: true
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// web: map form, sorted by name => backend first, then frontend.
	web := plans["web"].Spec.Networks
	if len(web) != 2 {
		t.Fatalf("web networks = %+v, want 2", web)
	}
	if web[0].Name != "literal-back" {
		t.Errorf("web[0] = %+v, want name: override literal-back", web[0])
	}
	fe := web[1]
	if fe.Name != "proj_frontend" || fe.Driver != "bridge" {
		t.Errorf("web[1] = %+v, want proj_frontend/bridge", fe)
	}
	if fe.DriverOpts["mtu"] != "1450" {
		t.Errorf("driver_opts = %v, want mtu stringified to 1450", fe.DriverOpts)
	}
	// Aliases: service name first, container_name, then declared.
	wantAliases := []string{"web", "webbox", "www", "web-alias"}
	if !reflect.DeepEqual(fe.Aliases, wantAliases) {
		t.Errorf("web frontend aliases = %v, want %v", fe.Aliases, wantAliases)
	}

	// db: list form; external network keeps its literal name, unscoped.
	db := plans["db"].Spec.Networks
	if len(db) != 2 || db[0].Name != "literal-back" || db[1].Name != "ext" {
		t.Errorf("db networks = %+v, want [literal-back ext]", db)
	}
	if !reflect.DeepEqual(db[0].Aliases, []string{"db"}) {
		t.Errorf("db aliases = %v, want [db]", db[0].Aliases)
	}

	// lonely: no networks block => the implicit project default.
	lonely := plans["lonely"].Spec.Networks
	if len(lonely) != 1 || lonely[0].Name != "proj_default" {
		t.Errorf("lonely networks = %+v, want the implicit proj_default", lonely)
	}
}

// TestProxyAllowTable covers the compose-plan-time control plane: a service on a
// proxy-enabled network gets a ProxySpec whose Allow lists its same-network
// peers (and nothing else); a service on no proxy network gets no ProxySpec.
func TestProxyAllowTable(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: web
    networks: [front, mesh]
  api:
    image: api
    networks: [mesh]
  db:
    image: db
    networks: [back]
  cache:
    image: cache
    networks: [mesh, back]
networks:
  front:
  back:
  mesh:
    driver_opts:
      proxy: "true"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// web is on mesh (proxied) => allow = its co-members across ALL its networks
	// (front has only web; mesh has api+cache) => [api, cache].
	if p := plans["web"].Spec.Proxy; p == nil {
		t.Fatal("web should be proxied (on the mesh network)")
	} else if got := p.Allow; !reflect.DeepEqual(got, []string{"api", "cache"}) {
		t.Errorf("web allow = %v, want [api cache]", got)
	}
	// cache is on mesh (proxied) + back => allow = mesh peers (web, api) + back peers (db).
	if p := plans["cache"].Spec.Proxy; p == nil || !reflect.DeepEqual(p.Allow, []string{"api", "db", "web"}) {
		t.Errorf("cache allow = %v, want [api db web]", allowOf(p))
	}
	// db is only on back (not proxied) => no ProxySpec.
	if plans["db"].Spec.Proxy != nil {
		t.Errorf("db is on no proxy network; want no ProxySpec, got %+v", plans["db"].Spec.Proxy)
	}
}

// TestProxyCooperativeMode covers the lighter proxy variant: a network with
// `driver_opts: {proxy: "true", mode: "cooperative"}` yields a ProxySpec in
// cooperative mode carrying each peer's container ports (from `ports:`/`expose:`)
// so the sidecar knows which loopback ports to listen on.
func TestProxyCooperativeMode(t *testing.T) {
	file := writeCompose(t, `
services:
  client:
    image: client
    networks: [mesh]
  web:
    image: web
    expose: ["80"]
    networks: [mesh]
  api:
    image: api
    ports: ["8080:9000"]
    networks: [mesh]
networks:
  mesh:
    driver_opts:
      proxy: "true"
      mode: cooperative
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	p := plans["client"].Spec.Proxy
	if p == nil {
		t.Fatal("client should be proxied")
	}
	if p.Mode != "cooperative" {
		t.Errorf("mode = %q, want cooperative", p.Mode)
	}
	if !reflect.DeepEqual(p.Allow, []string{"api", "web"}) {
		t.Errorf("allow = %v, want [api web]", p.Allow)
	}
	// web exposes 80; api's published container port is 9000 (not the host 8080).
	if !reflect.DeepEqual(p.Ports["web"], []int{80}) {
		t.Errorf("web ports = %v, want [80]", p.Ports["web"])
	}
	if !reflect.DeepEqual(p.Ports["api"], []int{9000}) {
		t.Errorf("api ports = %v, want [9000]", p.Ports["api"])
	}
}

func allowOf(p *api.ProxySpec) []string {
	if p == nil {
		return nil
	}
	return p.Allow
}

// TestNamedVolume covers the top-level `volumes:` resolution rules: a plain
// declaration is project-scoped, `name:` overrides the backing name verbatim,
// and `external: true` keeps the literal name (cornus neither scopes nor
// provisions it).
func TestNamedVolume(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    image: app
    volumes:
      - "scoped:/a"
      - "renamed:/b"
      - "ext:/c"
volumes:
  scoped:
  renamed:
    name: literal-name
  ext:
    external: true
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	spec := plans["app"].Spec

	got := map[string]string{} // target -> resolved name
	for _, v := range spec.Volumes {
		got[v.Target] = v.Name
	}
	want := map[string]string{
		"/a": "proj_scoped",  // default project scoping
		"/b": "literal-name", // name: override
		"/c": "ext",          // external: literal name, unscoped
	}
	for target, wantName := range want {
		if got[target] != wantName {
			t.Errorf("volume at %s: Name = %q, want %q (all: %+v)", target, got[target], wantName, spec.Volumes)
		}
	}
	if len(spec.Mounts) != 0 {
		t.Fatalf("named volumes must not become bind mounts: %+v", spec.Mounts)
	}
}

func TestPrivileged(t *testing.T) {
	file := writeCompose(t, `
services:
  priv:
    image: app
    privileged: true
  normal:
    image: app
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plans["priv"].Spec.Privileged {
		t.Fatalf("priv service should be privileged")
	}
	if plans["normal"].Spec.Privileged {
		t.Fatalf("normal service should not be privileged")
	}
}

func TestBuildSectionAndDefaultName(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: ./app
      dockerfile: Dockerfile.prod
      args:
        VERSION: "1.2.3"
    ports:
      - "9000"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Default project name derives from the compose file's directory.
	name := project.ResolveName(filepath.Dir(file))
	if name == "" {
		t.Fatal("empty project name")
	}
	plans, err := project.Plan(name)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	app := plans["app"]
	if app.Build == nil || app.Build.Context != "./app" || app.Build.Dockerfile != "Dockerfile.prod" {
		t.Fatalf("build = %+v", app.Build)
	}
	if app.Build.Args["VERSION"] != "1.2.3" {
		t.Fatalf("build args = %v", app.Build.Args)
	}
	// Bare container port -> published on the same host port.
	if len(app.Spec.Ports) != 1 || app.Spec.Ports[0].Host != 9000 || app.Spec.Ports[0].Container != 9000 {
		t.Fatalf("ports = %+v", app.Spec.Ports)
	}
}

func TestBuildTargetAndCacheFrom(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: ./app
      target: builder
      cache_from:
        - reg/app:cache
        - reg/app:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	name := project.ResolveName(filepath.Dir(file))
	plans, err := project.Plan(name)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	bp := plans["app"].Build
	if bp == nil {
		t.Fatal("build plan nil")
	}
	if bp.Target != "builder" {
		t.Errorf("BuildPlan.Target = %q want %q", bp.Target, "builder")
	}
	want := []string{"reg/app:cache", "reg/app:latest"}
	if len(bp.CacheFrom) != len(want) {
		t.Fatalf("BuildPlan.CacheFrom = %v want %v", bp.CacheFrom, want)
	}
	for i, w := range want {
		if bp.CacheFrom[i] != w {
			t.Errorf("CacheFrom[%d] = %q want %q", i, bp.CacheFrom[i], w)
		}
	}
}

// A bare cache_from string (not a list) is accepted.
func TestBuildCacheFromScalar(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: ./app
      cache_from: reg/app:cache
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan(project.ResolveName(filepath.Dir(file)))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	bp := plans["app"].Build
	if bp == nil || len(bp.CacheFrom) != 1 || bp.CacheFrom[0] != "reg/app:cache" {
		t.Fatalf("BuildPlan.CacheFrom = %v want [reg/app:cache]", bp.CacheFrom)
	}
}

func TestInterpolation(t *testing.T) {
	t.Setenv("NGINX_TAG", "1.29") // process env overrides the :- default
	file := writeFiles(t, map[string]string{
		".env": "WEB_PORT=9090\n",
		"compose.yaml": `
services:
  web:
    image: nginx:${NGINX_TAG:-1.27}
    environment:
      HOST_PORT: ${WEB_PORT}
      MISSING: ${UNSET_VAR:-fallback}
      LITERAL: $$NOT_A_VAR
    ports:
      - "${WEB_PORT}:80"
`,
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	web := plans["web"].Spec
	if web.Image != "nginx:1.29" {
		t.Fatalf("image = %q, want nginx:1.29", web.Image)
	}
	if web.Env["HOST_PORT"] != "9090" {
		t.Fatalf("HOST_PORT = %q (from .env)", web.Env["HOST_PORT"])
	}
	if web.Env["MISSING"] != "fallback" {
		t.Fatalf("MISSING = %q, want fallback", web.Env["MISSING"])
	}
	if web.Env["LITERAL"] != "$NOT_A_VAR" {
		t.Fatalf("LITERAL = %q, want $NOT_A_VAR ($$ escape)", web.Env["LITERAL"])
	}
	if len(web.Ports) != 1 || web.Ports[0].Host != 9090 {
		t.Fatalf("ports = %+v", web.Ports)
	}
}

func TestRequiredVariableError(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: ${CORNUS_MUST_BE_UNSET:?image is required}
`)
	if _, err := Load(file); err == nil {
		t.Fatal("expected an error for a required-but-unset variable")
	}
}

func TestEnvFile(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"web.env": "# a comment\nFOO=fromfile\nOVERRIDE=fromfile\nexport BAR=baz\nQUOTED=\"hello world\"\n",
		"compose.yaml": `
services:
  web:
    image: app
    env_file: ./web.env
    environment:
      OVERRIDE: inline
`,
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, _ := project.Plan("p")
	env := plans["web"].Spec.Env
	if env["FOO"] != "fromfile" || env["BAR"] != "baz" || env["QUOTED"] != "hello world" {
		t.Fatalf("env_file values = %v", env)
	}
	if env["OVERRIDE"] != "inline" {
		t.Fatalf("OVERRIDE = %q, want inline (environment overrides env_file)", env["OVERRIDE"])
	}
}

func TestEnvFileEscapeSequences(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"web.env": "DQ=\"line1\\nline2\\tcol\"\n" +
			"DQESC=\"a\\\\b\\\"c\"\n" +
			"SQ='raw\\nvalue'\n" +
			"UNQ=raw\\nvalue\n",
		"compose.yaml": `
services:
  web:
    image: app
    env_file: ./web.env
`,
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, _ := project.Plan("p")
	env := plans["web"].Spec.Env
	if got, want := env["DQ"], "line1\nline2\tcol"; got != want {
		t.Fatalf("DQ = %q, want %q", got, want)
	}
	if got, want := env["DQESC"], "a\\b\"c"; got != want {
		t.Fatalf("DQESC = %q, want %q", got, want)
	}
	if got, want := env["SQ"], "raw\\nvalue"; got != want {
		t.Fatalf("SQ = %q, want %q (single quotes are literal)", got, want)
	}
	if got, want := env["UNQ"], "raw\\nvalue"; got != want {
		t.Fatalf("UNQ = %q, want %q (unquoted is unaffected)", got, want)
	}
}

// TestExpandEnvEscapes pins the double-quoted .env escape set to compose-go/v2's
// dotenv expansion: the C-style single-char escapes, \$ -> $, octal \0NNN, and
// unknown escapes left literal.
func TestExpandEnvEscapes(t *testing.T) {
	cases := map[string]string{
		`a\nb`:     "a\nb",
		`a\tb`:     "a\tb",
		`\a\b\f\v`: "\a\b\f\v",
		`\$var`:    "$var", // \$ collapses to a literal $ (not interpolated here)
		`\0101`:    "A",    // octal 0o101 == 65 == 'A'
		`\0`:       "\x00", // bare \0 is NUL
		`\z`:       `\z`,   // unknown escape stays literal (backslash + char)
		`\\`:       `\`,
		`a"b`:      `a"b`,
		`no esc`:   "no esc",
	}
	for in, want := range cases {
		if got := expandEnvEscapes(in); got != want {
			t.Errorf("expandEnvEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvFileOptionalMissing(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    env_file:
      - path: ./does-not-exist.env
        required: false
`)
	if _, err := Load(file); err != nil {
		t.Fatalf("optional missing env_file should not error: %v", err)
	}
}

func TestDependencyCycle(t *testing.T) {
	file := writeCompose(t, `
services:
  a:
    image: a
    depends_on: [b]
  b:
    image: b
    depends_on: [a]
`)
	project, _ := Load(file)
	if _, err := project.Order(); err == nil {
		t.Fatal("expected a dependency cycle error")
	}
}

func TestAdditionalContextsMapForm(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: .
      additional_contexts:
        shared: ../shared
        vendor: ./vendor
        base: docker-image://alpine:3.19
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ac := plans["app"].Build.AdditionalContexts
	if ac["shared"] != "../shared" || ac["vendor"] != "./vendor" {
		t.Fatalf("additional_contexts = %v", ac)
	}
	// Image-reference contexts are dropped (only directories are forwarded).
	if _, ok := ac["base"]; ok {
		t.Fatalf("image-ref context should be skipped: %v", ac)
	}
	if len(ac) != 2 {
		t.Fatalf("expected 2 dir contexts, got %v", ac)
	}
}

func TestAdditionalContextsListForm(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: .
      additional_contexts:
        - shared=../shared
        - base=docker-image://alpine:3.19
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ac := plans["app"].Build.AdditionalContexts
	if len(ac) != 1 || ac["shared"] != "../shared" {
		t.Fatalf("additional_contexts = %v", ac)
	}
}

func TestBuildSecrets(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: .
      secrets:
        - mysecret
        - source: othersecret
        - source: envsecret
secrets:
  mysecret:
    file: ./secret.txt
  othersecret:
    file: ../shared/token
  envsecret:
    environment: SOME_ENV
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	sec := plans["app"].Build.Secrets
	if sec["mysecret"] != "./secret.txt" {
		t.Fatalf("mysecret = %q (all: %v)", sec["mysecret"], sec)
	}
	if sec["othersecret"] != "../shared/token" {
		t.Fatalf("othersecret = %q (all: %v)", sec["othersecret"], sec)
	}
	// Non-file secrets (no file:) are skipped.
	if _, ok := sec["envsecret"]; ok {
		t.Fatalf("non-file secret should be skipped: %v", sec)
	}
	if len(sec) != 2 {
		t.Fatalf("expected 2 file secrets, got %v", sec)
	}
}

func TestBuildSSHListForm(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: .
      ssh:
        - default
        - mykey=/tmp/agent.sock
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ssh := plans["app"].Build.SSH
	if len(ssh) != 2 || ssh[0] != "default" || ssh[1] != "mykey=/tmp/agent.sock" {
		t.Fatalf("build.ssh = %v", ssh)
	}
}

func TestBuildSSHMapForm(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: .
      ssh:
        default:
        mykey: /tmp/agent.sock
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Map entries are sorted for determinism: "default" then "mykey=...".
	ssh := plans["app"].Build.SSH
	if len(ssh) != 2 || ssh[0] != "default" || ssh[1] != "mykey=/tmp/agent.sock" {
		t.Fatalf("build.ssh = %v", ssh)
	}
}

func TestHealthcheckListForm(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost/health"]
      interval: 1m30s
      timeout: 10s
      retries: 3
      start_period: 40s
      start_interval: 5s
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	hc := plans["web"].Spec.Healthcheck
	if hc == nil {
		t.Fatal("expected healthcheck")
	}
	want := []string{"CMD", "curl", "-f", "http://localhost/health"}
	if strings.Join(hc.Test, ",") != strings.Join(want, ",") {
		t.Fatalf("test = %v", hc.Test)
	}
	if hc.Interval != "1m30s" || hc.Timeout != "10s" || hc.StartPeriod != "40s" || hc.Retries != 3 {
		t.Fatalf("timings = %+v", hc)
	}
	if hc.StartInterval != "5s" {
		t.Fatalf("start_interval = %q, want 5s", hc.StartInterval)
	}
}

func TestHealthcheckStringFormAndDisable(t *testing.T) {
	file := writeCompose(t, `
services:
  shell:
    image: app
    healthcheck:
      test: curl -f http://localhost
  disabled:
    image: app
    healthcheck:
      disable: true
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	shell := plans["shell"].Spec.Healthcheck
	if shell == nil || len(shell.Test) != 2 || shell.Test[0] != "CMD-SHELL" || shell.Test[1] != "curl -f http://localhost" {
		t.Fatalf("shell test = %v", shell)
	}
	off := plans["disabled"].Spec.Healthcheck
	if off == nil || len(off.Test) != 1 || off.Test[0] != "NONE" {
		t.Fatalf("disabled test = %v", off)
	}
	if !off.Disabled() {
		t.Fatalf("disabled should be Disabled()")
	}
}

func TestDeployResourceLimits(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: 512M
  bare:
    image: app
    deploy:
      resources:
        limits:
          cpus: 1.5
          memory: 1Gi
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res := plans["web"].Spec.Resources
	if res == nil {
		t.Fatal("expected resources")
	}
	if res.CPULimit != 0.5 {
		t.Fatalf("cpu = %v", res.CPULimit)
	}
	if res.MemoryLimit != 512*1024*1024 {
		t.Fatalf("mem = %d", res.MemoryLimit)
	}
	bare := plans["bare"].Spec.Resources
	if bare == nil || bare.CPULimit != 1.5 || bare.MemoryLimit != 1024*1024*1024 {
		t.Fatalf("bare resources = %+v", bare)
	}
}

// TestEntrypointNotWarned verifies that a service using `entrypoint:` (a
// supported, translated field) produces no unsupported-field warning.
func TestEntrypointNotWarned(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    entrypoint: ["/bin/sh", "-c"]
    command: ["echo", "hi"]
`)
	var warnings []string
	warn := func(service, field string) { warnings = append(warnings, service+"."+field) }
	if _, err := loadFile(file, nil, warn, nil); err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "entrypoint") {
			t.Fatalf("entrypoint should be supported, got warnings: %v", warnings)
		}
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

// TestCommandShellSplitting covers shell-style word splitting of the string
// form of command/entrypoint: quoted args stay grouped, single vs double
// quotes, escaped spaces, plain form unchanged, and the list form passes
// through untouched.
func TestCommandShellSplitting(t *testing.T) {
	file := writeCompose(t, `
services:
  dquote:
    image: app
    command: sh -c "echo hello world"
  squote:
    image: app
    command: echo 'a b' c
  escaped:
    image: app
    command: echo a\ b
  plain:
    image: app
    command: nginx -g daemonoff
  listform:
    image: app
    command: ["nginx", "-g", "daemon off;"]
  hashnotcomment:
    image: app
    command: "echo foo #bar"
  entry:
    image: app
    entrypoint: /bin/sh -c "exec app"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cases := map[string][]string{
		"dquote":   {"sh", "-c", "echo hello world"},
		"squote":   {"echo", "a b", "c"},
		"escaped":  {"echo", "a b"},
		"plain":    {"nginx", "-g", "daemonoff"},
		"listform": {"nginx", "-g", "daemon off;"},
		// go-shellwords (compose-go's splitter) does NOT treat `#` as a comment,
		// unlike google/shlex, so the token is preserved.
		"hashnotcomment": {"echo", "foo", "#bar"},
	}
	for svc, want := range cases {
		got := []string(plans[svc].Spec.Command)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s command = %v, want %v", svc, got, want)
		}
	}
	wantEntry := []string{"/bin/sh", "-c", "exec app"}
	if got := []string(plans["entry"].Spec.Entrypoint); !reflect.DeepEqual(got, wantEntry) {
		t.Errorf("entry entrypoint = %v, want %v", got, wantEntry)
	}
}

// TestPortRanges covers expansion of port-range short forms into one mapping per
// port, the bare-range host==container case, the host-IP form, mismatched range
// lengths as an error, and an unchanged single mapping with a protocol suffix.
func TestPortRanges(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "8000-8010:8000-8010"
      - "3000-3005"
      - "8080:80/tcp"
      - "127.0.0.1:5000-5002:6000-6002"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ports := plans["web"].Spec.Ports

	// 11 (8000-8010) + 6 (3000-3005) + 1 (8080:80) + 3 (5000-5002:6000-6002).
	if len(ports) != 21 {
		t.Fatalf("port count = %d, want 21: %+v", len(ports), ports)
	}
	// First range: pairwise host==container, tcp default.
	if ports[0].Host != 8000 || ports[0].Container != 8000 || ports[0].Protocol != "tcp" {
		t.Errorf("ports[0] = %+v, want 8000->8000/tcp", ports[0])
	}
	if ports[10].Host != 8010 || ports[10].Container != 8010 {
		t.Errorf("ports[10] = %+v, want 8010->8010", ports[10])
	}
	// Bare range: host==container.
	if ports[11].Host != 3000 || ports[11].Container != 3000 {
		t.Errorf("ports[11] = %+v, want 3000->3000", ports[11])
	}
	if ports[16].Host != 3005 || ports[16].Container != 3005 {
		t.Errorf("ports[16] = %+v, want 3005->3005", ports[16])
	}
	// Single mapping with protocol suffix stays unchanged.
	if ports[17].Host != 8080 || ports[17].Container != 80 || ports[17].Protocol != "tcp" {
		t.Errorf("ports[17] = %+v, want 8080->80/tcp", ports[17])
	}
	// Host-IP form with paired ranges: IP preserved on every expanded mapping,
	// ports mapped pairwise.
	if ports[18].Host != 5000 || ports[18].Container != 6000 || ports[18].HostIP != "127.0.0.1" {
		t.Errorf("ports[18] = %+v, want 127.0.0.1 5000->6000", ports[18])
	}
	if ports[20].Host != 5002 || ports[20].Container != 6002 || ports[20].HostIP != "127.0.0.1" {
		t.Errorf("ports[20] = %+v, want 127.0.0.1 5002->6002", ports[20])
	}
}

// TestPortHostIP verifies the leading host-IP component of a published port is
// captured on the translated api.PortMapping (compose host_ip) rather than
// dropped, for both the short "ip:host:container" form and the long object form.
func TestPortHostIP(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "127.0.0.1:8080:80"
      - target: 90
        published: 9090
        host_ip: 10.0.0.5
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ports := plans["web"].Spec.Ports
	if len(ports) != 2 {
		t.Fatalf("port count = %d, want 2: %+v", len(ports), ports)
	}
	if want := (api.PortMapping{HostIP: "127.0.0.1", Host: 8080, Container: 80, Protocol: "tcp"}); ports[0] != want {
		t.Errorf("ports[0] = %+v, want %+v", ports[0], want)
	}
	if ports[1].HostIP != "10.0.0.5" || ports[1].Host != 9090 || ports[1].Container != 90 {
		t.Errorf("ports[1] = %+v, want 10.0.0.5 9090->90", ports[1])
	}
}

// TestVolumeSELinux covers the `:z`/`:Z` relabel option in a bind mount's
// short-form options field (alone and combined with ro), and the long-form
// bind.selinux sub-object, reaching api.Mount.SELinux.
func TestVolumeSELinux(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    volumes:
      - "./a:/a:z"
      - "./b:/b:ro,z"
      - "./c:/c:Z"
      - type: bind
        source: ./d
        target: /d
        bind:
          selinux: z
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	mounts := plans["web"].Spec.Mounts
	byTarget := map[string]api.Mount{}
	for _, m := range mounts {
		byTarget[m.Target] = m
	}
	if m := byTarget["/a"]; m.SELinux != "z" || m.ReadOnly {
		t.Errorf("/a = %+v, want SELinux z, rw", m)
	}
	if m := byTarget["/b"]; m.SELinux != "z" || !m.ReadOnly {
		t.Errorf("/b = %+v, want SELinux z, ro", m)
	}
	if m := byTarget["/c"]; m.SELinux != "Z" {
		t.Errorf("/c = %+v, want SELinux Z", m)
	}
	if m := byTarget["/d"]; m.SELinux != "z" {
		t.Errorf("/d (long form) = %+v, want SELinux z", m)
	}
}

// TestVolumeCreateHostPath covers the long-form bind.create_host_path option
// reaching api.Mount.NoCreateHostPath: absent and true both keep Docker's default
// (create => NoCreateHostPath false); only an explicit false opts out.
func TestVolumeCreateHostPath(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    volumes:
      - "./a:/a"
      - type: bind
        source: ./b
        target: /b
        bind:
          create_host_path: false
      - type: bind
        source: ./c
        target: /c
        bind:
          create_host_path: true
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	byTarget := map[string]api.Mount{}
	for _, m := range plans["web"].Spec.Mounts {
		byTarget[m.Target] = m
	}
	if m := byTarget["/a"]; m.NoCreateHostPath {
		t.Errorf("/a (short form) = %+v, want NoCreateHostPath false (default create)", m)
	}
	if m := byTarget["/b"]; !m.NoCreateHostPath {
		t.Errorf("/b (create_host_path: false) = %+v, want NoCreateHostPath true", m)
	}
	if m := byTarget["/c"]; m.NoCreateHostPath {
		t.Errorf("/c (create_host_path: true) = %+v, want NoCreateHostPath false", m)
	}
}

// TestPortRangeMismatch verifies range/single-port combinations: a host-port
// range mapped to a single container port is approximated to its first host port
// (Docker would pick an ephemeral port from the range, which cornus cannot
// express); two ranges of unequal length remain an error.
func TestPortRangeMismatch(t *testing.T) {
	// host range -> single container port: approximate to the first host port.
	file := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "8000-8010:80"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ports := plans["web"].Spec.Ports
	if len(ports) != 1 || ports[0].Host != 8000 || ports[0].Container != 80 {
		t.Fatalf("ports = %+v, want single 8000->80 (first-port approximation)", ports)
	}

	// Two ranges of unequal length is still a genuine error.
	bad := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "8000-8010:9000-9005"
`)
	if _, err := Load(bad); err == nil {
		t.Fatal("expected an error for mismatched port range lengths")
	}

	// A single host port with a container range is invalid.
	bad2 := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "80:8000-8010"
`)
	if _, err := Load(bad2); err == nil {
		t.Fatal("expected an error for a single host port mapped to a container range")
	}
}

// TestPortIPv6HostIP verifies the bracketed IPv6 short form has its host IP
// captured (brackets stripped) instead of shattering on the address's colons.
func TestPortIPv6HostIP(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "[::1]:8080:80"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ports := plans["web"].Spec.Ports
	if len(ports) != 1 || ports[0].HostIP != "::1" || ports[0].Host != 8080 || ports[0].Container != 80 {
		t.Fatalf("ports = %+v, want ::1 8080->80", ports)
	}
}

// TestPortOutOfRange rejects a user-specified port above the valid range.
func TestPortOutOfRange(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "100000"
`)
	if _, err := Load(file); err == nil {
		t.Fatal("expected an error for a port above 65535")
	}
}

// TestVolumeSELinuxConflict rejects a short-form mount carrying both the shared
// (z) and private (Z) relabel tokens instead of silently last-winning.
func TestVolumeSELinuxConflict(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    volumes:
      - "./d:/d:z,Z"
`)
	if _, err := Load(file); err == nil {
		t.Fatal("expected an error for conflicting SELinux relabel options z and Z")
	}
}

// TestPortSingleUnchanged confirms a plain single mapping still parses as before.
func TestPortSingleUnchanged(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    ports:
      - "8080:80/tcp"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, _ := project.Plan("p")
	ports := plans["web"].Spec.Ports
	if len(ports) != 1 || ports[0].Host != 8080 || ports[0].Container != 80 || ports[0].Protocol != "tcp" {
		t.Fatalf("ports = %+v, want single 8080->80/tcp", ports)
	}
}

// TestExposeRanges covers `expose:` accepting a range string (expanded
// inclusively), a mix of ints and a range, and single values unchanged.
func TestExposeRanges(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    expose:
      - "3000-3005"
      - 80
      - "443"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := []int(project.Services()["web"].Expose)
	want := []int{3000, 3001, 3002, 3003, 3004, 3005, 80, 443}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expose = %v, want %v", got, want)
	}
}

func TestHealthcheckAndResourcesNotWarned(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: app
    healthcheck:
      test: ["CMD", "true"]
    deploy:
      resources:
        limits:
          cpus: "0.25"
          memory: 256M
`)
	var warnings []string
	warn := func(service, field string) { warnings = append(warnings, service+"."+field) }
	if _, err := loadFile(file, nil, warn, nil); err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestDependsOnListForm(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: web
    depends_on:
      - db
      - cache
  db:
    image: db
  cache:
    image: cache
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := project.Services()["web"].DependsOn
	if got := deps.Names(); !reflect.DeepEqual(got, []string{"db", "cache"}) {
		t.Fatalf("Names() = %v, want [db cache]", got)
	}
	for _, dep := range deps {
		if dep.Condition != DependsOnStarted {
			t.Errorf("%s: condition = %q, want %q", dep.Service, dep.Condition, DependsOnStarted)
		}
		if !dep.Required {
			t.Errorf("%s: required = false, want true (default)", dep.Service)
		}
	}
}

func TestDependsOnMapForm(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_healthy
        restart: true
      migrate:
        condition: service_completed_successfully
        required: false
      cache: null
  db:
    image: db
  migrate:
    image: migrate
  cache:
    image: cache
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := project.Services()["web"].DependsOn
	// Map form is sorted by service name for determinism.
	if got := deps.Names(); !reflect.DeepEqual(got, []string{"cache", "db", "migrate"}) {
		t.Fatalf("Names() = %v, want [cache db migrate] (sorted)", got)
	}
	byName := map[string]Dependency{}
	for _, d := range deps {
		byName[d.Service] = d
	}
	if d := byName["db"]; d.Condition != DependsOnHealthy || !d.Required || !d.Restart {
		t.Errorf("db = %+v, want {healthy, required, restart}", d)
	}
	if d := byName["migrate"]; d.Condition != DependsOnCompleted || d.Required {
		t.Errorf("migrate = %+v, want {completed, required=false}", d)
	}
	// A null value takes the defaults.
	if d := byName["cache"]; d.Condition != DependsOnStarted || !d.Required {
		t.Errorf("cache = %+v, want {started, required=true} defaults", d)
	}
}

func TestDependsOnOrder(t *testing.T) {
	// Order() must still sort correctly on the new struct type (via Names()).
	file := writeCompose(t, `
services:
  web:
    image: web
    depends_on:
      db:
        condition: service_healthy
  db:
    image: db
    depends_on: [cache]
  cache:
    image: cache
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	order, err := project.Order()
	if err != nil {
		t.Fatalf("Order: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"cache", "db", "web"}) {
		t.Fatalf("order = %v, want [cache db web]", order)
	}
}
