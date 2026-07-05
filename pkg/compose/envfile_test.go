package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadWithEnvFile checks --env-file interpolation: an explicit env file
// supplies ${VAR} values in place of the default .env, a missing explicit file
// is an error, and the process environment still overrides the file.
func TestLoadWithEnvFile(t *testing.T) {
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services:\n  web:\n    image: ${IMG}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A default .env in the same dir sets IMG to a value the explicit file must win over.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("IMG=from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envf := filepath.Join(dir, "custom.env")
	if err := os.WriteFile(envf, []byte("IMG=nginx:1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := LoadWithOptions(LoadOptions{EnvFiles: []string{envf}}, composeFile)
	if err != nil {
		t.Fatalf("LoadWithOptions: %v", err)
	}
	if got := p.Services()["web"].Image; got != "nginx:1.27" {
		t.Fatalf("image = %q, want nginx:1.27 (explicit env file should replace .env)", got)
	}

	if _, err := LoadWithOptions(LoadOptions{EnvFiles: []string{filepath.Join(dir, "nope.env")}}, composeFile); err == nil {
		t.Fatal("a missing explicit --env-file should be an error")
	}

	t.Setenv("IMG", "alpine")
	p, err = LoadWithOptions(LoadOptions{EnvFiles: []string{envf}}, composeFile)
	if err != nil {
		t.Fatalf("LoadWithOptions (env override): %v", err)
	}
	if got := p.Services()["web"].Image; got != "alpine" {
		t.Fatalf("image = %q, want alpine (process env should override the env file)", got)
	}
}
