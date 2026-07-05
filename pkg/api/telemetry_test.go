package api

import "testing"

func TestTelemetrySpecActive(t *testing.T) {
	cases := []struct {
		name string
		spec *TelemetrySpec
		want bool
	}{
		{"nil", nil, false},
		{"empty", &TelemetrySpec{}, false},
		{"enabled", &TelemetrySpec{Enabled: true}, true},
		{"endpoint implies enabled", &TelemetrySpec{Endpoint: "otlp:4317"}, true},
		{"blank endpoint stays inactive", &TelemetrySpec{Endpoint: "   "}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Active(); got != tc.want {
				t.Fatalf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTelemetrySpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    *TelemetrySpec
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"inactive ok even if odd protocol", &TelemetrySpec{Protocol: "bogus"}, false},
		{"active without endpoint fails", &TelemetrySpec{Enabled: true}, true},
		{"default protocol", &TelemetrySpec{Endpoint: "otlp:4317"}, false},
		{"grpc", &TelemetrySpec{Endpoint: "otlp:4317", Protocol: "grpc"}, false},
		{"http alias", &TelemetrySpec{Endpoint: "https://otlp", Protocol: "http"}, false},
		{"http/protobuf", &TelemetrySpec{Endpoint: "https://otlp", Protocol: "http/protobuf"}, false},
		{"bad protocol", &TelemetrySpec{Endpoint: "otlp:4317", Protocol: "thrift"}, true},
		{"good signals", &TelemetrySpec{Endpoint: "otlp:4317", Signals: []string{"traces", "logs"}}, false},
		{"bad signal", &TelemetrySpec{Endpoint: "otlp:4317", Signals: []string{"events"}}, true},
		{"neg grpc port", &TelemetrySpec{Endpoint: "otlp:4317", GRPCPort: -1}, true},
		{"huge http port", &TelemetrySpec{Endpoint: "otlp:4317", HTTPPort: 70000}, true},
		{"ok ports", &TelemetrySpec{Endpoint: "otlp:4317", GRPCPort: 4317, HTTPPort: 4318}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
