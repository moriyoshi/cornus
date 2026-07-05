package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// fakeDocker is a minimal in-memory stand-in for the Docker Engine API, enough
// to exercise the dockerhost backend without a real daemon.
type fakeDocker struct {
	mu         sync.Mutex
	pulled     []string
	created    []createBody
	createdIDs []string
	started    []string
	removed    []string
	// removedWithVolumes records, per removed container id, whether the DELETE
	// carried v=1 (docker rm -v parity: reap anonymous volumes).
	removedWithVolumes map[string]bool
	// containers returned by /containers/json, keyed by id.
	containers map[string]map[string]any
	// pendingPorts holds the host ports a created container asked to publish;
	// hostPorts maps an allocated host port -> owning container id. Like
	// dockerd, the fake allocates at /start (failing with "port is already
	// allocated" on conflict) and releases on DELETE.
	pendingPorts map[string][]string
	hostPorts    map[string]string
	// networks: name -> labels. connects records "net:containerID";
	// netRemoved records deleted network names; netCreateBodies keeps the raw
	// create body per network name.
	networks        map[string]map[string]string
	netCreateBodies map[string]map[string]any
	connects        []string
	netRemoved      []string
	// removedVolumes records DELETE /volumes/{name} calls (compose down --volumes).
	removedVolumes []string
	// volCreateBodies keeps the raw POST /volumes/create body per volume name.
	volCreateBodies map[string]map[string]any
	// images marks refs the daemon already has locally (for GET /images/{ref}/json).
	images map[string]bool
}

// networkMembers counts containers whose cornus.networks label names net.
func (f *fakeDocker) networkMembers(net string) int {
	n := 0
	for _, c := range f.containers {
		labels, _ := c["Labels"].(map[string]string)
		for _, m := range strings.Split(labels[labelNetworks], ",") {
			if m == net {
				n++
			}
		}
	}
	return n
}

func (f *fakeDocker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/images/create", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.pulled = append(f.pulled, r.URL.Query().Get("fromImage")+":"+r.URL.Query().Get("tag"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"Pulled"}`))
	})
	// GET /images/{ref}/json — image inspect, used by imageExists (skip-pull).
	mux.HandleFunc("/images/", func(w http.ResponseWriter, r *http.Request) {
		ref, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/images/"), "/json")
		if !ok || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		present := f.images[ref]
		f.mu.Unlock()
		if !present {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "sha256:0000"})
	})
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var body createBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		name := r.URL.Query().Get("name")
		f.mu.Lock()
		id := "id-" + name
		f.created = append(f.created, body)
		f.createdIDs = append(f.createdIDs, id)
		if f.containers == nil {
			f.containers = map[string]map[string]any{}
		}
		f.containers[id] = map[string]any{
			"Id": id, "Image": body.Image, "State": "running", "Labels": body.Labels,
		}
		if f.pendingPorts == nil {
			f.pendingPorts = map[string][]string{}
		}
		for _, pbs := range body.HostConfig.PortBindings {
			for _, pb := range pbs {
				f.pendingPorts[id] = append(f.pendingPorts[id], pb.HostPort)
			}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		// Parse the label filter and return matching containers.
		var filters map[string][]string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		want := ""
		if len(filters["label"]) > 0 {
			want = filters["label"][0]
		}
		f.mu.Lock()
		var out []map[string]any
		for _, c := range f.containers {
			labels, _ := c["Labels"].(map[string]string)
			if labelMatches(labels, want) {
				out = append(out, c)
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/networks/create", func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		name, _ := raw["Name"].(string)
		labels := map[string]string{}
		if m, ok := raw["Labels"].(map[string]any); ok {
			for k, v := range m {
				if s, ok := v.(string); ok {
					labels[k] = s
				}
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.networks[name]; ok {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if f.networks == nil {
			f.networks = map[string]map[string]string{}
		}
		if f.netCreateBodies == nil {
			f.netCreateBodies = map[string]map[string]any{}
		}
		f.networks[name] = labels
		f.netCreateBodies[name] = raw
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "net-" + name})
	})
	mux.HandleFunc("/networks/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/networks/")
		name, action, _ := strings.Cut(rest, "/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case action == "connect" && r.Method == http.MethodPost:
			var body struct {
				Container string `json:"Container"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.connects = append(f.connects, name+":"+body.Container)
			w.WriteHeader(http.StatusOK)
		case action == "" && r.Method == http.MethodGet:
			labels, ok := f.networks[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			containers := map[string]any{}
			for i := 0; i < f.networkMembers(name); i++ {
				containers[strings.Repeat("m", i+1)] = map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Labels": labels, "Containers": containers})
		case action == "" && r.Method == http.MethodDelete:
			delete(f.networks, name)
			f.netRemoved = append(f.netRemoved, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/volumes/create", func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		name, _ := raw["Name"].(string)
		f.mu.Lock()
		if f.volCreateBodies == nil {
			f.volCreateBodies = map[string]map[string]any{}
		}
		f.volCreateBodies[name] = raw
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Name": name})
	})
	mux.HandleFunc("/volumes/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/volumes/")
		switch r.Method {
		case http.MethodGet:
			// Synthetic daemon-host Mountpoint — the fake never needs a real
			// directory to exist; ApplyWithMounts only carries the string through
			// to the app/caretaker containers' bind Source.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"Name":       name,
				"Mountpoint": "/var/lib/docker/volumes/" + name + "/_data",
			})
		case http.MethodDelete:
			// A "missing-" prefix models a volume dockerd doesn't know (404), so the
			// engine's delete-if-exists tolerance can be exercised.
			if strings.HasPrefix(name, "missing-") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.mu.Lock()
			f.removedVolumes = append(f.removedVolumes, name)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/containers/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/containers/")
		switch {
		case strings.HasSuffix(id, "/json") && r.Method == http.MethodGet:
			cid := strings.TrimSuffix(id, "/json")
			f.mu.Lock()
			c := f.containers[cid]
			f.mu.Unlock()
			if c == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			ip, _ := c["ip"].(string)
			stateStr, _ := c["State"].(string)
			state := map[string]any{
				"Running":  stateStr == "running",
				"ExitCode": c["exitCode"],
			}
			// A container carries a Health object only when its image declared a
			// HEALTHCHECK; the fake mirrors that by emitting it only when set.
			if h, ok := c["health"].(string); ok {
				state["Health"] = map[string]any{"Status": h}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"NetworkSettings": map[string]any{"IPAddress": ip},
				"State":           state,
			})
		case strings.HasSuffix(id, "/start"):
			cid := strings.TrimSuffix(id, "/start")
			f.mu.Lock()
			// Host ports are allocated at start, mirroring dockerd: a second
			// container binding an already-held port fails here.
			for _, p := range f.pendingPorts[cid] {
				if owner, taken := f.hostPorts[p]; taken && owner != cid {
					f.mu.Unlock()
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"driver failed programming external connectivity: Bind for 0.0.0.0:` + p + ` failed: port is already allocated"}`))
					return
				}
			}
			if f.hostPorts == nil {
				f.hostPorts = map[string]string{}
			}
			for _, p := range f.pendingPorts[cid] {
				f.hostPorts[p] = cid
			}
			f.started = append(f.started, cid)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			f.mu.Lock()
			f.removed = append(f.removed, id)
			if f.removedWithVolumes == nil {
				f.removedWithVolumes = map[string]bool{}
			}
			f.removedWithVolumes[id] = r.URL.Query().Get("v") == "1"
			for p, owner := range f.hostPorts {
				if owner == id {
					delete(f.hostPorts, p)
				}
			}
			delete(f.pendingPorts, id)
			delete(f.containers, id)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return mux
}

