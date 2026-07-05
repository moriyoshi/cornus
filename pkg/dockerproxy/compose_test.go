package dockerproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestComposeSequence drives the Docker API calls `docker compose up -d`/`ps`/
// `down` make against the proxy: create a project network, create+start two
// labeled services, list by project label, then tear the project down
// (stop+rm each container, delete the network). It asserts the proxy accepts
// the whole sequence and the attacher deployed both services.
func TestComposeSequence(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	proj := "myproj"

	// up: create the default network.
	nb, _ := json.Marshal(map[string]string{"Name": proj + "_default", "Driver": "bridge"})
	resp := do(t, http.MethodPost, srv.URL+"/networks/create", nb)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("network create = %d", resp.StatusCode)
	}
	var nc struct{ Id string }
	_ = json.NewDecoder(resp.Body).Decode(&nc)
	resp.Body.Close()
	if nc.Id == "" {
		t.Fatal("no network id")
	}
	// The network is discoverable by name filter (compose re-checks).
	resp = do(t, http.MethodGet, srv.URL+`/networks?filters={"name":{"`+proj+`_default":true}}`, nil)
	var nets []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&nets)
	resp.Body.Close()
	if len(nets) != 1 {
		t.Fatalf("network list = %+v", nets)
	}

	// up: create + start each service.
	svc := func(name string) {
		labels := map[string]string{"com.docker.compose.project": proj, "com.docker.compose.service": name}
		b, _ := json.Marshal(createRequest{Image: "img:" + name, Labels: labels})
		resp := do(t, http.MethodPost, srv.URL+"/containers/create?name="+proj+"-"+name+"-1", b)
		var cr createResponse
		_ = json.NewDecoder(resp.Body).Decode(&cr)
		resp.Body.Close()
		if cr.ID == "" {
			t.Fatalf("create %s: no id", name)
		}
		if r := do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil); r.StatusCode != http.StatusNoContent {
			t.Fatalf("start %s = %d", name, r.StatusCode)
		}
		r := do(t, http.MethodGet, srv.URL+"/containers/"+cr.ID+"/json", nil)
		r.Body.Close()
	}
	svc("web")
	svc("db")

	if fa.specFor("myproj-web-1") == nil || fa.specFor("myproj-db-1") == nil {
		t.Fatalf("both services should be deployed; attached=%d", len(fa.attached))
	}

	// ps: list by project label.
	pf, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + proj}})
	resp = do(t, http.MethodGet, srv.URL+"/containers/json?all=1&filters="+string(pf), nil)
	var list []containerSummary
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 2 {
		t.Fatalf("compose ps = %d containers, want 2", len(list))
	}
	for _, c := range list {
		if c.Labels["com.docker.compose.project"] != proj {
			t.Errorf("ps container missing project label: %+v", c)
		}
	}

	// down: stop + remove each, then delete the network.
	for _, c := range list {
		if r := do(t, http.MethodPost, srv.URL+"/containers/"+c.ID+"/stop", nil); r.StatusCode != http.StatusNoContent {
			t.Fatalf("stop = %d", r.StatusCode)
		}
		if r := do(t, http.MethodDelete, srv.URL+"/containers/"+c.ID, nil); r.StatusCode != http.StatusNoContent {
			t.Fatalf("rm = %d", r.StatusCode)
		}
	}
	if r := do(t, http.MethodDelete, srv.URL+"/networks/"+nc.Id, nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("network rm = %d", r.StatusCode)
	}

	// Project is gone.
	resp = do(t, http.MethodGet, srv.URL+"/containers/json?all=1&filters="+string(pf), nil)
	list = nil
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 0 {
		t.Fatalf("after down, ps = %d, want 0", len(list))
	}
}

