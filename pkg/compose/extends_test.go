package compose

import (
	"strings"
	"testing"
)

// TestExtendsLocal: web extends a base service in the same file. The base's
// image and environment are inherited; web's own keys win on conflict.
func TestExtendsLocal(t *testing.T) {
	file := writeCompose(t, `
services:
  base:
    image: base:1
    environment:
      A: base-a
      B: base-b
  web:
    extends: base
    image: web:1
    environment:
      B: web-b
      C: web-c
`)
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	web := p.Services()["web"]
	if web.Image != "web:1" {
		t.Errorf("image: got %q, want web:1 (web's own key wins)", web.Image)
	}
	want := map[string]string{"A": "base-a", "B": "web-b", "C": "web-c"}
	for k, v := range want {
		if web.Environment[k] != v {
			t.Errorf("environment[%q]: got %q, want %q", k, web.Environment[k], v)
		}
	}
	if web.Extends != nil {
		t.Errorf("Extends should be cleared after resolution, got %+v", web.Extends)
	}
}

// TestExtendsCrossFile: web in the main file extends a service in another file
// via {file, service}. Fields from the referenced service are inherited.
func TestExtendsCrossFile(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
services:
  web:
    extends:
      file: common.yml
      service: common
    environment:
      OWN: web
`,
		"common.yml": `
services:
  common:
    image: common:1
    environment:
      SHARED: "yes"
`,
	})
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	web := p.Services()["web"]
	if web.Image != "common:1" {
		t.Errorf("image: got %q, want common:1 (inherited from common.yml)", web.Image)
	}
	if web.Environment["SHARED"] != "yes" {
		t.Errorf("environment[SHARED]: got %q, want yes", web.Environment["SHARED"])
	}
	if web.Environment["OWN"] != "web" {
		t.Errorf("environment[OWN]: got %q, want web", web.Environment["OWN"])
	}
	// The referenced service should not leak into the main project's services.
	if _, ok := p.Services()["common"]; ok {
		t.Errorf("referenced service %q must not leak into main project", "common")
	}
}

// TestExtendsChain: a extends b extends c, all local, resolves fully so a
// inherits c's image and the whole environment chain.
func TestExtendsChain(t *testing.T) {
	file := writeCompose(t, `
services:
  a:
    extends: b
    environment:
      AA: a
  b:
    extends: c
    environment:
      BB: b
  c:
    image: c:1
    environment:
      CC: c
`)
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	a := p.Services()["a"]
	if a.Image != "c:1" {
		t.Errorf("image: got %q, want c:1 (through b -> c)", a.Image)
	}
	want := map[string]string{"AA": "a", "BB": "b", "CC": "c"}
	for k, v := range want {
		if a.Environment[k] != v {
			t.Errorf("environment[%q]: got %q, want %q", k, a.Environment[k], v)
		}
	}
}

// TestExtendsCycle: a extends b and b extends a is a circular reference and must
// error rather than loop.
func TestExtendsCycle(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    extends: base
    image: web:1
  base:
    extends: web
    image: base:1
`)
	_, err := Load(file)
	if err == nil {
		t.Fatal("expected a circular reference error, got nil")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error should mention a circular reference, got: %v", err)
	}
}

// TestExtendsCycleCrossFile: a cross-file cycle (a.yml -> b.yml -> a.yml) is
// also detected.
func TestExtendsCycleCrossFile(t *testing.T) {
	file := writeFiles(t, map[string]string{
		"compose.yaml": `
services:
  web:
    extends:
      file: other.yml
      service: common
`,
		"other.yml": `
services:
  common:
    extends:
      file: compose.yaml
      service: web
`,
	})
	_, err := Load(file)
	if err == nil {
		t.Fatal("expected a circular reference error, got nil")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error should mention a circular reference, got: %v", err)
	}
}

// TestExtendsMissingService: extending a non-existent service is an error.
func TestExtendsMissingService(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    extends: nope
    image: web:1
`)
	_, err := Load(file)
	if err == nil {
		t.Fatal("expected an error for a missing extends target, got nil")
	}
	if !strings.Contains(err.Error(), "no such service") {
		t.Errorf("error should mention the missing service, got: %v", err)
	}
}

// TestExtendsMissingFile: extending a service in a non-existent file is an error.
func TestExtendsMissingFile(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    extends:
      file: nonexistent.yml
      service: base
    image: web:1
`)
	_, err := Load(file)
	if err == nil {
		t.Fatal("expected an error for a missing extends file, got nil")
	}
}

// TestExtendsDropsDependsOn: per the compose-spec restriction, the base's
// depends_on is NOT inherited; only the extending service's own depends_on
// survives.
func TestExtendsDropsDependsOn(t *testing.T) {
	file := writeCompose(t, `
services:
  base:
    image: base:1
    depends_on:
      - db
  web:
    extends: base
    image: web:1
    depends_on:
      - cache
  db:
    image: db:1
  cache:
    image: cache:1
`)
	p, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	web := p.Services()["web"]
	names := web.DependsOn.Names()
	if len(names) != 1 || names[0] != "cache" {
		t.Errorf("web depends_on: got %v, want [cache] (base's db must not be inherited)", names)
	}
}
