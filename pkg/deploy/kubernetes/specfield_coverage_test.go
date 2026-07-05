package kubernetes

import (
	"reflect"
	"testing"

	"cornus/pkg/api"
)

// TestEveryDeploySpecFieldIsMappedOrWarned closes the "keep this in sync" gap by
// reflection: a hand-maintained list parallel to a struct is exactly the shape
// that produces the defect the list exists to prevent — a field accepted and then
// dropped in total silence, invisible to every other gate (build passes, tests
// pass, deploy succeeds, workload is not what the operator asked for).
//
// Every exported api.DeploySpec field must be EXERCISED somewhere: set non-zero by
// at least one unsupported-field case (so a warning is required of it) or by one of
// the fully-honoured supported specs (so silence AND a realization assertion are
// required of it). A field in neither is one nobody has decided about.
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
	for _, tc := range supportedCases() {
		note(tc.spec)
	}

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
		t.Errorf("api.DeploySpec fields the kubernetes backend neither maps nor warns about: %v\n"+
			"Each must either be mapped (and added to a supportedCases spec, which asserts the backend stays "+
			"silent and that the mapping reaches the cluster objects) or given a row in unsupportedFieldCases "+
			"(which asserts it warns). A field in neither is accepted and dropped in silence.", undecided)
	}
}