func labelMatches(labels map[string]string, want string) bool {
	k, v, ok := strings.Cut(want, "=")
	if !ok {
		return false
	}
	return labels[k] == v
}

// TestRemoveVolume checks the deploy.VolumeRemover path: RemoveVolume issues a
// DELETE /volumes/{name} for the project-scoped name, and a 404 (already gone) is
// tolerated as delete-if-exists.
func TestRemoveVolume(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	if err := b.RemoveVolume(context.Background(), "proj_cache"); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
	f.mu.Lock()
	got := append([]string(nil), f.removedVolumes...)
	f.mu.Unlock()
	if len(got) != 1 || got[0] != "proj_cache" {
		t.Fatalf("removedVolumes = %v, want [proj_cache]", got)
	}
	if err := b.RemoveVolume(context.Background(), "missing-x"); err != nil {
		t.Fatalf("RemoveVolume of an absent volume should succeed (404 tolerated), got %v", err)
	}
}

func newTestBackend(t *testing.T, f *fakeDocker) *Backend {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	// Existing Apply tests exercise create/replicas/networks with host binds, not
	// the privilege policy, so use a permissive policy here. Policy enforcement has
	// its own tests (policy_test.go).
	b, err := New(WithPolicy(PermissivePolicy()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// TestStatusHealthAndExitCode confirms Status inspects each instance and
// surfaces the container's health string plus, for a terminated container, its
// exit code — while leaving ExitCode nil for a running one.
func TestStatusHealthAndExitCode(t *testing.T) {
	f := &fakeDocker{containers: map[string]map[string]any{
		"id-web-0": {
			"Id": "id-web-0", "Image": "web:v1", "State": "running",
			"Labels": map[string]string{"cornus.app": "web"},
			"health": "healthy",
		},
		"id-web-1": {
			"Id": "id-web-1", "Image": "web:v1", "State": "exited",
			"Labels":   map[string]string{"cornus.app": "web"},
			"exitCode": 17,
		},
	}}
	b := newTestBackend(t, f)

	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byID := map[string]api.InstanceStatus{}
	for _, inst := range st.Instances {
		byID[inst.ID] = inst
	}
	if len(byID) != 2 {
		t.Fatalf("instances = %d, want 2", len(byID))
	}

	running := byID["id-web-0"]
	if !running.Running || running.Health != "healthy" {
		t.Fatalf("running instance = %+v, want Running=true Health=healthy", running)
	}
	if running.ExitCode != nil {
		t.Fatalf("running instance ExitCode = %v, want nil", *running.ExitCode)
	}

	exited := byID["id-web-1"]
	if exited.Running || exited.ExitCode == nil || *exited.ExitCode != 17 {
		t.Fatalf("exited instance = %+v (ExitCode=%v), want Running=false ExitCode=17", exited, exited.ExitCode)
	}
	// No HEALTHCHECK on this container -> empty health.
	if exited.Health != "" {
		t.Fatalf("exited instance Health = %q, want empty", exited.Health)
	}
}

func TestToCreateBodyPrivileged(t *testing.T) {
	// Off by default.
	if b := toCreateBody(api.DeploySpec{Name: "x", Image: "img"}); b.HostConfig.Privileged {
		t.Fatal("Privileged should default to false")
	}
	// Opt-in threads through to the Docker HostConfig.
	if b := toCreateBody(api.DeploySpec{Name: "x", Image: "img", Privileged: true}); !b.HostConfig.Privileged {
		t.Fatal("Privileged=true should set HostConfig.Privileged")
	}
}

// TestToCreateBodySecurityAndNetKeys asserts the security & networking batch
// (cap_add/cap_drop/security_opt/group_add/sysctls/extra_hosts/dns/dns_search/
// dns_opt) threads verbatim into the Docker HostConfig.
func TestToCreateBodySecurityAndNetKeys(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:        "x",
		Image:       "img",
		CapAdd:      []string{"NET_ADMIN", "SYS_TIME"},
		CapDrop:     []string{"MKNOD"},
		SecurityOpt: []string{"no-new-privileges:true", "seccomp=unconfined"},
		GroupAdd:    []string{"1001", "staff"},
		Sysctls:     map[string]string{"net.core.somaxconn": "1024"},
		ExtraHosts:  []string{"somehost:162.242.195.82"},
		DNSServers:  []string{"8.8.8.8", "1.1.1.1"},
		DNSSearch:   []string{"example.com"},
		DNSOptions:  []string{"timeout:2", "use-vc"},
	})
	hc := b.HostConfig
	if !reflect.DeepEqual(hc.CapAdd, []string{"NET_ADMIN", "SYS_TIME"}) {
		t.Errorf("CapAdd = %v", hc.CapAdd)
	}
	if !reflect.DeepEqual(hc.CapDrop, []string{"MKNOD"}) {
		t.Errorf("CapDrop = %v", hc.CapDrop)
	}
	// security_opt is passed through verbatim, including the un-mapped seccomp=.
	if !reflect.DeepEqual(hc.SecurityOpt, []string{"no-new-privileges:true", "seccomp=unconfined"}) {
		t.Errorf("SecurityOpt = %v", hc.SecurityOpt)
	}
	if !reflect.DeepEqual(hc.GroupAdd, []string{"1001", "staff"}) {
		t.Errorf("GroupAdd = %v", hc.GroupAdd)
	}
	if hc.Sysctls["net.core.somaxconn"] != "1024" {
		t.Errorf("Sysctls = %v", hc.Sysctls)
	}
	if !reflect.DeepEqual(hc.ExtraHosts, []string{"somehost:162.242.195.82"}) {
		t.Errorf("ExtraHosts = %v", hc.ExtraHosts)
	}
	if !reflect.DeepEqual(hc.Dns, []string{"8.8.8.8", "1.1.1.1"}) {
		t.Errorf("Dns = %v", hc.Dns)
	}
	if !reflect.DeepEqual(hc.DnsSearch, []string{"example.com"}) {
		t.Errorf("DnsSearch = %v", hc.DnsSearch)
	}
	if !reflect.DeepEqual(hc.DnsOptions, []string{"timeout:2", "use-vc"}) {
		t.Errorf("DnsOptions = %v", hc.DnsOptions)
	}
}

func TestApplyCreatesReplicas(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/web:v1",
		Replicas: 2,
		Env:      map[string]string{"FOO": "bar"},
		Ports:    []api.PortMapping{{Host: 8080, Container: 80}},
		Mounts:   []api.Mount{{Source: "/data", Target: "/var/data", ReadOnly: true}},
		Restart:  "always",
	}
	st, err := b.Apply(ctx, spec)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(f.pulled) != 1 || !strings.HasPrefix(f.pulled[0], "localhost:5000/web") {
		t.Fatalf("pulled = %v", f.pulled)
	}
	if len(f.created) != 2 {
		t.Fatalf("created %d containers, want 2", len(f.created))
	}
	c := f.created[0]
	if c.Labels["cornus.app"] != "web" || c.Labels["cornus.managed"] != "true" {
		t.Fatalf("labels = %v", c.Labels)
	}
	if len(c.Env) != 1 || c.Env[0] != "FOO=bar" {
		t.Fatalf("env = %v", c.Env)
	}
	if c.HostConfig.RestartPolicy.Name != "always" {
		t.Fatalf("restart = %q", c.HostConfig.RestartPolicy.Name)
	}
	if pb, ok := c.HostConfig.PortBindings["80/tcp"]; !ok || pb[0].HostPort != "8080" {
		t.Fatalf("portBindings = %v", c.HostConfig.PortBindings)
	}
	if len(c.HostConfig.Binds) != 1 || c.HostConfig.Binds[0] != "/data:/var/data:ro" {
		t.Fatalf("binds = %v", c.HostConfig.Binds)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("status instances = %d, want 2", len(st.Instances))
	}
	if len(f.started) != 2 {
		t.Fatalf("started = %v", f.started)
	}
}

