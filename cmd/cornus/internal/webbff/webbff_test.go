package webbff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/client"
)

// fakeCornusServer serves the /.cornus/v1/* subset the BFF consumes: the
// deployment list, per-deployment status, and tunnel status.
func fakeCornusServer(t *testing.T, list []api.DeployStatus, tunnels map[string]api.TunnelStatus) *httptest.Server {
	t.Helper()
	byName := map[string]api.DeployStatus{}
	for _, st := range list {
		byName[st.Name] = st
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.cornus/v1/deploy", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("GET /.cornus/v1/deploy/{name}", func(w http.ResponseWriter, r *http.Request) {
		st, ok := byName[r.PathValue("name")]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("GET /.cornus/v1/deploy/{name}/tunnel", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tunnels[r.PathValue("name")])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeAgentView is a webbff.AgentView backed by a fixed status — the in-process
// counterpart of the CLI's socket-backed view. A nil status models "no agent".
type fakeAgentView struct{ status *AgentStatus }

func (fakeAgentView) Socket() string         { return "/run/cornus/agent.sock" }
func (v fakeAgentView) Status() *AgentStatus { return v.status }

// testServer builds a Server over a temp compose project (web depends_on db, web
// has a bind mount, db a named volume) and the given fakes.
func testServer(t *testing.T, upstream *httptest.Server, av AgentView) *Server {
	t.Helper()
	dir := t.TempDir()
	composePath := filepath.Join(dir, "compose.yaml")
	composeYAML := `services:
  web:
    image: example/web:1
    depends_on:
      db:
        condition: service_healthy
    volumes:
      - ./html:/usr/share/nginx/html:ro
  db:
    image: example/db:1
    volumes:
      - dbdata:/var/lib/db
volumes:
  dbdata: {}
`
	if err := os.WriteFile(composePath, []byte(composeYAML), 0o644); err != nil {
		t.Fatalf("writing compose file: %v", err)
	}

	s, err := New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL),
		upstream.URL,
		&clientconn.Resolver{},
		av,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// doJSON drives one BFF request and decodes the JSON response into out.
func doJSON(t *testing.T, s *Server, method, path string, out any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: decoding response %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec
}

func TestWebWorkloadsJoin(t *testing.T) {
	// db is deployed and running; web is not created yet.
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-db", Image: "example/db:1@sha256:abc", Backend: "dockerhost",
			Origin:    &api.Origin{Project: "proj", Host: "laptop", User: "alice", Subject: "user:alice"},
			Instances: []api.InstanceStatus{{ID: "c1", State: "running", Running: true}}},
		{Name: "other", Image: "example/other:1",
			Origin:    &api.Origin{Project: "otherproj", Host: "box", User: "bob"},
			Instances: []api.InstanceStatus{{ID: "c2", State: "exited", Running: false}}},
	}, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})

	var rows []webWorkload
	doJSON(t, s, "GET", "/.cornus/web/workloads", &rows)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	// Dependency order: db before web; non-project deployments last.
	if rows[0].Service != "db" || rows[0].Name != "proj-db" || !rows[0].Running || rows[0].Summary != "1/1 running" {
		t.Errorf("db row: %+v", rows[0])
	}
	if rows[0].Image != "example/db:1@sha256:abc" {
		t.Errorf("db image should prefer the status image: %+v", rows[0])
	}
	// Lineage flows onto the deployed row; an uncreated row carries none.
	if rows[0].Origin == nil || rows[0].Origin.User != "alice" || rows[0].Origin.Subject != "user:alice" {
		t.Errorf("db row origin: %+v", rows[0].Origin)
	}
	if rows[1].Origin != nil {
		t.Errorf("uncreated web row should have no origin: %+v", rows[1].Origin)
	}
	if rows[1].Service != "web" || rows[1].Created || rows[1].Summary != "not created" {
		t.Errorf("web row: %+v", rows[1])
	}
	if rows[2].Name != "other" || rows[2].Service != "" || rows[2].Running {
		t.Errorf("other row: %+v", rows[2])
	}
	// A workload outside the loaded project is attributed to its recorded origin
	// project, not left project-less.
	if rows[2].Project != "otherproj" {
		t.Errorf("other row should be attributed to origin project: %+v", rows[2])
	}
}

// TestWebWorkloadDetailOrigin confirms the detail endpoint passes the
// deployment's lineage through on the embedded status.
func TestWebWorkloadDetailOrigin(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-db", Image: "example/db:1", Backend: "dockerhost",
			Origin: &api.Origin{
				Project: "proj", Host: "laptop", User: "alice", Subject: "user:alice",
				Git: &api.GitOrigin{Branch: "main", Commit: "deadbeef", Dirty: true},
			},
			Instances: []api.InstanceStatus{{ID: "c1", State: "running", Running: true}}},
	}, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})

	var detail webWorkloadDetail
	doJSON(t, s, "GET", "/.cornus/web/workloads/proj-db", &detail)
	if detail.Status == nil || detail.Status.Origin == nil {
		t.Fatalf("detail status/origin missing: %+v", detail)
	}
	o := detail.Status.Origin
	if o.User != "alice" || o.Subject != "user:alice" || o.Git == nil || o.Git.Branch != "main" || !o.Git.Dirty {
		t.Errorf("detail origin: %+v (git %+v)", o, o.Git)
	}
}

