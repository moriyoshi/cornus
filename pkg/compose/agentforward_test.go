package compose

import "testing"

// TestTranslateAgentForward proves the service-level `x-cornus-agent-forward:
// true` extension maps straight onto api.DeploySpec.AgentForward.
func TestTranslateAgentForward(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    x-cornus-agent-forward: true
  db:
    image: db:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plans["web"].Spec.AgentForward {
		t.Error("web: AgentForward = false, want true (x-cornus-agent-forward: true)")
	}
	if plans["db"].Spec.AgentForward {
		t.Error("db: AgentForward = true, want false (no x-cornus-agent-forward block)")
	}
}
