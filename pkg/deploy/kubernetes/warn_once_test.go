package kubernetes

import (
	"context"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// warnEverythingSpec requests, at once, everything this backend is known to warn
// about, so a counting test can see a message emitted more than once.
func warnEverythingSpec() api.DeploySpec {
	return api.DeploySpec{
		Name:            "web",
		Image:           "localhost:5000/app:v1",
		User:            "app",
		SecurityOpt:     []string{"seccomp=unconfined"},
		GroupAdd:        []string{"docker"},
		Tmpfs:           []string{"/run:size=64m"},
		PIDMode:         "service:db",
		IPCMode:         "shareable",
		Ulimits:         []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
		Devices:         []string{"/dev/fuse:/dev/fuse:rwm"},
		StopSignal:      "SIGINT",
		Init:            ptr.To(true),
		Ports:           []api.PortMapping{{Host: 8080, Container: 80, HostIP: "127.0.0.1"}},
		Volumes:         []api.VolumeSpec{{Name: "cache", Target: "/data", Driver: "local"}},
		Healthcheck:     &api.Healthcheck{Test: []string{"CMD", "true"}, StartInterval: "1s"},
		Mounts:          []api.Mount{{Source: "/host/data", Target: "/data-in", SELinux: "z"}},
		Credentials:     &api.CredentialSpec{Sources: []api.CredentialSource{{Name: "aws", Backend: "aws-sts"}}},
		Egress:          &api.EgressSpec{Mode: "proxy", Default: "client"},
		StopGracePeriod: "30s",
	}
}

// countWarnings buckets the captured log by message text (what a reader actually
// sees, including the attributes that distinguish two legitimately-different
// warnings of the same shape).
func countWarnings(out string) map[string]int {
	seen := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		start := strings.Index(line, "msg=")
		if start < 0 {
			continue
		}
		seen[line[start:]]++
	}
	return seen
}

// TestApplyNeverRepeatsAWarning pins that a spec requesting everything this
// backend cannot honor produces each warning AT MOST ONCE.
//
// Counting is what catches duplication, and every other assertion in this package
// uses strings.Contains, which cannot see a second line. That blind spot is not
// hypothetical: ApplyWithAttachments used to build the plain Deployment
// (b.deployment) and then THROW IT AWAY in favour of deploymentWithAttachments,
// which calls b.deployment again — so every per-field warning inside the
// translation fired TWICE on the attach path, reading as two separate problems,
// with the whole suite green. Both arms are exercised below because only the
// attachment arm had the bug.
func TestApplyNeverRepeatsAWarning(t *testing.T) {
	t.Run("plain apply", func(t *testing.T) {
		buf := captureLogs(t)
		b := NewWithClient(fake.NewSimpleClientset(), "default")
		spec := warnEverythingSpec()
		spec.Mounts = nil // the stateless Apply rejects bind mounts outright
		if _, err := b.Apply(context.Background(), spec); err != nil {
			t.Fatalf("apply: %v", err)
		}
		assertNoRepeats(t, buf.String())
	})

	t.Run("attachment apply", func(t *testing.T) {
		buf := captureLogs(t)
		b := NewWithClient(fake.NewSimpleClientset(), "default")
		spec := warnEverythingSpec()
		mounts := []deploy.AttachMount{{Target: "/data-in", Name: "m0", Session: "s1", RelayURL: "http://cornus"}}
		if _, err := b.ApplyWithAttachments(context.Background(), spec, mounts, nil, nil); err != nil {
			t.Fatalf("apply with attachments: %v", err)
		}
		assertNoRepeats(t, buf.String())
	})
}

func assertNoRepeats(t *testing.T, out string) {
	t.Helper()
	seen := countWarnings(out)
	if len(seen) == 0 {
		t.Fatal("a spec requesting every unsupported field produced no warnings at all")
	}
	for msg, n := range seen {
		if n > 1 {
			t.Errorf("warning emitted %d times:\n  %s", n, msg[:min(len(msg), 200)])
		}
	}
}
