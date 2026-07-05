package otelcollector

import (
	"reflect"
	"testing"
)

func TestSignalsOrAll(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty is all", nil, allSignals},
		{"single", []string{"traces"}, []string{"traces"}},
		{"stable order", []string{"logs", "traces"}, []string{"traces", "logs"}},
		{"dedup", []string{"metrics", "metrics"}, []string{"metrics"}},
		{"unknown ignored -> all", []string{"bogus"}, allSignals},
		{"unknown mixed", []string{"bogus", "logs"}, []string{"logs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := signalsOrAll(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("signalsOrAll(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildConfigMap_ReceiversAndPipelines(t *testing.T) {
	m := buildConfigMap(Config{
		GRPCEndpoint:     "127.0.0.1:4317",
		HTTPEndpoint:     "127.0.0.1:4318",
		ExporterEndpoint: "otlp.example.com:4317",
	})

	protocols := m["receivers"].(map[string]any)["otlp"].(map[string]any)["protocols"].(map[string]any)
	if _, ok := protocols["grpc"]; !ok {
		t.Error("expected grpc receiver protocol")
	}
	if _, ok := protocols["http"]; !ok {
		t.Error("expected http receiver protocol")
	}

	pipelines := m["service"].(map[string]any)["pipelines"].(map[string]any)
	for _, sig := range allSignals {
		p, ok := pipelines[sig].(map[string]any)
		if !ok {
			t.Fatalf("missing %s pipeline", sig)
		}
		if !reflect.DeepEqual(p["exporters"], []any{"otlp_grpc"}) {
			t.Errorf("%s exporters = %v, want [otlp]", sig, p["exporters"])
		}
		if !reflect.DeepEqual(p["processors"], []any{"memory_limiter", "batch"}) {
			t.Errorf("%s processors = %v", sig, p["processors"])
		}
	}
}

func TestBuildConfigMap_HTTPExporterAndSignalsSubset(t *testing.T) {
	m := buildConfigMap(Config{
		HTTPEndpoint:     "127.0.0.1:4318",
		ExporterEndpoint: "https://otlp.example.com",
		ExporterProtocol: "http/protobuf",
		Signals:          []string{"traces"},
	})

	exporters := m["exporters"].(map[string]any)
	if _, ok := exporters["otlp_http"]; !ok {
		t.Fatalf("expected otlp_http exporter, got %v", exporters)
	}
	if _, ok := exporters["otlp_grpc"]; ok {
		t.Error("did not expect grpc otlp exporter for http protocol")
	}

	pipelines := m["service"].(map[string]any)["pipelines"].(map[string]any)
	if len(pipelines) != 1 {
		t.Fatalf("expected 1 pipeline, got %d: %v", len(pipelines), pipelines)
	}
	p := pipelines["traces"].(map[string]any)
	if !reflect.DeepEqual(p["exporters"], []any{"otlp_http"}) {
		t.Errorf("traces exporters = %v, want [otlphttp]", p["exporters"])
	}
}

func TestBuildConfigMap_HeaderEnvResolved(t *testing.T) {
	t.Setenv("CORNUS_OTEL_HEADER_AUTHORIZATION", "Bearer secret")
	m := buildConfigMap(Config{
		GRPCEndpoint:      "127.0.0.1:4317",
		ExporterEndpoint:  "otlp.example.com:4317",
		ExporterHeaderEnv: map[string]string{"authorization": "CORNUS_OTEL_HEADER_AUTHORIZATION"},
	})
	otlp := m["exporters"].(map[string]any)["otlp_grpc"].(map[string]any)
	headers, ok := otlp["headers"].(map[string]any)
	if !ok || headers["authorization"] != "Bearer secret" {
		t.Errorf("env-projected header not resolved: %v", otlp["headers"])
	}
}

func TestBuildConfigMap_HeaderEnvMissingIsDropped(t *testing.T) {
	// An unset env var yields no header (rather than an empty one).
	m := buildConfigMap(Config{
		GRPCEndpoint:      "127.0.0.1:4317",
		ExporterEndpoint:  "otlp.example.com:4317",
		ExporterHeaderEnv: map[string]string{"authorization": "CORNUS_OTEL_HEADER_UNSET_XYZ"},
	})
	otlp := m["exporters"].(map[string]any)["otlp_grpc"].(map[string]any)
	if _, ok := otlp["headers"]; ok {
		t.Errorf("expected no headers when env var unset, got %v", otlp["headers"])
	}
}

func TestBuildConfigMap_HeadersAndDebug(t *testing.T) {
	m := buildConfigMap(Config{
		GRPCEndpoint:     "127.0.0.1:4317",
		ExporterEndpoint: "otlp.example.com:4317",
		ExporterHeaders:  map[string]string{"authorization": "Bearer x"},
		ExporterInsecure: true,
		Debug:            true,
	})

	otlp := m["exporters"].(map[string]any)["otlp_grpc"].(map[string]any)
	headers, ok := otlp["headers"].(map[string]any)
	if !ok || headers["authorization"] != "Bearer x" {
		t.Errorf("headers not carried: %v", otlp["headers"])
	}
	if tls := otlp["tls"].(map[string]any); tls["insecure"] != true {
		t.Errorf("insecure not set: %v", tls)
	}

	exporters := m["exporters"].(map[string]any)
	if _, ok := exporters["debug"]; !ok {
		t.Error("expected debug exporter when Debug=true")
	}
	traces := m["service"].(map[string]any)["pipelines"].(map[string]any)["traces"].(map[string]any)
	if !reflect.DeepEqual(traces["exporters"], []any{"otlp_grpc", "debug"}) {
		t.Errorf("traces exporters = %v, want [otlp debug]", traces["exporters"])
	}
}
