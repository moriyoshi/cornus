//go:build linux

package barehost

import (
	"reflect"
	"testing"

	"cornus/pkg/api"
)

// unsupportedFieldCases carries this contract in its doc comment: adding a field
// to api.DeploySpec means either mapping it (and adding it to supportedSpec) or
// adding a row.
//
// Nothing enforced it. A "keep this in sync" comment on a hand-maintained list
// parallel to a struct is the shape that produced the very defect the list exists
// to prevent: a field accepted and then dropped in total silence, invisible to
// every other gate — build passes, tests pass, deploy succeeds, workload is not
// what the operator asked for. This backend is where that was found: it dropped
// spec.Ingress entirely — the one host backend that did — plus the six
// kubernetes-only fields (proxy, dns, hub, docker, agentForward, updateConfig)
// that deploy.WarnKubernetesOnlyFields now covers. Nothing in the tree noticed.
//
// This test closes that by reflection: every exported api.DeploySpec field must be
// EXERCISED somewhere — set non-zero by at least one unsupported-field case (so a
// warning is required of it) or by the fully-honoured spec (so silence is required
// of it). A field in neither is one nobody has decided about.
func TestEveryDeploySpecFieldIsMappedOrWarned(t *testing.T) {
	exercised := map[string]bool{}

	note := func(spec api.DeploySpec) {
		v := reflect.ValueOf(spec)
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.PkgPath == "" && !v.Field(i).IsZero() {
				exercised[f.Name] = true
			}
		}
	}
	for _, tc := range unsupportedFieldCases {
		note(tc.spec)
	}
	note(supportedSpec())

	// Name and Image are the identity every case sets as scaffolding; they are not
	// a mapping decision and carry no warning of their own.
	always := map[string]bool{"Name": true, "Image": true}

	specType := reflect.TypeOf(api.DeploySpec{})
	var undecided []string
	for i := 0; i < specType.NumField(); i++ {
		f := specType.Field(i)
		if f.PkgPath != "" || always[f.Name] || exercised[f.Name] {
			continue
		}
		undecided = append(undecided, f.Name)
	}
	if len(undecided) > 0 {
		t.Errorf("api.DeploySpec fields that barehost neither maps nor warns about: %v\n"+
			"Each must either be mapped (and added to supportedSpec, which asserts the backend stays silent) "+
			"or given a row in unsupportedFieldCases (which asserts it warns). A field in neither is accepted "+
			"and dropped in silence.", undecided)
	}
}
