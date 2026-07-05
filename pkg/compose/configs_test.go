package compose

import (
	"path/filepath"
	"testing"

	"cornus/pkg/api"
)

// findMount returns the mount with the given target, or nil.
func findMount(mounts []api.Mount, target string) *api.Mount {
	for i := range mounts {
		if mounts[i].Target == target {
			return &mounts[i]
		}
	}
	return nil
}

// planWeb loads the given compose.yaml content and returns the "web" service
// plan with its bind-mount sources resolved against the project directory (so a
// file: source becomes the absolute path the backend sees, as at runtime).
func planWeb(t *testing.T, files map[string]string) ServicePlan {
	t.Helper()
	file := writeFiles(t, files)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan, ok := plans["web"]
	if !ok {
		t.Fatalf("no web service in plan")
	}
	plan.ResolveMounts(filepath.Dir(file))
	return plan
}

func TestServiceConfigShortForm(t *testing.T) {
	plan := planWeb(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    configs:
      - app_cfg
configs:
  app_cfg:
    file: ./app.cfg
`,
		"app.cfg": "hello",
	})
	m := findMount(plan.Spec.Mounts, "/app_cfg")
	if m == nil {
		t.Fatalf("no config mount at /app_cfg; mounts=%v", plan.Spec.Mounts)
	}
	if !m.ReadOnly {
		t.Errorf("config mount not read-only: %+v", m)
	}
	if !filepath.IsAbs(m.Source) || filepath.Base(m.Source) != "app.cfg" {
		t.Errorf("config mount source = %q, want absolute .../app.cfg", m.Source)
	}
}

func TestServiceConfigLongFormTargetAndMode(t *testing.T) {
	plan := planWeb(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    configs:
      - source: app_cfg
        target: /etc/app/app.conf
        mode: 0440
configs:
  app_cfg:
    file: ./app.cfg
`,
		"app.cfg": "hello",
	})
	m := findMount(plan.Spec.Mounts, "/etc/app/app.conf")
	if m == nil {
		t.Fatalf("no config mount at /etc/app/app.conf; mounts=%v", plan.Spec.Mounts)
	}
	if !m.ReadOnly {
		t.Errorf("config mount not read-only: %+v", m)
	}
	if filepath.Base(m.Source) != "app.cfg" {
		t.Errorf("config mount source = %q, want .../app.cfg", m.Source)
	}
}

func TestServiceSecretShortForm(t *testing.T) {
	plan := planWeb(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    secrets:
      - db_pw
secrets:
  db_pw:
    file: ./db_pw.txt
`,
		"db_pw.txt": "s3cr3t",
	})
	m := findMount(plan.Spec.Mounts, "/run/secrets/db_pw")
	if m == nil {
		t.Fatalf("no secret mount at /run/secrets/db_pw; mounts=%v", plan.Spec.Mounts)
	}
	if !m.ReadOnly {
		t.Errorf("secret mount not read-only: %+v", m)
	}
	if filepath.Base(m.Source) != "db_pw.txt" {
		t.Errorf("secret mount source = %q, want .../db_pw.txt", m.Source)
	}
}

func TestServiceSecretLongFormTarget(t *testing.T) {
	plan := planWeb(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    secrets:
      - source: db_pw
        target: /etc/db/password
secrets:
  db_pw:
    file: ./db_pw.txt
`,
		"db_pw.txt": "s3cr3t",
	})
	if m := findMount(plan.Spec.Mounts, "/etc/db/password"); m == nil {
		t.Fatalf("no secret mount at /etc/db/password; mounts=%v", plan.Spec.Mounts)
	}
}

// A relative long-form secret target is placed under /run/secrets/.
func TestServiceSecretRelativeTarget(t *testing.T) {
	plan := planWeb(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    secrets:
      - source: db_pw
        target: nested/pw
secrets:
  db_pw:
    file: ./db_pw.txt
`,
		"db_pw.txt": "s3cr3t",
	})
	if m := findMount(plan.Spec.Mounts, "/run/secrets/nested/pw"); m == nil {
		t.Fatalf("no secret mount at /run/secrets/nested/pw; mounts=%v", plan.Spec.Mounts)
	}
}

// A content:-based config cannot be realised as a bind mount: it is skipped
// (no mount, no error).
func TestServiceConfigContentSkipped(t *testing.T) {
	plan := planWeb(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    configs:
      - inline_cfg
configs:
  inline_cfg:
    content: "hello from inline"
`,
	})
	if len(plan.Spec.Mounts) != 0 {
		t.Fatalf("content-based config should produce no mount; mounts=%v", plan.Spec.Mounts)
	}
}

// A grant referencing a config/secret not defined at the top level is an error.
func TestServiceConfigMissingSource(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    configs:
      - nope
`,
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("p"); err == nil {
		t.Fatalf("expected error for missing config source, got nil")
	}
}

func TestServiceSecretMissingSource(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
    secrets:
      - nope
`,
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("p"); err == nil {
		t.Fatalf("expected error for missing secret source, got nil")
	}
}

// Regression: a top-level file secret consumed by build.secrets still resolves
// for builds and does NOT leak into the runtime mount list.
func TestBuildSecretUnaffectedByRuntimeSecrets(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
services:
  web:
    build:
      context: .
      secrets:
        - mysecret
secrets:
  mysecret:
    file: ./secret.txt
`,
		"secret.txt": "token",
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	web := plans["web"]
	if web.Build == nil || web.Build.Secrets["mysecret"] != "./secret.txt" {
		t.Fatalf("build secret not resolved: %+v", web.Build)
	}
	// No runtime `secrets:` grant on the service, so no bind mount is produced.
	if len(web.Spec.Mounts) != 0 {
		t.Fatalf("build secret leaked into runtime mounts: %v", web.Spec.Mounts)
	}
}

// The top-level SecretDef now carries the runtime fields; confirm they decode
// without disturbing the File field the build path reads.
func TestTopLevelSecretRuntimeFields(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
services:
  web:
    image: nginx
secrets:
  ext_sec:
    external: true
    name: prod_secret
  env_sec:
    environment: SECRET_ENV
`,
	})
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := project.Secrets()["ext_sec"]; !got.External || got.Name != "prod_secret" {
		t.Fatalf("ext_sec decoded wrong: %+v", got)
	}
	if got := project.Secrets()["env_sec"]; got.Environment != "SECRET_ENV" {
		t.Fatalf("env_sec decoded wrong: %+v", got)
	}
}