// TestApplyStampsOriginLabels confirms the deployment's lineage is written onto
// the created container as cornus.origin.* labels.
func TestApplyStampsOriginLabels(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	spec := api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/web:v1",
		Replicas: 1,
		Origin: &api.Origin{
			Project: "proj", Host: "laptop", User: "alice", Subject: "user:alice",
			Git: &api.GitOrigin{Commit: "deadbeef", Dirty: true},
		},
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	l := f.created[0].Labels
	if l["cornus.origin.project"] != "proj" || l["cornus.origin.user"] != "alice" {
		t.Fatalf("origin labels = %v", l)
	}
	if l["cornus.origin.subject"] != "user:alice" || l["cornus.origin.git.dirty"] != "true" {
		t.Fatalf("origin labels = %v", l)
	}
}

// TestStatusReadsOriginLabels confirms Status reconstructs the origin from the
// container labels.
func TestStatusReadsOriginLabels(t *testing.T) {
	f := &fakeDocker{containers: map[string]map[string]any{
		"id-web-0": {
			"Id": "id-web-0", "Image": "web:v1", "State": "running",
			"Labels": map[string]string{
				"cornus.app":            "web",
				"cornus.origin.project": "proj",
				"cornus.origin.host":    "laptop",
				"cornus.origin.subject": "user:alice",
			},
		},
	}}
	b := newTestBackend(t, f)
	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Origin == nil {
		t.Fatal("Status.Origin is nil, want reconstructed origin")
	}
	if st.Origin.Project != "proj" || st.Origin.Host != "laptop" || st.Origin.Subject != "user:alice" {
		t.Fatalf("Status.Origin = %+v", st.Origin)
	}
}

// newTestBackendOpts is newTestBackend with extra Backend options (e.g. the
// skip-pull predicate), pointing the client at the fake daemon.
func newTestBackendOpts(t *testing.T, f *fakeDocker, opts ...Option) *Backend {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	b, err := New(append([]Option{WithPolicy(PermissivePolicy())}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// TestApplySkipsPullWhenLocalAndPresent confirms that in re-export mode a
// local-registry ref already in the daemon is not pulled (avoiding the self-pull
// round-trip), while the container is still created from it.
func TestApplySkipsPullWhenLocalAndPresent(t *testing.T) {
	f := &fakeDocker{images: map[string]bool{"app:v1": true}}
	b := newTestBackendOpts(t, f, WithSkipPullIfLocal(localRefTrue))
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "app", Image: "app:v1", Replicas: 1}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.pulled) != 0 {
		t.Fatalf("pulled = %v, want no pull (image present locally)", f.pulled)
	}
	if len(f.created) != 1 || f.created[0].Image != "app:v1" {
		t.Fatalf("created = %v, want one container from app:v1", f.created)
	}
}

// TestApplyPullsWhenLocalRefAbsent confirms the shortcut falls back to a normal
// pull when the predicate matches but the daemon does not have the image.
func TestApplyPullsWhenLocalRefAbsent(t *testing.T) {
	f := &fakeDocker{} // daemon has nothing
	b := newTestBackendOpts(t, f, WithSkipPullIfLocal(localRefTrue))
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "app", Image: "app:v1", Replicas: 1}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.pulled) != 1 {
		t.Fatalf("pulled = %v, want one pull (image absent locally)", f.pulled)
	}
}

// TestApplyPullsExternalRefEvenIfPresent confirms a non-local ref is always
// pulled even when present: the predicate returns false for it, so the shortcut
// never engages (external images must stay fresh).
func TestApplyPullsExternalRefEvenIfPresent(t *testing.T) {
	f := &fakeDocker{images: map[string]bool{"docker.io/library/nginx:latest": true}}
	// Predicate returns false for external refs.
	b := newTestBackendOpts(t, f, WithSkipPullIfLocal(func(string) bool { return false }))
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "n", Image: "docker.io/library/nginx:latest", Replicas: 1}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.pulled) != 1 {
		t.Fatalf("pulled = %v, want one pull for external ref", f.pulled)
	}
}

func localRefTrue(string) bool { return true }