func TestWebGraph(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-db", Instances: []api.InstanceStatus{{Running: true}}},
	}, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})

	var g webGraph
	doJSON(t, s, "GET", "/.cornus/web/projects/proj/graph", &g)
	if len(g.Nodes) != 2 || len(g.Edges) != 1 {
		t.Fatalf("got %d nodes / %d edges, want 2/1: %+v", len(g.Nodes), len(g.Edges), g)
	}
	e := g.Edges[0]
	if e.From != "web" || e.To != "db" || e.Condition != "service_healthy" || !e.Required {
		t.Errorf("edge: %+v", e)
	}
	if rec := doJSON(t, s, "GET", "/.cornus/web/projects/nope/graph", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: got %d, want 404", rec.Code)
	}
}

func TestWebMountStatusDerivation(t *testing.T) {
	// Both services running; the agent holds a session only for web.
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: true}}},
		{Name: "proj-db", Instances: []api.InstanceStatus{{Running: true}}},
	}, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{Projects: map[string][]string{"proj": {"web"}}}})

	var mounts []webMount
	doJSON(t, s, "GET", "/.cornus/web/mounts", &mounts)
	byTarget := map[string]webMount{}
	for _, m := range mounts {
		byTarget[m.Target] = m
	}
	bind := byTarget["/usr/share/nginx/html"]
	if bind.Kind != "bind" || bind.Status != "live" || !bind.ReadOnly {
		t.Errorf("web bind mount: %+v", bind)
	}
	if !strings.HasSuffix(bind.Source, "/html") || !filepath.IsAbs(bind.Source) {
		t.Errorf("bind source should be resolved absolute: %+v", bind)
	}
	vol := byTarget["/var/lib/db"]
	// A volume is backend-realized: running (not "live") even when sessions exist.
	if vol.Kind != "volume" || vol.Status != "running" || vol.Source != "proj_dbdata" {
		t.Errorf("db volume: %+v", vol)
	}
}

func TestWebFilesRoundTripAndAllowList(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})

	var files []webFile
	doJSON(t, s, "GET", "/.cornus/web/files", &files)
	var composeFile string
	for _, f := range files {
		if f.Kind == "compose" {
			composeFile = f.Path
		}
	}
	if composeFile == "" {
		t.Fatalf("no compose file in editable set: %+v", files)
	}

	mux := http.NewServeMux()
	s.routes(mux)

	// PUT then GET round-trips the content.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/.cornus/web/files/content?path="+composeFile, strings.NewReader("services: {}\n")))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/.cornus/web/files/content?path="+composeFile, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "services: {}\n" {
		t.Fatalf("GET after PUT: got %d %q", rec.Code, rec.Body.String())
	}

	// Anything outside the allow-list is rejected, including traversal spellings.
	for _, path := range []string{"/etc/passwd", filepath.Dir(composeFile) + "/../../../etc/passwd", filepath.Dir(composeFile) + "/other.yaml"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/.cornus/web/files/content?path="+path, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: got %d, want 403", path, rec.Code)
		}
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/.cornus/web/files/content?path="+path, strings.NewReader("x")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("PUT %s: got %d, want 403", path, rec.Code)
		}
	}
}

func TestWebTunnelsAndForwards(t *testing.T) {
	upstream := fakeCornusServer(t,
		[]api.DeployStatus{{Name: "proj-web"}},
		map[string]api.TunnelStatus{"proj-web": {Active: true, URL: "https://x.ngrok.app", Port: 80}})
	av := fakeAgentView{status: &AgentStatus{
		Banners:  []string{"SOCKS5 proxy at 127.0.0.1:1080"},
		Forwards: map[string][]string{"web": {"127.0.0.1:8080 -> :80"}},
	}}
	s := testServer(t, upstream, av)

	var resp webTunnelsResponse
	doJSON(t, s, "GET", "/.cornus/web/tunnels", &resp)
	if len(resp.Tunnels) != 1 || !resp.Tunnels[0].Active || resp.Tunnels[0].URL != "https://x.ngrok.app" {
		t.Errorf("tunnels: %+v", resp.Tunnels)
	}
	if len(resp.Forwards["web"]) != 1 || len(resp.Banners) != 1 {
		t.Errorf("forwards/banners: %+v", resp)
	}
}

// TestGuardHostRejectsRebind covers the DNS-rebinding guard: the loopback
// spellings and the published name are accepted; a foreign Host is refused.
func TestGuardHostRejectsRebind(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	s.cfg.PublishedName = "cornus.internal"
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{
		"127.0.0.1:41234":      http.StatusOK,
		"localhost:41234":      http.StatusOK,
		"[::1]:41234":          http.StatusOK,
		"cornus.internal":      http.StatusOK,
		"cornus.internal:80":   http.StatusOK,
		"CORNUS.INTERNAL":      http.StatusOK,
		"evil.example.com":     http.StatusMisdirectedRequest,
		"attacker.internal:80": http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest("GET", "/.cornus/web/config", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, rec.Code, want)
		}
	}
}