// TestComposeScaleAndRecreateDiff covers the two mechanics compose relies on for
// `--scale N` and for its recreate-vs-keep decision, both of which the proxy
// satisfies without any special server-side logic:
//
//   - Scale: `docker compose up --scale web=3` is driven client-side as three
//     independent creates named <project>-web-1/-2/-3. Each maps to its own
//     deterministic deployment, so N replicas => N deployments and a project ps
//     lists all N.
//   - Recreate diffing: compose stamps every container with a
//     `com.docker.compose.config-hash` label and, on a subsequent `up`, keeps a
//     container whose stored hash matches the freshly computed one (else it
//     stop+rm+recreates). That decision is client-side; the proxy only has to
//     round-trip the label verbatim through BOTH list (ps) and inspect, which is
//     what this asserts.
func TestComposeScaleAndRecreateDiff(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	const proj, svc = "scaleproj", "web"
	const hash = "sha256:deadbeefcafef00d"

	// up --scale web=3: three creates, one per replica, as the compose plugin issues them.
	ids := make([]string, 0, 3)
	for i := 1; i <= 3; i++ {
		num := strconv.Itoa(i)
		labels := map[string]string{
			"com.docker.compose.project":          proj,
			"com.docker.compose.service":          svc,
			"com.docker.compose.container-number": num,
			"com.docker.compose.config-hash":      hash,
		}
		b, _ := json.Marshal(createRequest{Image: "img:web", Labels: labels})
		resp := do(t, http.MethodPost, srv.URL+"/containers/create?name="+proj+"-"+svc+"-"+num, b)
		var cr createResponse
		_ = json.NewDecoder(resp.Body).Decode(&cr)
		resp.Body.Close()
		if cr.ID == "" {
			t.Fatalf("create replica %s: no id", num)
		}
		if r := do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil); r.StatusCode != http.StatusNoContent {
			t.Fatalf("start replica %s = %d", num, r.StatusCode)
		}
		ids = append(ids, cr.ID)
	}

	// Each replica became its own deployment.
	for i := 1; i <= 3; i++ {
		dep := proj + "-" + svc + "-" + strconv.Itoa(i)
		if fa.specFor(dep) == nil {
			t.Errorf("replica %d not deployed as %q; attached=%d", i, dep, len(fa.attached))
		}
	}

	// ps by project label lists all three replicas, and the config-hash the
	// compose differ reads round-trips through the list response.
	pf, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + proj}})
	resp := do(t, http.MethodGet, srv.URL+"/containers/json?all=1&filters="+string(pf), nil)
	var list []containerSummary
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 3 {
		t.Fatalf("compose ps = %d containers, want 3", len(list))
	}
	seen := map[string]bool{}
	for _, c := range list {
		if c.Labels["com.docker.compose.config-hash"] != hash {
			t.Errorf("ps replica %s: config-hash = %q, want %q (compose would spuriously recreate)", c.ID, c.Labels["com.docker.compose.config-hash"], hash)
		}
		seen[c.Labels["com.docker.compose.container-number"]] = true
	}
	if !seen["1"] || !seen["2"] || !seen["3"] {
		t.Errorf("ps did not surface all three container-numbers: %v", seen)
	}

	// ...and through inspect, the other place the differ may read it.
	r := do(t, http.MethodGet, srv.URL+"/containers/"+ids[0]+"/json", nil)
	var cj containerJSON
	_ = json.NewDecoder(r.Body).Decode(&cj)
	r.Body.Close()
	if cj.Config.Labels["com.docker.compose.config-hash"] != hash {
		t.Errorf("inspect config-hash = %q, want %q", cj.Config.Labels["com.docker.compose.config-hash"], hash)
	}
}