// TestApplyReplicasPublishHostPortsOnFirstOnly is the regression test for the
// replicas>1 + published-ports bug: the create body used to be shared verbatim
// across replicas, so replica 1 re-bound the same host port and dockerd failed
// it with "port is already allocated" — after the old instances were already
// removed. Host ports must publish on replica 0 only (containerd-backend
// parity); replicas 1+ keep ExposedPorts but carry no host bindings.
func TestApplyReplicasPublishHostPortsOnFirstOnly(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/web:v1",
		Replicas: 2,
		Ports:    []api.PortMapping{{Host: 8080, Container: 80}},
	}
	st, err := b.Apply(ctx, spec)
	if err != nil {
		t.Fatalf("Apply with replicas=2 and a published port must succeed: %v", err)
	}
	if len(st.Instances) != 2 || len(f.started) != 2 {
		t.Fatalf("instances = %d started = %v, want 2 running replicas", len(st.Instances), f.started)
	}
	if len(f.created) != 2 {
		t.Fatalf("created %d containers, want 2", len(f.created))
	}
	// Replica 0 publishes the host port.
	if pb, ok := f.created[0].HostConfig.PortBindings["80/tcp"]; !ok || pb[0].HostPort != "8080" {
		t.Fatalf("replica 0 portBindings = %v, want 80/tcp -> 8080", f.created[0].HostConfig.PortBindings)
	}
	// Replica 1 must not bind host ports; the container port stays exposed.
	if pb := f.created[1].HostConfig.PortBindings; len(pb) != 0 {
		t.Fatalf("replica 1 must not publish host ports: %v", pb)
	}
	if _, ok := f.created[1].ExposedPorts["80/tcp"]; !ok {
		t.Fatalf("replica 1 exposedPorts = %v, want 80/tcp kept", f.created[1].ExposedPorts)
	}
	// Only instance 0 holds the host binding on the daemon.
	if owner := f.hostPorts["8080"]; owner != "id-cornus-web-0" {
		t.Fatalf("host port 8080 held by %q, want id-cornus-web-0", owner)
	}
	if len(f.hostPorts) != 1 {
		t.Fatalf("hostPorts = %v, want exactly one allocation", f.hostPorts)
	}
}

// TestDeleteRemovesAnonymousVolumes is the regression test for the
// volume-leak bug: Delete must send v=1 on container DELETE (docker rm -v
// parity per the deploy.Backend contract) so dockerd reaps the container's
// anonymous volumes.
func TestDeleteRemovesAnonymousVolumes(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "vol",
		Image:   "localhost:5000/vol:v1",
		Volumes: []api.VolumeSpec{{Target: "/data"}}, // anonymous volume
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(ctx, "vol"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.removed) != 1 {
		t.Fatalf("removed = %v, want 1", f.removed)
	}
	if !f.removedWithVolumes[f.removed[0]] {
		t.Fatalf("container %s removed without v=1: anonymous volumes leak", f.removed[0])
	}
}

// TestToCreateBodyAnonymousVolume confirms a managed volume becomes a Docker
// anonymous "volume"-type mount (empty Source) at the target.
func TestToCreateBodyAnonymousVolume(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:    "x",
		Image:   "img",
		Volumes: []api.VolumeSpec{{Target: "/data"}, {Target: "/cache", ReadOnly: true}},
	})
	if len(b.HostConfig.Mounts) != 2 {
		t.Fatalf("mounts = %+v, want 2", b.HostConfig.Mounts)
	}
	m := b.HostConfig.Mounts[0]
	if m.Type != "volume" || m.Source != "" || m.Target != "/data" {
		t.Fatalf("mount[0] = %+v, want anonymous volume at /data", m)
	}
	if m2 := b.HostConfig.Mounts[1]; m2.Target != "/cache" || !m2.ReadOnly {
		t.Fatalf("mount[1] = %+v, want read-only /cache", m2)
	}
}

// TestToCreateBodyNamedVolume confirms a named volume becomes a Docker
// "volume"-type mount whose Source is the (project-scoped) volume name, so Docker
// shares one persistent volume across every container that names it.
func TestToCreateBodyNamedVolume(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:    "x",
		Image:   "img",
		Volumes: []api.VolumeSpec{{Name: "proj_cache", Target: "/var/cache"}},
	})
	if len(b.HostConfig.Mounts) != 1 {
		t.Fatalf("mounts = %+v, want 1", b.HostConfig.Mounts)
	}
	m := b.HostConfig.Mounts[0]
	if m.Type != "volume" || m.Source != "proj_cache" || m.Target != "/var/cache" {
		t.Fatalf("mount = %+v, want named volume proj_cache at /var/cache", m)
	}
}

// TestToCreateBodyEntrypoint confirms an explicit entrypoint reaches Docker's
// Entrypoint slot alongside Command in Cmd, and that no override leaves
// Entrypoint unset (image default).
func TestToCreateBodyEntrypoint(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:       "x",
		Image:      "img",
		Entrypoint: []string{"/bin/sh"},
		Command:    []string{"-c", "sleep infinity"},
	})
	if len(b.Entrypoint) != 1 || b.Entrypoint[0] != "/bin/sh" {
		t.Fatalf("entrypoint = %v, want [/bin/sh]", b.Entrypoint)
	}
	if len(b.Cmd) != 2 || b.Cmd[0] != "-c" {
		t.Fatalf("cmd = %v, want [-c, sleep infinity]", b.Cmd)
	}
	if b := toCreateBody(api.DeploySpec{Name: "x", Image: "img", Command: []string{"run"}}); len(b.Entrypoint) != 0 {
		t.Fatalf("entrypoint without override = %v, want unset", b.Entrypoint)
	}
}

// TestToCreateBodyNetworks confirms the primary user-defined network rides the
// create body (NetworkMode + endpoint aliases) and the full membership list is
// recorded on the GC label; no networks => none of that.
func TestToCreateBodyNetworks(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "proj_front", Aliases: []string{"web", "www"}},
			{Name: "proj_back"},
		},
	})
	if b.HostConfig.NetworkMode != "proj_front" {
		t.Errorf("NetworkMode = %q, want the primary proj_front", b.HostConfig.NetworkMode)
	}
	if b.NetworkingConfig == nil {
		t.Fatal("NetworkingConfig missing")
	}
	ep, ok := b.NetworkingConfig.EndpointsConfig["proj_front"]
	if !ok || len(ep.Aliases) != 2 || ep.Aliases[0] != "web" {
		t.Errorf("EndpointsConfig = %+v, want proj_front with aliases [web www]", b.NetworkingConfig.EndpointsConfig)
	}
	if _, ok := b.NetworkingConfig.EndpointsConfig["proj_back"]; ok {
		t.Error("secondary network must not be in the create body (connected separately)")
	}
	if got := b.Labels[labelNetworks]; got != "proj_front,proj_back" {
		t.Errorf("networks label = %q, want proj_front,proj_back", got)
	}

	plain := toCreateBody(api.DeploySpec{Name: "x", Image: "img"})
	if plain.NetworkingConfig != nil || plain.HostConfig.NetworkMode != "" || plain.Labels[labelNetworks] != "" {
		t.Errorf("network-less spec leaked network config: %+v", plain)
	}
}

