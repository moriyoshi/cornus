package dockerhost

// The stats remap is the one place the libpod engine translates rather than
// forwards, and the failure it prevents is invisible.
//
// libpod emits the container id as "Id"; real Docker and cornus's own
// hostrun.DockerStats use "id" (both verified — libpod against Podman 5.8.2, the
// Docker side against Docker 29.2.1). Forwarding verbatim produces a frame that
// parses cleanly, has every other field right, and carries an EMPTY id. Nothing
// errors; a `docker stats`-shaped consumer just shows a blank column.
//
// So this test decodes the emitted bytes and reads the value. A test that only
// checked "the stream produced output" would pass against the bug.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cornus/pkg/deploy/internal/hostrun"
)

// libpodStatsBody is a real libpod stats object, trimmed. Note "Id".
const libpodStatsBody = `{"read":"2026-08-05T23:29:16.175100667Z","preread":"0001-01-01T00:00:00Z",` +
	`"pids_stats":{"current":1},"num_procs":0,` +
	`"cpu_stats":{"cpu_usage":{"total_usage":2043812000,"usage_in_kernelmode":4999,` +
	`"usage_in_usermode":2043807001},"system_cpu_usage":2555387614047000,"online_cpus":20},` +
	`"precpu_stats":{"cpu_usage":{"total_usage":0}},` +
	`"memory_stats":{"usage":1769472,"limit":130660868096},` +
	`"name":"statstest","Id":"458b2695dcf4"}`

func podmanStatsServer(t *testing.T, body string) *podmanEngine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == libpodPingPath {
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	e, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	return e
}

func TestPodmanStatsRemapsIdToDockerCasing(t *testing.T) {
	e := podmanStatsServer(t, libpodStatsBody)

	rc, err := e.containerStats(context.Background(), "abc", false)
	if err != nil {
		t.Fatalf("containerStats: %v", err)
	}
	defer rc.Close()
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}

	// Decode through the type cornus's own consumers use, which is the whole
	// point: it reads `json:"id"`.
	var ds hostrun.DockerStats
	if err := json.Unmarshal(out, &ds); err != nil {
		t.Fatalf("decode emitted stats as hostrun.DockerStats: %v\nbody: %s", err, out)
	}
	if ds.ID != "458b2695dcf4" {
		t.Errorf("hostrun.DockerStats.ID = %q, want %q — libpod's \"Id\" was not remapped to \"id\", "+
			"so every consumer sees a blank container id\nemitted: %s", ds.ID, "458b2695dcf4", out)
	}
	// The capitalised key must be gone, not merely duplicated: a frame carrying
	// both is not what Docker emits and invites a consumer to pick the wrong one.
	if strings.Contains(string(out), `"Id"`) {
		t.Errorf("emitted stats still carry the libpod-cased \"Id\" key: %s", out)
	}
	// And the remap must not have disturbed the numbers around it.
	if ds.CPU.Usage.Total != 2043812000 {
		t.Errorf("cpu total_usage = %d, want 2043812000 (the remap corrupted neighbouring fields)",
			ds.CPU.Usage.Total)
	}
}

// TestPodmanStatsLeavesDockerShapedFramesAlone: if a future podman emits "id"
// directly, the remap must be a no-op rather than dropping the field.
func TestPodmanStatsLeavesDockerShapedFramesAlone(t *testing.T) {
	e := podmanStatsServer(t, `{"name":"x","id":"already-lowercase"}`)
	rc, err := e.containerStats(context.Background(), "abc", false)
	if err != nil {
		t.Fatalf("containerStats: %v", err)
	}
	defer rc.Close()
	out, _ := io.ReadAll(rc)
	var ds hostrun.DockerStats
	if err := json.Unmarshal(out, &ds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ds.ID != "already-lowercase" {
		t.Errorf("ID = %q, want %q — an already-Docker-shaped frame was disturbed", ds.ID, "already-lowercase")
	}
}

func TestNormalizePodmanStatsIsInPlaceAndTotal(t *testing.T) {
	raw := map[string]json.RawMessage{"Id": json.RawMessage(`"abc"`)}
	normalizePodmanStats(raw)
	if _, stillCapital := raw["Id"]; stillCapital {
		t.Error(`"Id" survived the remap`)
	}
	if got := string(raw["id"]); got != `"abc"` {
		t.Errorf(`raw["id"] = %s, want "abc"`, got)
	}
}
