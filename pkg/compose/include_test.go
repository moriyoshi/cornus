package compose

import (
	"strings"
	"testing"
)

// TestIncludeBasic: main includes common.yml; the loaded project has both the
// included service and the main file's own service.
func TestIncludeBasic(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
include:
  - common.yml
services:
  web:
    image: web:v1
`,
		"common.yml": `
services:
  db:
    image: postgres:16
`,
	})
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Services()["web"]; !ok {
		t.Errorf("main service web missing")
	}
	db, ok := p.Services()["db"]
	if !ok {
		t.Fatalf("included service db missing")
	}
	if db.Image != "postgres:16" {
		t.Errorf("db image = %q, want postgres:16", db.Image)
	}
}

// TestIncludePrecedence: both files define web; the main file's scalar wins,
// while an included-only field is inherited via the deep merge.
func TestIncludePrecedence(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
include:
  - common.yml
services:
  web:
    image: web:main
    environment:
      FROM_MAIN: "1"
`,
		"common.yml": `
services:
  web:
    image: web:included
    restart: always
    environment:
      FROM_INCLUDED: "1"
      FROM_MAIN: "0"
`,
	})
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	web := p.Services()["web"]
	if web.Image != "web:main" {
		t.Errorf("image = %q, want web:main (main overrides included)", web.Image)
	}
	if web.Restart != "always" {
		t.Errorf("restart = %q, want always (inherited from included)", web.Restart)
	}
	if web.Environment["FROM_INCLUDED"] != "1" {
		t.Errorf("FROM_INCLUDED = %q, want 1 (inherited)", web.Environment["FROM_INCLUDED"])
	}
	if web.Environment["FROM_MAIN"] != "1" {
		t.Errorf("FROM_MAIN = %q, want 1 (main overrides)", web.Environment["FROM_MAIN"])
	}
}

// TestIncludeLongFormPathListEnvFile: a long-form entry with a path list and an
// env_file used to interpolate the included model.
func TestIncludeLongFormPathListEnvFile(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
include:
  - path:
      - a.yml
      - b.yml
    env_file: inc.env
services:
  web:
    image: web:v1
`,
		"a.yml": `
services:
  cache:
    image: redis:${REDIS_TAG}
`,
		"b.yml": `
services:
  worker:
    image: worker:v1
`,
		"inc.env": "REDIS_TAG=7\n",
	})
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "cache", "worker"} {
		if _, ok := p.Services()[name]; !ok {
			t.Errorf("service %q missing", name)
		}
	}
	if got := p.Services()["cache"].Image; got != "redis:7" {
		t.Errorf("cache image = %q, want redis:7 (env_file interpolation)", got)
	}
}

// TestIncludeNested: main -> a -> b, all folded in.
func TestIncludeNested(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
include:
  - a.yml
services:
  main:
    image: main:v1
`,
		"a.yml": `
include:
  - b.yml
services:
  a:
    image: a:v1
`,
		"b.yml": `
services:
  b:
    image: b:v1
`,
	})
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main", "a", "b"} {
		if _, ok := p.Services()[name]; !ok {
			t.Errorf("service %q missing after nested include", name)
		}
	}
}

// TestIncludeCycle: a includes b and b includes a -> circular reference error.
func TestIncludeCycle(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
include:
  - a.yml
services:
  main:
    image: main:v1
`,
		"a.yml": `
include:
  - b.yml
services:
  a:
    image: a:v1
`,
		"b.yml": `
include:
  - a.yml
services:
  b:
    image: b:v1
`,
	})
	_, err := Load(file)
	if err == nil {
		t.Fatal("expected circular reference error, got nil")
	}
	if !strings.Contains(err.Error(), "circular reference") {
		t.Errorf("error = %q, want it to mention circular reference", err)
	}
}

// TestIncludeMissingFile: a missing included file yields a clear error.
func TestIncludeMissingFile(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
include:
  - nope.yml
services:
  web:
    image: web:v1
`,
	})
	_, err := Load(file)
	if err == nil {
		t.Fatal("expected error for missing included file, got nil")
	}
	if !strings.Contains(err.Error(), "include") {
		t.Errorf("error = %q, want it to mention include", err)
	}
}