// TestApplyAndDeleteWithNetworks drives the full lifecycle against the fake
// daemon: Apply ensures both networks (labeling them managed), joins the
// primary at create and connects the secondary before start; Delete removes
// the containers then reaps the now-unused managed networks — but leaves a
// pre-existing external network (no managed label) alone.
func TestApplyAndDeleteWithNetworks(t *testing.T) {
	f := &fakeDocker{networks: map[string]map[string]string{
		"ext": nil, // pre-existing external network, NOT cornus-managed
	}}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "api",
		Image: "localhost:5000/api:v1",
		Networks: []api.NetworkAttachment{
			{Name: "proj_default", Aliases: []string{"api"}},
			{Name: "ext"},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// proj_default was created with the managed label; ext already existed
	// (ensure got 409 and moved on).
	if labels := f.networks["proj_default"]; labels["cornus.managed"] != "true" {
		t.Fatalf("proj_default labels = %v, want cornus.managed=true", labels)
	}
	// The secondary network was connected before start.
	if len(f.connects) != 1 || f.connects[0] != "ext:id-cornus-api-0" {
		t.Fatalf("connects = %v, want [ext:id-cornus-api-0]", f.connects)
	}
	if len(f.started) != 1 {
		t.Fatalf("started = %v, want 1", f.started)
	}

	if err := b.Delete(ctx, "api"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// The managed, now-empty network is reaped; the external one survives.
	if _, ok := f.networks["proj_default"]; ok {
		t.Error("proj_default should be reaped after its last member is deleted")
	}
	if _, ok := f.networks["ext"]; !ok {
		t.Error("external network ext must never be reaped")
	}
}

// TestNetworkEnsureDriver confirms real Docker drivers pass through while the
// kubernetes-fabric pseudo-drivers (services/policy/cilium) are dropped so
// Docker uses its default bridge (which already gives DNS + isolation).
func TestNetworkEnsureDriver(t *testing.T) {
	f := &fakeDocker{}
	newTestBackend(t, f) // sets DOCKER_HOST; we use a fresh client below
	c, err := newEngineClient()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()

	cases := map[string]bool{ // network name -> expect a Driver key sent
		"real-bridge": true,  // driver "bridge"
		"pseudo":      false, // driver "policy"
	}
	if err := c.networkEnsure(ctx, api.NetworkAttachment{Name: "real-bridge", Driver: "bridge"}); err != nil {
		t.Fatal(err)
	}
	if err := c.networkEnsure(ctx, api.NetworkAttachment{Name: "pseudo", Driver: "policy"}); err != nil {
		t.Fatal(err)
	}
	for name, wantDriver := range cases {
		body, ok := f.netCreateBodies[name]
		if !ok {
			t.Fatalf("network %q was not created", name)
		}
		_, hasDriver := body["Driver"]
		if hasDriver != wantDriver {
			t.Errorf("network %q: Driver sent=%v, want %v (body=%v)", name, hasDriver, wantDriver, body)
		}
	}
}

// TestNetworkEnsureIPAMAndToggles checks that a network's compose ipam config,
// the internal/attachable/enable_ipv6 toggles, and user labels reach the
// /networks/create body, with cornus's own management label preserved.
func TestNetworkEnsureIPAMAndToggles(t *testing.T) {
	f := &fakeDocker{}
	newTestBackend(t, f)
	c, err := newEngineClient()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()

	net := api.NetworkAttachment{
		Name:       "proj_back",
		Driver:     "bridge",
		Subnet:     "172.28.0.0/16",
		Gateway:    "172.28.0.1",
		IPRange:    "172.28.5.0/24",
		Internal:   true,
		Attachable: true,
		EnableIPv6: true,
		Labels:     map[string]string{"team": "infra"},
	}
	if err := c.networkEnsure(ctx, net); err != nil {
		t.Fatal(err)
	}
	body := f.netCreateBodies["proj_back"]
	if body == nil {
		t.Fatal("network not created")
	}
	if body["Internal"] != true || body["Attachable"] != true || body["EnableIPv6"] != true {
		t.Errorf("toggles = internal:%v attachable:%v enableIPv6:%v", body["Internal"], body["Attachable"], body["EnableIPv6"])
	}
	ipam, _ := body["IPAM"].(map[string]any)
	cfgs, _ := ipam["Config"].([]any)
	if len(cfgs) != 1 {
		t.Fatalf("IPAM.Config = %v", ipam)
	}
	cfg, _ := cfgs[0].(map[string]any)
	if cfg["Subnet"] != "172.28.0.0/16" || cfg["Gateway"] != "172.28.0.1" || cfg["IPRange"] != "172.28.5.0/24" {
		t.Errorf("IPAM.Config[0] = %v", cfg)
	}
	labels, _ := body["Labels"].(map[string]any)
	if labels["team"] != "infra" || labels["cornus.managed"] != "true" {
		t.Errorf("Labels = %v, want team=infra + cornus.managed=true", labels)
	}
}

// TestVolumeEnsureDriverOpts checks that a named volume's compose driver /
// driver_opts / labels reach the /volumes/create body, management label kept.
func TestVolumeEnsureDriverOpts(t *testing.T) {
	f := &fakeDocker{}
	newTestBackend(t, f)
	c, err := newEngineClient()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()

	v := api.VolumeSpec{
		Name:       "proj_data",
		Target:     "/var/lib/data",
		Driver:     "local",
		DriverOpts: map[string]string{"type": "nfs", "device": ":/exports"},
		Labels:     map[string]string{"env": "prod"},
	}
	if err := c.volumeEnsure(ctx, v); err != nil {
		t.Fatal(err)
	}
	body := f.volCreateBodies["proj_data"]
	if body == nil {
		t.Fatal("volume not created")
	}
	if body["Driver"] != "local" {
		t.Errorf("Driver = %v", body["Driver"])
	}
	opts, _ := body["DriverOpts"].(map[string]any)
	if opts["type"] != "nfs" || opts["device"] != ":/exports" {
		t.Errorf("DriverOpts = %v", opts)
	}
	labels, _ := body["Labels"].(map[string]any)
	if labels["env"] != "prod" || labels["cornus.managed"] != "true" {
		t.Errorf("Labels = %v", labels)
	}
}

// TestApplyWiresNamedVolumeAndEndpoint drives Apply end to end: a named volume
// carrying a driver is created via /volumes/create; the endpoint MAC / IPv6
// pins ride the primary network's create-body endpoint; and priority ordering
// makes the highest-priority network the primary interface.
func TestApplyWiresNamedVolumeAndEndpoint(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "api",
		Image: "localhost:5000/api:v1",
		Volumes: []api.VolumeSpec{
			{Name: "proj_data", Target: "/data", Driver: "local", DriverOpts: map[string]string{"type": "tmpfs"}},
			{Name: "proj_plain", Target: "/plain"}, // no driver/opts/labels: not pre-created
		},
		Networks: []api.NetworkAttachment{
			{Name: "proj_low", Priority: 1},
			{Name: "proj_high", Priority: 10, MAC: "02:42:ac:11:00:05", IPv6: "2001:db8::5"},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The driver-bearing named volume was pre-created; the plain one was not.
	if _, ok := f.volCreateBodies["proj_data"]; !ok {
		t.Error("named volume with a driver must be created via /volumes/create")
	}
	if _, ok := f.volCreateBodies["proj_plain"]; ok {
		t.Error("plain named volume must NOT be pre-created (dockerd auto-provisions it)")
	}

	// The highest-priority network is the primary (create body), carrying its
	// endpoint MAC and IPv6 pin.
	body := f.created[0]
	if body.HostConfig.NetworkMode != "proj_high" {
		t.Errorf("primary network = %q, want proj_high (highest priority)", body.HostConfig.NetworkMode)
	}
	ep := body.NetworkingConfig.EndpointsConfig["proj_high"]
	if ep.MacAddress != "02:42:ac:11:00:05" {
		t.Errorf("endpoint MacAddress = %q", ep.MacAddress)
	}
	if ep.IPAMConfig == nil || ep.IPAMConfig.IPv6Address != "2001:db8::5" {
		t.Errorf("endpoint IPv6 = %+v", ep.IPAMConfig)
	}
	// The lower-priority network is connected separately, after create.
	if len(f.connects) != 1 || f.connects[0] != "proj_low:id-cornus-api-0" {
		t.Errorf("connects = %v, want [proj_low:id-cornus-api-0]", f.connects)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()
	spec := api.DeploySpec{Name: "api", Image: "localhost:5000/api:v1"}

	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	// Second apply must have removed the first instance before recreating.
	if len(f.removed) != 1 {
		t.Fatalf("removed = %v, want exactly 1 on recreate", f.removed)
	}
	st, err := b.Status(ctx, "api")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 1 || !st.Instances[0].Running {
		t.Fatalf("status = %+v", st)
	}
}

// TestApplyRedeployKeepsNetwork is the regression test for the network-reaping
// bug in Apply's recreate step: a redeploy of a networked app must NOT delete
// the deployment's own user-defined network. On the second Apply, networkEnsure
// is a 409 no-op, then the old container is removed; the pre-fix code reused the
// full network-reaping Delete, which saw proj_default's last member gone and
// deleted the just-ensured network out from under the create body.
func TestApplyRedeployKeepsNetwork(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "api",
		Image: "localhost:5000/api:v1",
		Networks: []api.NetworkAttachment{
			{Name: "proj_default", Aliases: []string{"api"}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if labels := f.networks["proj_default"]; labels["cornus.managed"] != "true" {
		t.Fatalf("proj_default labels = %v, want cornus.managed=true", labels)
	}

	// Redeploy the same spec. The managed network must survive the recreate.
	st, err := b.Apply(ctx, spec)
	if err != nil {
		t.Fatalf("redeploy apply: %v", err)
	}
	if _, ok := f.networks["proj_default"]; !ok {
		t.Fatal("proj_default was reaped during redeploy: the just-ensured network must survive Apply's recreate step")
	}
	for _, n := range f.netRemoved {
		if n == "proj_default" {
			t.Fatal("Apply's recreate step must not reap the deployment's own network")
		}
	}
	if len(st.Instances) != 1 || !st.Instances[0].Running {
		t.Fatalf("redeploy status = %+v, want 1 running instance on the kept network", st)
	}
}

// TestImagePullReportsInStreamError is the regression test for the swallowed
// pull-failure bug: Docker returns HTTP 200 for /images/create and reports a
// failed pull as an {"error":...} object inside the streamed body. imagePull
// must surface that as an error rather than treating the pull as success.
func TestImagePullReportsInStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"Pulling from reg/web"}` + "\n" +
			`{"errorDetail":{"message":"manifest unknown"},"error":"manifest for reg/web:v9 not found"}` + "\n"))
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	c, err := newEngineClient()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	err = c.imagePull(context.Background(), "reg/web:v9")
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("imagePull = %v, want an error carrying the in-stream pull failure", err)
	}
}

// TestImagePullSucceedsOnCleanStream confirms a success-only progress stream
// (no error object) still returns nil after the decode-based drain.
func TestImagePullSucceedsOnCleanStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"Pulling"}` + "\n" + `{"status":"Downloaded"}` + "\n"))
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	c, err := newEngineClient()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := c.imagePull(context.Background(), "reg/web:v1"); err != nil {
		t.Fatalf("imagePull on a clean stream = %v, want nil", err)
	}
}

// TestToCreateBodyExposeOnlyPort is the regression test for the expose-only
// port bug: a port with Host==0 must be exposed to other containers but NOT
// published on a host port (matching the containerd backend's toCNIPorts, which
// skips Host==0). Emitting a HostPort:"0" binding makes dockerd publish it on a
// random host port — an unintended host exposure.
func TestToCreateBodyExposeOnlyPort(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Ports: []api.PortMapping{{Container: 8080, Host: 0}},
	})
	if _, ok := b.ExposedPorts["8080/tcp"]; !ok {
		t.Fatalf("exposedPorts = %v, want 8080/tcp exposed", b.ExposedPorts)
	}
	if pb := b.HostConfig.PortBindings; len(pb) != 0 {
		t.Fatalf("expose-only port (Host==0) must not be published: PortBindings = %v", pb)
	}

	// A real host publish still binds.
	pubd := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Ports: []api.PortMapping{{Container: 8080, Host: 18080}},
	})
	if pb, ok := pubd.HostConfig.PortBindings["8080/tcp"]; !ok || pb[0].HostPort != "18080" {
		t.Fatalf("published port bindings = %v, want 8080/tcp -> 18080", pubd.HostConfig.PortBindings)
	}
	if pubd.HostConfig.PortBindings["8080/tcp"][0].HostIP != "" {
		t.Fatalf("HostIP should be empty when unset: %v", pubd.HostConfig.PortBindings)
	}
}

