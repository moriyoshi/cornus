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

// TestWebWorkloadsCarryNoBackend pins the ABSENCE of a per-workload backend name
// on the wire. api.DeployStatus has one — the upstream fixture below sets it —
// but it is a property of the server, not of a workload: pkg/server builds one
// backend for its lifetime and every backend stamps its own Name() on every
// status it returns, so relaying it per row only ever repeated a single string.
// The server-scoped fact stays on /config as server.backend.
//
// Read from the raw JSON: decoding into webWorkload cannot observe a key the
// struct no longer names, so it would pass no matter what the handler emitted.
func TestWebWorkloadsCarryNoBackend(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-db", Image: "example/db:1", Backend: "dockerhost",
			Instances: []api.InstanceStatus{{ID: "c1", State: "running", Running: true}}},
	}, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})

	var rows []map[string]any
	doJSON(t, s, "GET", "/.cornus/web/workloads", &rows)
	// A row the upstream never created carries no status at all, so it would
	// satisfy the loop below vacuously. The deployed row is the one under test.
	created := 0
	for _, row := range rows {
		if row["created"] == true {
			created++
		}
		if v, ok := row["backend"]; ok {
			t.Errorf("row %v relays a backend name (%v)", row["name"], v)
		}
	}
	if created == 0 {
		t.Fatalf("no created row to check: %+v", rows)
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

// TestWebHasNoApplyRoute pins the deliberate hole in the HTTP surface: the UI is a
// companion to the CLI, so re-deploying a project is not something a browser can
// ask the BFF for. "proj" is the LOADED project — the name the removed handler
// would have accepted — so a 404 here means the route is gone, not that the
// project name was rejected. Server.Apply itself stays; TestMCPToolsMatchHTTP
// pins the `project_apply` tool that reaches it.
func TestWebHasNoApplyRoute(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	if s.project == nil || s.projectName != "proj" {
		t.Fatalf("test premise: want the loaded project %q, got project=%v name=%q", "proj", s.project != nil, s.projectName)
	}

	if rec := doJSON(t, s, "POST", "/.cornus/web/projects/proj/apply", nil); rec.Code != http.StatusNotFound {
		t.Errorf("POST apply on the loaded project: got %d, want 404 (no such route); body %q", rec.Code, rec.Body.String())
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

// /config relays the CLIENT's ingress settings — the counterpart to the server's
// advertised front door, which the server itself knows nothing about. It rides on
// /config rather than /tunnels because it is a setting, and next to agentLive,
// which is the only thing that tells "routes none" apart from "nobody to ask".
func TestWebConfigRelaysClientIngress(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	av := fakeAgentView{status: &AgentStatus{
		Ingress: []AgentIngress{{
			Mode:   "native",
			Domain: "shop.test",
			Controller: &AgentIngressController{
				Namespace: "ingress-nginx",
				Service:   "ingress-nginx-controller",
				HTTPPort:  80,
				HTTPSPort: 443,
			},
		}},
	}}

	var resp webConfigResponse
	doJSON(t, testServer(t, upstream, av), "GET", "/.cornus/web/config", &resp)
	if !resp.AgentLive {
		t.Fatalf("agentLive = false, want true")
	}
	if len(resp.ClientIngress) != 1 {
		t.Fatalf("clientIngress = %+v, want one entry", resp.ClientIngress)
	}
	got := resp.ClientIngress[0]
	if got.Mode != "native" || got.Domain != "shop.test" {
		t.Errorf("mode/domain = %q/%q, want native/shop.test", got.Mode, got.Domain)
	}
	if got.Controller == nil || got.Controller.Service != "ingress-nginx-controller" || got.Controller.HTTPSPort != 443 {
		t.Errorf("controller = %+v", got.Controller)
	}
}

// No agent answered: the field is ABSENT, not an empty list. The UI reads
// agentLive to choose between "this client routes no ingress" and "the setting is
// unknown", so a response that reported an empty list here alongside agentLive
// would still be honest — but a handler that invented an entry, or that called
// Status() twice and straddled an agent restart, would not.
func TestWebConfigOmitsClientIngressWithoutAnAgent(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)

	var resp webConfigResponse
	rec := doJSON(t, testServer(t, upstream, fakeAgentView{}), "GET", "/.cornus/web/config", &resp)
	if resp.AgentLive {
		t.Errorf("agentLive = true, want false when no agent answers")
	}
	if len(resp.ClientIngress) != 0 {
		t.Errorf("clientIngress = %+v, want none", resp.ClientIngress)
	}
	if strings.Contains(rec.Body.String(), "clientIngress") {
		t.Errorf("response names clientIngress with no agent to ask: %s", rec.Body.String())
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
	// The tunnel names the compose service, not only the resource: the UI puts
	// service first in every project table, and the forward rows beside these
	// carry nothing else to join on.
	if got := resp.Tunnels[0].Service; got != "web" {
		t.Errorf("tunnel service: got %q, want %q", got, "web")
	}
	if len(resp.Forwards["web"]) != 1 || len(resp.Banners) != 1 {
		t.Errorf("forwards/banners: %+v", resp)
	}
}

// TestWebTunnelServiceEmptyOutsideProject pins the other half of the join: a
// deployment the loaded project has no plan for gets no invented service name.
func TestWebTunnelServiceEmptyOutsideProject(t *testing.T) {
	upstream := fakeCornusServer(t,
		[]api.DeployStatus{{Name: "stray"}},
		map[string]api.TunnelStatus{"stray": {Active: true, URL: "https://y.ngrok.app", Port: 80}})
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})

	var resp webTunnelsResponse
	doJSON(t, s, "GET", "/.cornus/web/tunnels", &resp)
	if len(resp.Tunnels) != 1 {
		t.Fatalf("tunnels: %+v", resp.Tunnels)
	}
	if got := resp.Tunnels[0].Service; got != "" {
		t.Errorf("tunnel service for a deployment outside the project: got %q, want empty", got)
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

// TestGuardHostAllowedHosts covers the widened pin an off-host bind uses
// (`cornus web --allow-non-loopback --allow-host box.lan`). The negative case is
// the point: naming a host must ADD one name, not turn the guard into a pass —
// an unnamed foreign Host is still refused, and the refusal names the flag that
// would accept it, because that message is a browser's only clue.
func TestGuardHostAllowedHosts(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	s.cfg.AllowedHosts = []string{"box.lan", "192.168.1.5"}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{
		"box.lan:8080":     http.StatusOK,
		"BOX.LAN":          http.StatusOK,
		"192.168.1.5:8080": http.StatusOK,
		"127.0.0.1:8080":   http.StatusOK,
		"evil.example.com": http.StatusMisdirectedRequest,
		"192.168.1.6:8080": http.StatusMisdirectedRequest,
	} {
		req := httptest.NewRequest("GET", "/.cornus/web/config", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, rec.Code, want)
		}
		if want == http.StatusMisdirectedRequest && !strings.Contains(rec.Body.String(), "--allow-host") {
			t.Errorf("Host %q: refusal body %q does not name --allow-host", host, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// TestGuardHostAllowAnyHost covers the other off-host shape: an operator who
// bound a wildcard and named nothing. The pin cannot be enforced against names
// we were never told, so it is dropped wholesale — including for the Host that
// would otherwise be the rebinding attack, which is why `cornus web` warns.
func TestGuardHostAllowAnyHost(t *testing.T) {
	upstream := fakeCornusServer(t, nil, nil)
	s := testServer(t, upstream, fakeAgentView{status: &AgentStatus{}})
	s.cfg.AllowAnyHost = true
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"127.0.0.1:8080", "192.168.1.5:8080", "evil.example.com"} {
		req := httptest.NewRequest("GET", "/.cornus/web/config", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("Host %q with AllowAnyHost: got %d, want 200", host, rec.Code)
		}
	}
}
