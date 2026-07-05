package webui

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// get drives h with a GET for path and returns the status code and body.
func get(t *testing.T, fsys fstest.MapFS, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handlerFor(fsys).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return rec.Code, string(body)
}

func TestServesAssetsAndFallsBackToIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}

	if code, body := get(t, fsys, "/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Errorf("asset: got %d %q", code, body)
	}
	// Exact root and unknown client-side routes both serve index.html.
	for _, path := range []string{"/", "/workloads", "/projects/foo/graph"} {
		if code, body := get(t, fsys, path); code != 200 || body != "<html>app</html>" {
			t.Errorf("%s: got %d %q, want index.html", path, code, body)
		}
	}
}

func TestNotBuiltNotice(t *testing.T) {
	// dist without an index.html (the committed .gitkeep-only state).
	fsys := fstest.MapFS{".gitkeep": {Data: nil}}
	code, body := get(t, fsys, "/")
	if code != 503 || !strings.Contains(body, "make web") {
		t.Errorf("got %d %q, want 503 with build hint", code, body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("x")}}
	rec := httptest.NewRecorder()
	handlerFor(fsys).ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	if rec.Code != 405 {
		t.Errorf("POST /: got %d, want 405", rec.Code)
	}
}