// TestToCreateBodyPortHostIP asserts a PortMapping.HostIP lands on the Docker
// port binding, restricting the publish to that host interface.
func TestToCreateBodyPortHostIP(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Ports: []api.PortMapping{{Container: 80, Host: 8080, HostIP: "127.0.0.1"}},
	})
	pb, ok := b.HostConfig.PortBindings["80/tcp"]
	if !ok || len(pb) != 1 {
		t.Fatalf("port bindings = %v", b.HostConfig.PortBindings)
	}
	if pb[0].HostIP != "127.0.0.1" || pb[0].HostPort != "8080" {
		t.Fatalf("binding = %+v, want 127.0.0.1:8080", pb[0])
	}
}

// TestToCreateBodyBindSELinux asserts bind options are built as a comma-joined
// list: read-only and the SELinux relabel token, in that order.
func TestToCreateBodyBindSELinux(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Mounts: []api.Mount{
			{Source: "/a", Target: "/a"},
			{Source: "/b", Target: "/b", SELinux: "z"},
			{Source: "/c", Target: "/c", ReadOnly: true},
			{Source: "/d", Target: "/d", ReadOnly: true, SELinux: "Z"},
		},
	})
	want := []string{"/a:/a", "/b:/b:z", "/c:/c:ro", "/d:/d:ro,Z"}
	if !reflect.DeepEqual(b.HostConfig.Binds, want) {
		t.Fatalf("binds = %v, want %v", b.HostConfig.Binds, want)
	}
}