// TestNetworkCreateEchoesLabels covers the first compose-reconverge gap: a
// second `up` inspects the project network and refuses to reuse it unless the
// com.docker.compose.* labels it set at create echo back ("incorrect label
// com.docker.compose.network set to the empty string"). Create with labels
// must store them,
// both inspect and list must return them, and a duplicate create must NOT
// merge new labels (docker semantics: labels are create-time-only).
func TestNetworkCreateEchoesLabels(t *testing.T) {
	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()

	labels := map[string]string{
		"com.docker.compose.network": "default",
		"com.docker.compose.project": "dscale",
	}
	nb, _ := json.Marshal(map[string]any{"Name": "dscale_default", "Driver": "bridge", "Labels": labels})
	resp := do(t, http.MethodPost, srv.URL+"/networks/create", nb)
	var nc struct{ Id string }
	_ = json.NewDecoder(resp.Body).Decode(&nc)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || nc.Id == "" {
		t.Fatalf("network create = %d, id = %q", resp.StatusCode, nc.Id)
	}

	assertLabels := func(got map[string]any, where string) {
		t.Helper()
		lbl, _ := got["Labels"].(map[string]any)
		for k, v := range labels {
			if lbl[k] != v {
				t.Errorf("%s: Labels[%q] = %v, want %q", where, k, lbl[k], v)
			}
		}
	}

	// Inspect echoes the create-time labels.
	resp = do(t, http.MethodGet, srv.URL+"/networks/"+nc.Id, nil)
	var ins map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&ins)
	resp.Body.Close()
	assertLabels(ins, "inspect")

	// List (name-filtered, as compose issues it) echoes them too.
	resp = do(t, http.MethodGet, srv.URL+`/networks?filters={"name":{"dscale_default":true}}`, nil)
	var nets []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&nets)
	resp.Body.Close()
	if len(nets) != 1 {
		t.Fatalf("network list = %+v, want 1 entry", nets)
	}
	assertLabels(nets[0], "list")

	// A duplicate create with different labels reuses the network and keeps the
	// original labels.
	nb2, _ := json.Marshal(map[string]any{"Name": "dscale_default", "Labels": map[string]string{"other": "x"}})
	resp = do(t, http.MethodPost, srv.URL+"/networks/create", nb2)
	var nc2 struct{ Id string }
	_ = json.NewDecoder(resp.Body).Decode(&nc2)
	resp.Body.Close()
	if nc2.Id != nc.Id {
		t.Fatalf("duplicate create id = %q, want reuse of %q", nc2.Id, nc.Id)
	}
	resp = do(t, http.MethodGet, srv.URL+"/networks/"+nc.Id, nil)
	ins = nil
	_ = json.NewDecoder(resp.Body).Decode(&ins)
	resp.Body.Close()
	assertLabels(ins, "inspect after duplicate create")
	if lbl, _ := ins["Labels"].(map[string]any); lbl["other"] != nil {
		t.Errorf("duplicate create merged labels: %v", lbl)
	}
}

// TestContainerListNetworkSettings covers the second compose-reconverge gap:
// compose v5's convergence (checkExpectedNetworks) dereferences
// NetworkSettings.Networks on the container LIST response and reads its keys
// as the container's network membership, so /containers/json must carry the
// Networks map keyed by network name with a non-empty NetworkID.
func TestContainerListNetworkSettings(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	// The project network exists, so the endpoint can carry its real id.
	nb, _ := json.Marshal(map[string]any{"Name": "dscale_default"})
	resp := do(t, http.MethodPost, srv.URL+"/networks/create", nb)
	var nc struct{ Id string }
	_ = json.NewDecoder(resp.Body).Decode(&nc)
	resp.Body.Close()

	b, _ := json.Marshal(createRequest{
		Image: "img:web",
		NetworkingConfig: networkingConfig{
			EndpointsConfig: map[string]endpointConfig{"dscale_default": {Aliases: []string{"web"}}},
		},
	})
	resp = do(t, http.MethodPost, srv.URL+"/containers/create?name=dscale-web-1", b)
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()

	resp = do(t, http.MethodGet, srv.URL+"/containers/json?all=1", nil)
	var list []containerSummary
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 {
		t.Fatalf("ps = %d containers, want 1", len(list))
	}
	ns := list[0].NetworkSettings
	if ns == nil {
		t.Fatal("list NetworkSettings is nil (compose convergence nil-derefs it)")
	}
	nets, _ := ns["Networks"].(map[string]any)
	ep, ok := nets["dscale_default"].(map[string]any)
	if !ok {
		t.Fatalf("list NetworkSettings.Networks = %v, want key dscale_default", nets)
	}
	if ep["NetworkID"] != nc.Id {
		t.Errorf("list endpoint NetworkID = %v, want %q", ep["NetworkID"], nc.Id)
	}
}

// TestVolumeAndEventsStubs confirms volume create/list and that /events opens
// (then is closed by the client) without error.
func TestVolumeAndEventsStubs(t *testing.T) {
	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()

	vb, _ := json.Marshal(map[string]string{"Name": "data"})
	if r := do(t, http.MethodPost, srv.URL+"/volumes/create", vb); r.StatusCode != http.StatusCreated {
		t.Fatalf("volume create = %d", r.StatusCode)
	}
	resp := do(t, http.MethodGet, srv.URL+"/volumes", nil)
	var vl struct{ Volumes []map[string]any }
	_ = json.NewDecoder(resp.Body).Decode(&vl)
	resp.Body.Close()
	if len(vl.Volumes) != 1 {
		t.Fatalf("volume list = %+v", vl)
	}

	// /events returns 200 and holds open; the client cancels via its context.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
}
