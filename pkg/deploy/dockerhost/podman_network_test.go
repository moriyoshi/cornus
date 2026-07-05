package dockerhost

// dns_enabled is the trap this file exists for.
//
// libpod's network create takes the body VERBATIM and does not default
// dns_enabled. Every other route to a podman network sets it — the CLI's
// --disable-dns defaults off, and the Docker-compat endpoint forces it on for
// bridge networks — so it is easy to believe it is on. Measured against Podman
// 5.8.2, a network created through libpod REST without it resolves peer
// container names as NXDOMAIN, while an identical one with it resolves them.
//
// The consequence for cornus is total and silent: compose user networks depend
// entirely on container-name resolution, so a service that cannot find its
// database looks like an application bug, on a network whose creation returned
// success and whose inspect looks correct.
//
// The end-to-end proof lives in an E2E scenario that performs an actual lookup —
// a unit test cannot resolve DNS. What is pinned HERE is the wire contract: the
// key is present and true on the request. That is a real check because the
// failure is an omitted field, and the test reads the decoded body rather than
// asserting the call succeeded (which it does either way).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// capturingLibpod records the decoded body of the last non-ping request.
type capturingLibpod struct {
	srv    *httptest.Server
	body   map[string]any
	path   string
	method string
	status int
}

func newCapturingLibpod(t *testing.T) *capturingLibpod {
	t.Helper()
	c := &capturingLibpod{status: http.StatusOK}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		c.path, c.method = r.URL.Path, r.Method
		if b, err := io.ReadAll(r.Body); err == nil && len(b) > 0 {
			c.body = map[string]any{}
			_ = json.Unmarshal(b, &c.body)
		}
		w.WriteHeader(c.status)
		io.WriteString(w, "{}")
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *capturingLibpod) engine(t *testing.T) *podmanEngine {
	t.Helper()
	e, err := newPodmanEngine(context.Background(), endpointFor(t, c.srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	return e
}

func TestPodmanNetworkCreateEnablesDNS(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)

	if err := e.networkEnsure(context.Background(), api.NetworkAttachment{Name: "appnet"}); err != nil {
		t.Fatalf("networkEnsure: %v", err)
	}

	v, present := c.body["dns_enabled"]
	if !present {
		t.Fatalf("network create body omits dns_enabled entirely; libpod does not default it, "+
			"so the network would be created with NO container-name resolution and every compose "+
			"service lookup would NXDOMAIN.\nbody: %v", c.body)
	}
	if v != true {
		t.Errorf("dns_enabled = %v, want true", v)
	}
}

// TestPodmanNetworkCreateUsesSnakeCase guards the casing, which is genuinely
// inconsistent across libpod: networks are snake_case while volumes and inspect
// are PascalCase. A PascalCase key here is silently ignored by the decoder,
// producing a network with none of the requested settings.
func TestPodmanNetworkCreateUsesSnakeCase(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)

	err := e.networkEnsure(context.Background(), api.NetworkAttachment{
		Name:     "appnet",
		Driver:   "bridge",
		Internal: true,
		Subnet:   "10.89.7.0/24",
		Gateway:  "10.89.7.1",
		Labels:   map[string]string{"cornus.managed": "true"},
	})
	if err != nil {
		t.Fatalf("networkEnsure: %v", err)
	}
	for _, key := range []string{"name", "driver", "internal", "dns_enabled", "labels", "subnets"} {
		if _, ok := c.body[key]; !ok {
			t.Errorf("network create body missing snake_case key %q; body: %v", key, c.body)
		}
	}
	subnets, _ := c.body["subnets"].([]any)
	if len(subnets) != 1 {
		t.Fatalf("subnets = %v, want one entry", c.body["subnets"])
	}
	s, _ := subnets[0].(map[string]any)
	if s["subnet"] != "10.89.7.0/24" || s["gateway"] != "10.89.7.1" {
		t.Errorf("subnet entry = %v, want subnet/gateway carried through", s)
	}
}

// TestPodmanNetworkCreateStripsPseudoDrivers: the kubernetes pseudo-drivers are
// not real network drivers, and libpod rejects an unknown driver outright.
func TestPodmanNetworkCreateStripsPseudoDrivers(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)
	if err := e.networkEnsure(context.Background(), api.NetworkAttachment{Name: "n", Driver: "cilium"}); err != nil {
		t.Fatalf("networkEnsure: %v", err)
	}
	if d, ok := c.body["driver"]; ok {
		t.Errorf("driver = %v, want it stripped so libpod picks its default", d)
	}
}