func TestToCreateBodyCommonKeys(t *testing.T) {
	initTrue := true
	b := toCreateBody(api.DeploySpec{
		Name:            "x",
		Image:           "img",
		User:            "1000:2000",
		WorkingDir:      "/srv",
		Hostname:        "web-host",
		Labels:          map[string]string{"role": "web", deploy.LabelApp: "attempted-clobber"},
		StopSignal:      "SIGINT",
		StopGracePeriod: "1m30s",
		Init:            &initTrue,
		TTY:             true,
		StdinOpen:       true,
		ReadOnly:        true,
	})
	if b.User != "1000:2000" {
		t.Errorf("User = %q, want 1000:2000", b.User)
	}
	if b.WorkingDir != "/srv" {
		t.Errorf("WorkingDir = %q, want /srv", b.WorkingDir)
	}
	if b.Hostname != "web-host" {
		t.Errorf("Hostname = %q, want web-host", b.Hostname)
	}
	if b.StopSignal != "SIGINT" {
		t.Errorf("StopSignal = %q, want SIGINT", b.StopSignal)
	}
	if b.StopTimeout == nil || *b.StopTimeout != 90 {
		t.Errorf("StopTimeout = %v, want 90", b.StopTimeout)
	}
	if !b.Tty {
		t.Error("Tty = false, want true")
	}
	if !b.OpenStdin {
		t.Error("OpenStdin = false, want true")
	}
	if b.HostConfig.Init == nil || !*b.HostConfig.Init {
		t.Errorf("HostConfig.Init = %v, want true", b.HostConfig.Init)
	}
	if !b.HostConfig.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = false, want true")
	}
	// User label present, but cornus management labels win on a key clash.
	if b.Labels["role"] != "web" {
		t.Errorf("Labels[role] = %q, want web", b.Labels["role"])
	}
	if b.Labels[deploy.LabelApp] != "x" {
		t.Errorf("Labels[app] = %q, want x (management label must win)", b.Labels[deploy.LabelApp])
	}
	if b.Labels[deploy.LabelManaged] != "true" {
		t.Errorf("Labels[managed] = %q, want true", b.Labels[deploy.LabelManaged])
	}

	// A spec with none of these set leaves the fields at their zero/nil default.
	plain := toCreateBody(api.DeploySpec{Name: "x", Image: "img"})
	if plain.StopTimeout != nil || plain.HostConfig.Init != nil {
		t.Errorf("plain spec: StopTimeout=%v Init=%v, want nil/nil", plain.StopTimeout, plain.HostConfig.Init)
	}
	if plain.Tty || plain.OpenStdin || plain.HostConfig.ReadonlyRootfs {
		t.Error("plain spec: tty/stdin/readonly should default false")
	}
}

// TestToCreateBodyDeployKeys asserts the deploy.* batch maps onto the Docker
// create body: restart_policy condition -> RestartPolicy.Name, max_attempts ->
// MaximumRetryCount, and reservations.memory -> MemoryReservation (a CPU
// reservation has no Docker field and is dropped).
func TestToCreateBodyDeployKeys(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:               "x",
		Image:              "img",
		Restart:            "on-failure",
		RestartMaxAttempts: 5,
		Resources: &api.Resources{
			MemoryLimit:    512 * 1024 * 1024,
			ReservedMemory: 128 * 1024 * 1024,
			ReservedCPU:    0.5, // no Docker field: dropped
		},
	})
	if b.HostConfig.RestartPolicy.Name != "on-failure" {
		t.Errorf("RestartPolicy.Name = %q, want on-failure", b.HostConfig.RestartPolicy.Name)
	}
	if b.HostConfig.RestartPolicy.MaximumRetryCount != 5 {
		t.Errorf("MaximumRetryCount = %d, want 5", b.HostConfig.RestartPolicy.MaximumRetryCount)
	}
	if b.HostConfig.Memory != 512*1024*1024 {
		t.Errorf("Memory = %d, want 512Mi", b.HostConfig.Memory)
	}
	if b.HostConfig.MemoryReservation != 128*1024*1024 {
		t.Errorf("MemoryReservation = %d, want 128Mi", b.HostConfig.MemoryReservation)
	}

	// A plain spec leaves the deploy-derived fields at their zero default.
	plain := toCreateBody(api.DeploySpec{Name: "x", Image: "img"})
	if plain.HostConfig.RestartPolicy.MaximumRetryCount != 0 || plain.HostConfig.MemoryReservation != 0 {
		t.Errorf("plain spec: retry=%d reservation=%d, want 0/0", plain.HostConfig.RestartPolicy.MaximumRetryCount, plain.HostConfig.MemoryReservation)
	}
}

