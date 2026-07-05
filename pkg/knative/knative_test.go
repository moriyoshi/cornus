package knative

import (
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"ksvc-v1", "apiVersion: serving.knative.dev/v1\nkind: Service\n", true},
		{"ksvc-v1beta1", "apiVersion: serving.knative.dev/v1beta1\nkind: Service\n", true},
		{"native-spec", "name: web\nimage: nginx\n", false},
		{"k8s-deployment", "apiVersion: apps/v1\nkind: Deployment\n", false},
		{"eventing", "apiVersion: eventing.knative.dev/v1\nkind: Broker\n", false},
		{"garbage", "\t\t: : not yaml : :", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect([]byte(c.data)); got != c.want {
				t.Fatalf("Detect(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

const fullKsvc = `
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: hello
spec:
  template:
    metadata:
      annotations:
        autoscaling.knative.dev/minScale: "1"
        autoscaling.knative.dev/maxScale: "5"
        autoscaling.knative.dev/target: "80"
        autoscaling.knative.dev/class: "kpa.autoscaling.knative.dev"
        autoscaling.knative.dev/metric: "concurrency"
        autoscaling.knative.dev/window: "60s"
    spec:
      containerConcurrency: 100
      timeoutSeconds: 300
      containers:
        - image: ghcr.io/example/hello@sha256:abc
          command: ["/server"]
          args: ["--port=8080"]
          workingDir: /app
          env:
            - name: GREETING
              value: hi
            - name: SECRET
              valueFrom:
                secretKeyRef:
                  name: s
                  key: k
          ports:
            - containerPort: 8080
          resources:
            limits:
              cpu: "500m"
              memory: "512Mi"
            requests:
              cpu: "100m"
              memory: "256Mi"
`

func TestLoadFullService(t *testing.T) {
	spec, warnings, err := Load([]byte(fullKsvc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if spec.Name != "hello" {
		t.Errorf("Name = %q, want hello", spec.Name)
	}
	if spec.Image != "ghcr.io/example/hello@sha256:abc" {
		t.Errorf("Image = %q", spec.Image)
	}
	// ksvc container.command is the entrypoint; args are the arguments.
	if len(spec.Entrypoint) != 1 || spec.Entrypoint[0] != "/server" {
		t.Errorf("Entrypoint = %v, want [/server]", spec.Entrypoint)
	}
	if len(spec.Command) != 1 || spec.Command[0] != "--port=8080" {
		t.Errorf("Command = %v, want [--port=8080]", spec.Command)
	}
	if spec.WorkingDir != "/app" {
		t.Errorf("WorkingDir = %q", spec.WorkingDir)
	}
	if spec.Env["GREETING"] != "hi" {
		t.Errorf("Env[GREETING] = %q", spec.Env["GREETING"])
	}
	if _, ok := spec.Env["SECRET"]; ok {
		t.Errorf("valueFrom env should be dropped, got %q", spec.Env["SECRET"])
	}
	if len(spec.Ports) != 1 || spec.Ports[0].Container != 8080 {
		t.Errorf("Ports = %v, want one 8080", spec.Ports)
	}
	if spec.Resources == nil || spec.Resources.CPULimit != 0.5 || spec.Resources.MemoryLimit != 512*1024*1024 {
		t.Errorf("Resources limits = %+v", spec.Resources)
	}
	if spec.Resources.ReservedCPU != 0.1 || spec.Resources.ReservedMemory != 256*1024*1024 {
		t.Errorf("Resources requests = %+v", spec.Resources)
	}
	kn := spec.Knative
	if kn == nil || !kn.Enabled {
		t.Fatalf("Knative block not enabled: %+v", kn)
	}
	if kn.MinScale == nil || *kn.MinScale != 1 {
		t.Errorf("MinScale = %v, want 1", kn.MinScale)
	}
	if kn.MaxScale == nil || *kn.MaxScale != 5 {
		t.Errorf("MaxScale = %v, want 5", kn.MaxScale)
	}
	if kn.Target == nil || *kn.Target != 80 {
		t.Errorf("Target = %v, want 80", kn.Target)
	}
	if kn.Class != "kpa" {
		t.Errorf("Class = %q, want kpa", kn.Class)
	}
	if kn.Metric != "concurrency" {
		t.Errorf("Metric = %q", kn.Metric)
	}
	if kn.Concurrency == nil || *kn.Concurrency != 100 {
		t.Errorf("Concurrency = %v, want 100", kn.Concurrency)
	}
	if kn.TimeoutSeconds == nil || *kn.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds = %v, want 300", kn.TimeoutSeconds)
	}
	// Port is recorded on the Knative block too.
	if kn.Port != 8080 {
		t.Errorf("Knative.Port = %d, want 8080", kn.Port)
	}
	// The unrecognised autoscaling annotation is preserved for round-trip.
	if kn.Annotations["autoscaling.knative.dev/window"] != "60s" {
		t.Errorf("passthrough window annotation lost: %v", kn.Annotations)
	}
	// A dropped valueFrom env produces a warning.
	if !containsSubstr(warnings, "valueFrom") {
		t.Errorf("expected a valueFrom warning, got %v", warnings)
	}
}

func TestLoadExecProbe(t *testing.T) {
	const manifest = `
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: probed
spec:
  template:
    spec:
      containers:
        - image: nginx
          readinessProbe:
            exec:
              command: ["cat", "/tmp/ready"]
            periodSeconds: 5
            failureThreshold: 3
`
	spec, _, err := Load([]byte(manifest))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if spec.Healthcheck == nil {
		t.Fatal("exec probe should map to a Healthcheck")
	}
	if got := strings.Join(spec.Healthcheck.Test, " "); got != "CMD cat /tmp/ready" {
		t.Errorf("Test = %q", got)
	}
	if spec.Healthcheck.Interval != "5s" || spec.Healthcheck.Retries != 3 {
		t.Errorf("probe timings = %+v", spec.Healthcheck)
	}
}

func TestLoadHTTPProbeWarns(t *testing.T) {
	const manifest = `
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: httpprobe
spec:
  template:
    spec:
      containers:
        - image: nginx
          readinessProbe:
            httpGet:
              path: /healthz
`
	spec, warnings, err := Load([]byte(manifest))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if spec.Healthcheck != nil {
		t.Errorf("non-exec probe should not become a Healthcheck, got %+v", spec.Healthcheck)
	}
	if !containsSubstr(warnings, "non-exec") {
		t.Errorf("expected a non-exec probe warning, got %v", warnings)
	}
}

func TestLoadMultiPortWarns(t *testing.T) {
	const manifest = `
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: multiport
spec:
  template:
    spec:
      containers:
        - image: nginx
          ports:
            - containerPort: 8080
            - containerPort: 9090
`
	spec, warnings, err := Load([]byte(manifest))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(spec.Ports) != 1 || spec.Ports[0].Container != 8080 {
		t.Errorf("expected single port 8080, got %v", spec.Ports)
	}
	if !containsSubstr(warnings, "single port") {
		t.Errorf("expected a single-port warning, got %v", warnings)
	}
}

func TestLoadTrafficWarns(t *testing.T) {
	const manifest = `
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: split
spec:
  template:
    spec:
      containers:
        - image: nginx
  traffic:
    - revisionName: split-00001
      percent: 50
    - revisionName: split-00002
      percent: 50
`
	_, warnings, err := Load([]byte(manifest))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !containsSubstr(warnings, "traffic") {
		t.Errorf("expected a traffic warning, got %v", warnings)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"wrong-kind", "apiVersion: serving.knative.dev/v1\nkind: Route\n", "not a serving.knative.dev Service"},
		{"no-name", "apiVersion: serving.knative.dev/v1\nkind: Service\nspec:\n  template:\n    spec:\n      containers:\n        - image: nginx\n", "metadata.name"},
		{"no-container", "apiVersion: serving.knative.dev/v1\nkind: Service\nmetadata:\n  name: x\nspec:\n  template:\n    spec:\n      containers: []\n", "containers is empty"},
		{"no-image", "apiVersion: serving.knative.dev/v1\nkind: Service\nmetadata:\n  name: x\nspec:\n  template:\n    spec:\n      containers:\n        - {}\n", "image is required"},
		{
			"multi-container",
			"apiVersion: serving.knative.dev/v1\nkind: Service\nmetadata:\n  name: x\nspec:\n  template:\n    spec:\n      containers:\n        - image: a\n        - image: b\n",
			"multi-container",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Load([]byte(c.manifest))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load error = %v, want substring %q", err, c.want)
			}
		})
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
