package dockerproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The defect these three tests pin: `Tty` was absent from createRequest, so
// `docker run -t` was dropped at JSON decode and every container the proxy ever
// created was non-TTY. api.DeploySpec.TTY existed the whole time and every
// backend maps it (dockerhost Config.Tty, kubernetes container.TTY, containerd
// OCI), so the value simply never reached them.
//
// Two things break, and they are tested separately because they fail
// independently. The container itself gets no pseudo-TTY, so a program that
// checks isatty behaves as if piped. And the CLI decides how to DECODE an attach
// stream — raw for a TTY, stdcopy-multiplexed otherwise — from the Config.Tty it
// reads back out of inspect, so a proxy that creates a TTY container but reports
// Tty:false would have the framing and the decoder disagree. That is why the
// inspect round-trip is asserted and not just the translation.

// TestCreateRequestDecodesTty pins the wire field name. The whole defect was a
// missing struct field, so a test that constructs createRequest in Go can never
// catch it — only decoding Docker's actual JSON can.
func TestCreateRequestDecodesTty(t *testing.T) {
	var req createRequest
	body := `{"Image":"alpine:3.20","Tty":true,"Cmd":["sh"],"HostConfig":{}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal create request: %v", err)
	}
	if !req.Tty {
		t.Fatalf("Tty = false, want true (decoded from %s)", body)
	}
	if spec := toDeploySpec("app", req); !spec.TTY {
		t.Fatalf("spec.TTY = false: `docker run -t` reached the backend as a non-TTY container")
	}
}

func TestToDeploySpecTty(t *testing.T) {
	for _, tt := range []struct {
		name string
		tty  bool
	}{
		{"docker run -t asks for a pseudo-TTY", true},
		{"a plain docker run does not", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := toDeploySpec("app", createRequest{Image: "alpine:3.20", Tty: tt.tty})
			if spec.TTY != tt.tty {
				t.Fatalf("spec.TTY = %v, want %v", spec.TTY, tt.tty)
			}
		})
	}
}

// TestInspectReportsTty drives the real create -> start -> inspect path, because
// the CLI never sees toDeploySpec's output — it sees inspect. `docker attach`
// reads Config.Tty from here to pick its stream decoder, so a container created
// with a TTY that inspects as Tty:false makes the CLI apply stdcopy framing to a
// raw stream.
func TestInspectReportsTty(t *testing.T) {
	for _, tt := range []struct {
		name string
		tty  bool
	}{
		{"tty container", true},
		{"non-tty container", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAttacher{}
			srv := httptest.NewServer(New(fa).Handler())
			defer srv.Close()

			b, _ := json.Marshal(createRequest{Image: "alpine:3.20", Tty: tt.tty})
			resp, err := http.Post(srv.URL+"/containers/create?name=web", "application/json", bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			var cr createResponse
			_ = json.NewDecoder(resp.Body).Decode(&cr)
			resp.Body.Close()

			resp = do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil)
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("start status = %d", resp.StatusCode)
			}
			// The backend must have been asked for the TTY, ...
			spec := fa.specFor("web")
			if spec == nil {
				t.Fatal("start did not deploy")
			}
			if spec.TTY != tt.tty {
				t.Fatalf("deployed spec.TTY = %v, want %v", spec.TTY, tt.tty)
			}
			// ... and inspect must report the same thing back, or the CLI decodes
			// the attach stream with the wrong framing.
			resp = do(t, http.MethodGet, srv.URL+"/containers/web/json", nil)
			var cj containerJSON
			_ = json.NewDecoder(resp.Body).Decode(&cj)
			resp.Body.Close()
			if cj.Config.Tty != tt.tty {
				t.Fatalf("inspect Config.Tty = %v, want %v (spec said %v)", cj.Config.Tty, tt.tty, spec.TTY)
			}
		})
	}
}