func TestSplitRefTag(t *testing.T) {
	cases := map[string][2]string{
		"alpine":                {"alpine", "latest"},
		"alpine:3.20":           {"alpine", "3.20"},
		"localhost:5000/app:v1": {"localhost:5000/app", "v1"},
		"localhost:5000/app":    {"localhost:5000/app", "latest"},
		"reg/app@sha256:abcd":   {"reg/app", "sha256:abcd"},
	}
	for ref, want := range cases {
		n, tg := splitRefTag(ref)
		if n != want[0] || tg != want[1] {
			t.Errorf("splitRefTag(%q) = (%q,%q), want (%q,%q)", ref, n, tg, want[0], want[1])
		}
	}
}

func TestToCreateBodyHealthcheck(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Healthcheck: &api.Healthcheck{
			Test:          []string{"CMD", "curl", "-f", "http://localhost"},
			Interval:      "30s",
			Timeout:       "5s",
			StartPeriod:   "1m",
			StartInterval: "5s",
			Retries:       4,
		},
	})
	hc := b.Healthcheck
	if hc == nil {
		t.Fatal("expected Healthcheck")
	}
	if len(hc.Test) != 4 || hc.Test[0] != "CMD" {
		t.Fatalf("test = %v", hc.Test)
	}
	if hc.Interval != int64(30*time.Second) || hc.Timeout != int64(5*time.Second) || hc.StartPeriod != int64(time.Minute) {
		t.Fatalf("durations = %+v", hc)
	}
	if hc.StartInterval != int64(5*time.Second) {
		t.Fatalf("start interval = %d, want %d", hc.StartInterval, int64(5*time.Second))
	}
	if hc.Retries != 4 {
		t.Fatalf("retries = %d", hc.Retries)
	}
}

func TestToCreateBodyResources(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:      "x",
		Image:     "img",
		Resources: &api.Resources{CPULimit: 0.5, MemoryLimit: 512 * 1024 * 1024},
	})
	if b.HostConfig.NanoCpus != 500_000_000 {
		t.Fatalf("NanoCpus = %d", b.HostConfig.NanoCpus)
	}
	if b.HostConfig.Memory != 512*1024*1024 {
		t.Fatalf("Memory = %d", b.HostConfig.Memory)
	}
	// No resources => no limits set.
	plain := toCreateBody(api.DeploySpec{Name: "x", Image: "img"})
	if plain.HostConfig.NanoCpus != 0 || plain.HostConfig.Memory != 0 || plain.Healthcheck != nil {
		t.Fatalf("unexpected limits on plain spec: %+v", plain.HostConfig)
	}
}

// TestToCreateBodyResourceKeys asserts the resource & host-namespace batch
// (ulimits, tmpfs, devices, shm_size, pid, ipc) maps onto the Docker HostConfig.
func TestToCreateBodyResourceKeys(t *testing.T) {
	b := toCreateBody(api.DeploySpec{
		Name:  "x",
		Image: "img",
		Ulimits: []api.Ulimit{
			{Name: "nofile", Soft: 20000, Hard: 40000},
			{Name: "nproc", Soft: 65535, Hard: 65535},
		},
		Tmpfs:   []string{"/run", "/tmp:size=64m"},
		Devices: []string{"/dev/ttyUSB0:/dev/ttyUSB0:rwm", "/dev/null"},
		ShmSize: 128 * 1024 * 1024,
		PIDMode: "host",
		IPCMode: "shareable",
	})
	hc := b.HostConfig
	wantUlimits := []ulimit{
		{Name: "nofile", Soft: 20000, Hard: 40000},
		{Name: "nproc", Soft: 65535, Hard: 65535},
	}
	if !reflect.DeepEqual(hc.Ulimits, wantUlimits) {
		t.Errorf("Ulimits = %+v, want %+v", hc.Ulimits, wantUlimits)
	}
	// tmpfs: path -> options, split on the first colon.
	if hc.Tmpfs["/run"] != "" || hc.Tmpfs["/tmp"] != "size=64m" {
		t.Errorf("Tmpfs = %v", hc.Tmpfs)
	}
	wantDevices := []deviceMapping{
		{PathOnHost: "/dev/ttyUSB0", PathInContainer: "/dev/ttyUSB0", CgroupPermissions: "rwm"},
		{PathOnHost: "/dev/null", PathInContainer: "/dev/null", CgroupPermissions: "rwm"},
	}
	if !reflect.DeepEqual(hc.Devices, wantDevices) {
		t.Errorf("Devices = %+v, want %+v", hc.Devices, wantDevices)
	}
	if hc.ShmSize != 128*1024*1024 {
		t.Errorf("ShmSize = %d", hc.ShmSize)
	}
	if hc.PidMode != "host" {
		t.Errorf("PidMode = %q", hc.PidMode)
	}
	if hc.IpcMode != "shareable" {
		t.Errorf("IpcMode = %q", hc.IpcMode)
	}
	// A plain spec sets none of these.
	plain := toCreateBody(api.DeploySpec{Name: "x", Image: "img"}).HostConfig
	if plain.Ulimits != nil || plain.Tmpfs != nil || plain.Devices != nil ||
		plain.ShmSize != 0 || plain.PidMode != "" || plain.IpcMode != "" {
		t.Errorf("unexpected resource keys on plain spec: %+v", plain)
	}
}

// TestLifecycleMissingDeployment asserts the cross-backend contract: Stop,
// Start, and Restart of a name with no instances error wrapping
// deploy.ErrNotFound (no more silent nil), while Delete stays delete-if-exists.
func TestLifecycleMissingDeployment(t *testing.T) {
	b := newTestBackend(t, &fakeDocker{})
	ctx := context.Background()
	for verb, fn := range map[string]func(context.Context, string) error{
		"Stop":    b.Stop,
		"Start":   b.Start,
		"Restart": b.Restart,
	} {
		if err := fn(ctx, "ghost"); !errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("%s(ghost) = %v, want error wrapping deploy.ErrNotFound", verb, err)
		}
	}
	if err := b.Delete(ctx, "ghost"); err != nil {
		t.Errorf("Delete(ghost) = %v, want nil (delete-if-exists)", err)
	}
}

// TestLogsRejectsInvalidSince asserts a garbage since value is rejected by the
// shared parser before anything reaches the daemon.
func TestLogsRejectsInvalidSince(t *testing.T) {
	b := newTestBackend(t, &fakeDocker{})
	err := b.Logs(context.Background(), "web", api.LogOptions{Since: "yesterdayish"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid since") {
		t.Fatalf("Logs with garbage since = %v, want invalid-since error", err)
	}
}