// TestPodmanNetworkConnectSwallowsAlreadyConnected pins the 500-with-a-cause
// case. libpod returns 500 here where compat returns 403 and real Docker 409, so
// the status alone cannot distinguish it from a genuine internal failure — the
// short `cause` field is the only discriminator.
func TestPodmanNetworkConnectSwallowsAlreadyConnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"cause":"network is already connected",`+
			`"message":"container abc is already connected to network appnet: network is already connected",`+
			`"response":500}`)
	}))
	t.Cleanup(srv.Close)
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	if err := e.networkJoin(context.Background(), "appnet", "abc"); err != nil {
		t.Errorf("networkJoin returned %v for an already-connected container; "+
			"converging to a state that already holds is not a failure", err)
	}
}

// ...but a genuine 500 must still surface, or every real network failure is
// swallowed along with it.
func TestPodmanNetworkConnectSurfacesRealFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"cause":"no such network","message":"network appnet not found","response":500}`)
	}))
	t.Cleanup(srv.Close)
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	if err := e.networkJoin(context.Background(), "appnet", "abc"); err == nil {
		t.Error("networkJoin swallowed a genuine 500; only the already-connected cause may be ignored")
	}
}

// TestPodmanNetworkDisconnectUsesDockerCasing: connect takes snake_case while
// disconnect takes Docker's PascalCase, because that route is wired to the
// compat handler. Getting it wrong means the container is never detached and the
// body is silently ignored.
func TestPodmanNetworkDisconnectUsesDockerCasing(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)
	if err := e.networkLeave(context.Background(), "appnet", "abc"); err != nil {
		t.Fatalf("networkLeave: %v", err)
	}
	if _, ok := c.body["Container"]; !ok {
		t.Errorf("disconnect body uses %v, want Docker-cased \"Container\" — "+
			"libpod's disconnect route decodes Docker's DisconnectOptions", c.body)
	}
}

// TestPodmanNetworkConnectUsesLibpodCasing is the other half of the asymmetry.
func TestPodmanNetworkConnectUsesLibpodCasing(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)
	if err := e.networkJoin(context.Background(), "appnet", "abc"); err != nil {
		t.Fatalf("networkJoin: %v", err)
	}
	if _, ok := c.body["container"]; !ok {
		t.Errorf("connect body uses %v, want libpod-cased \"container\"", c.body)
	}
}

// A duplicate network create must be a no-op, not an error — on EVERY podman.
//
// networkEnsure asks for idempotency with ignoreIfExists, but that parameter
// arrived in libpod 5.0. An older server does not reject an unknown query key;
// it ignores it and answers the duplicate with 409 Conflict. Measured on podman
// 4.3.1: every compose re-deploy onto an existing user network failed with
// "network name X already used", which surfaces as a failed DEPLOY of a service
// whose network was already exactly right.
//
// The status is forced here rather than driven by a real duplicate because the
// fake has no network store — what is under test is how the engine reads the
// answer, and 409 is the only answer that distinguishes the two behaviors.
func TestPodmanNetworkCreateTreatsConflictAsAlreadyExisting(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)
	c.status = http.StatusConflict

	if err := e.networkEnsure(context.Background(), api.NetworkAttachment{Name: "appnet"}); err != nil {
		t.Fatalf("networkEnsure on an existing network returned %v, want nil: a network that "+
			"already exists is the state this call exists to establish, so a 409 is success "+
			"reached the other way. Treating it as an error makes idempotency depend on which "+
			"podman is behind the socket (ignoreIfExists is 5.0+).", err)
	}
}

// ...but a genuine failure must still be one. The conflict arm is narrow on
// purpose: if it widened to "any non-200 is fine", a network that could not be
// created at all would report success and the deploy would fail later, somewhere
// else, with the cause gone.
func TestPodmanNetworkCreateStillFailsOnRealErrors(t *testing.T) {
	c := newCapturingLibpod(t)
	e := c.engine(t)
	c.status = http.StatusInternalServerError

	if err := e.networkEnsure(context.Background(), api.NetworkAttachment{Name: "appnet"}); err == nil {
		t.Fatal("networkEnsure returned nil for a 500; a network that could not be created must " +
			"fail here, while the cause is still in hand")
	}
}

// --- reusing a pre-existing DNS-less network --------------------------------
//
// The create above always asks for dns_enabled, but ignoreIfExists (and the 409
// that stands in for it on podman 4.x) means the network cornus deploys onto may
// not be the one it asked for. A network created once WITHOUT DNS keeps serving
// every future deploy with no name resolution, and the failure is the same silent
// one the dns_enabled work exists to prevent: the network is present, inspect
// looks plausible, the deploy succeeds, and only service-name lookups fail.
//
// Found 2026-08-06 while neutralizing compose-dns-resolution.star — the run with
// the broken cornus left the network behind and the FIXED cornus reused it and
// failed identically, so the fix read as no fix at all.
//
// What is pinned here is that networkEnsure SAYS SO. These tests assert on the
// emitted log rather than on a return value on purpose: a warning is the whole
// behaviour, and it must not fail the deploy.

