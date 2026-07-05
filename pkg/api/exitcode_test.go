package api

import "testing"

// TestTerminalExitCode pins the contract every exit-code consumer depends on:
// a settled status yields its code, and anything that has NOT settled — or a
// backend that cannot report — yields "unknown" rather than a fabricated 0.
// The distinction is the whole point: `docker wait` through the Docker API
// proxy reports 0 only when it really means exit 0.
func TestTerminalExitCode(t *testing.T) {
	tests := []struct {
		name      string
		instances []InstanceStatus
		want      int
		wantKnown bool
	}{
		{
			name:      "no instances is unknown",
			instances: nil,
		},
		{
			name:      "running instance has not settled",
			instances: []InstanceStatus{{ID: "a", State: "running", Running: true}},
		},
		{
			name:      "stopped without a reported code is unknown",
			instances: []InstanceStatus{{ID: "a", State: "exited"}},
		},
		{
			name:      "clean exit",
			instances: []InstanceStatus{{ID: "a", State: "exited", ExitCode: intp(0)}},
			want:      0,
			wantKnown: true,
		},
		{
			name:      "failed exit",
			instances: []InstanceStatus{{ID: "a", State: "exited", ExitCode: intp(3)}},
			want:      3,
			wantKnown: true,
		},
		{
			name: "one replica still running: unknown even though a sibling exited",
			instances: []InstanceStatus{
				{ID: "a", State: "exited", ExitCode: intp(7)},
				{ID: "b", State: "running", Running: true},
			},
		},
		{
			name: "a failed replica wins over a clean one",
			instances: []InstanceStatus{
				{ID: "a", State: "exited", ExitCode: intp(0)},
				{ID: "b", State: "exited", ExitCode: intp(9)},
			},
			want:      9,
			wantKnown: true,
		},
		{
			name: "a replica with no reported code does not veto a known one",
			instances: []InstanceStatus{
				{ID: "a", State: "exited"},
				{ID: "b", State: "exited", ExitCode: intp(2)},
			},
			want:      2,
			wantKnown: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, known := DeployStatus{Name: "w", Instances: tc.instances}.TerminalExitCode()
			if known != tc.wantKnown || got != tc.want {
				t.Fatalf("TerminalExitCode() = %d, %v; want %d, %v", got, known, tc.want, tc.wantKnown)
			}
		})
	}
}
