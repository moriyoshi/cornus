package dockerhost

import (
	"reflect"
	"testing"

	"cornus/pkg/api"
)

// TestEveryDeploySpecFieldIsMappedOrWarned is what makes the two hand-maintained
// lists in specfields_test.go a guard rather than a comment.
//
// unsupportedFieldCases carries the contract in its doc comment: "Adding a field
// to api.DeploySpec therefore means doing one of two things here: mapping it (and
// adding it to supportedSpec) or adding a row." A "keep this in sync" note on a
// hand-maintained list parallel to a struct is exactly the shape that produced the
// very defect the list exists to prevent — a field accepted and then dropped in
// total silence, invisible to every other gate.
//
// This closes that by reflection: every exported api.DeploySpec field must be
// EXERCISED somewhere — set non-zero by at least one unsupported-field case (so a
// warning is required of it) or by supportedSpec (so silence is required of it).
// A field in neither is one nobody has decided about.
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
		t.Errorf("api.DeploySpec fields that dockerhost neither maps nor warns about: %v\n"+
			"Each must either be mapped (and added to supportedSpec, which asserts the backend stays silent) "+
			"or given a row in unsupportedFieldCases (which asserts it warns). A field in neither is accepted "+
			"and dropped in silence — on the DEFAULT backend.", undecided)
	}
}