// libpodWithNetwork serves a create (at createStatus) plus a network inspect
// returning the given body, so a test can describe a network that already exists.
func libpodWithNetwork(t *testing.T, createStatus int, inspectBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == libpodPingPath:
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, inspectBody)
		default:
			w.WriteHeader(createStatus)
			io.WriteString(w, "{}")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ensureAgainst(t *testing.T, srv *httptest.Server) {
	t.Helper()
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	// A network that is serving traffic must not be refused because of what the
	// inspect said; the report is the only consequence.
	if err := e.networkEnsure(context.Background(), api.NetworkAttachment{Name: "appnet"}); err != nil {
		t.Fatalf("networkEnsure: %v", err)
	}
}

func TestPodmanNetworkEnsureReportsAReusedDNSLessNetwork(t *testing.T) {
	// Both spellings of "it already existed": ignoreIfExists answering 200 on
	// podman 5.x, and the 409 an older libpod returns instead. The 409 path
	// returns EARLY, so a check placed only after expect() would cover neither
	// podman 4.x nor the version cornus most often meets a stale network on.
	for _, tc := range []struct {
		name         string
		createStatus int
	}{
		{"ignoreIfExists 200", http.StatusOK},
		{"podman 4.x 409", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			ensureAgainst(t, libpodWithNetwork(t, tc.createStatus,
				`{"name":"appnet","driver":"bridge","dns_enabled":false}`))
			got := buf.String()
			if !strings.Contains(got, "DNS disabled") {
				t.Fatalf("deploying onto a pre-existing network with dns_enabled=false was SILENT.\n"+
					"Container-name resolution does not work on it, so every compose service lookup "+
					"NXDOMAINs while the deploy reports success.\nlog: %q", got)
			}
			if !strings.Contains(got, "appnet") {
				t.Errorf("the warning does not name the network, so an operator cannot act on it: %q", got)
			}
		})
	}
}

// The other three answers must stay silent, or the warning becomes noise an
// operator learns to skip past — which costs exactly the case above.
func TestPodmanNetworkEnsureStaysSilentWhenDNSIsFineOrUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inspect string
		why     string
	}{
		{
			"dns enabled",
			`{"name":"appnet","driver":"bridge","dns_enabled":true}`,
			"the network resolves names — this is the ordinary case, on every deploy",
		},
		{
			"macvlan",
			`{"name":"appnet","driver":"macvlan","dns_enabled":false}`,
			"macvlan carries no aardvark-dns BY CONSTRUCTION, so its lack of DNS is not a defect",
		},
		{
			"field absent",
			`{"name":"appnet","driver":"bridge"}`,
			"a libpod that does not report dns_enabled says nothing about the network's state",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			ensureAgainst(t, libpodWithNetwork(t, http.StatusOK, tc.inspect))
			if got := buf.String(); strings.Contains(got, "DNS disabled") {
				t.Errorf("warned anyway: %s\nlog: %q", tc.why, got)
			}
		})
	}
}

// TestBothEnginesDecodeEveryInspectField is the guard on a drift that already
// happened once, silently.
//
// There are TWO containerInspect implementations — the Docker engine's and
// podmanEngine's — filling the SAME containerInspectResult from two different
// JSON shapes. When the Docker one gained State.Pid for credential endpoint
// delivery, the podman one did not, and nothing complained: both still compiled,
// both still decoded, and the zero Pid simply read downstream as "not running
// yet". The endpoint retried forever on a container that was running, and the
// workload's credential never arrived.
//
// It drives the REAL engine over a fake libpod endpoint rather than repeating the
// field mapping here. A first version of this test rebuilt the result inline and
// passed with the fix neutralized — it was testing its own copy of the code.
func TestBothEnginesDecodeEveryInspectField(t *testing.T) {
	const state = `{"Running":true,"ExitCode":7,"Pid":4242,"Health":{"Status":"healthy"}}`
	want := containerInspectResult{Running: true, ExitCode: 7, Pid: 4242, Health: "healthy"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"State":`+state+`}`)
	}))
	t.Cleanup(srv.Close)
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	got, err := e.containerInspect(context.Background(), "abc")
	if err != nil {
		t.Fatalf("containerInspect: %v", err)
	}
	if got != want {
		t.Fatalf("podman containerInspect returned %+v, want %+v — a field the docker "+
			"engine populates is missing here, and its zero value reads downstream as a "+
			"legitimate state rather than as absent", got, want)
	}

	// Reflection over the result type, so a field added later is caught even if
	// nobody thinks to extend the fixture above.
	rt := reflect.TypeOf(containerInspectResult{})
	v := reflect.ValueOf(want)
	for i := 0; i < rt.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("containerInspectResult.%s is not covered by this test; give it a "+
				"non-zero value in the fixture and make sure BOTH engines populate it", rt.Field(i).Name)
		}
	}
}
