package deploy

import (
	"reflect"
	"testing"

	"cornus/pkg/api"
)

func TestOriginToLabelsNilAndEmpty(t *testing.T) {
	if got := OriginToLabels(nil); got != nil {
		t.Errorf("OriginToLabels(nil) = %v, want nil", got)
	}
	if got := OriginToLabels(&api.Origin{}); got != nil {
		t.Errorf("OriginToLabels(empty) = %v, want nil", got)
	}
	if got := OriginToLabels(&api.Origin{Git: &api.GitOrigin{}}); got != nil {
		t.Errorf("OriginToLabels(empty git) = %v, want nil", got)
	}
}

func TestOriginLabelsRoundTrip(t *testing.T) {
	o := &api.Origin{
		Project:   "myproj",
		Host:      "laptop",
		User:      "alice",
		Directory: "/home/alice/src/app",
		Subject:   "user:alice",
		Git: &api.GitOrigin{
			Remote: "git@github.com:o/r.git",
			Branch: "main",
			Commit: "0123456789abcdef0123456789abcdef01234567",
			Dirty:  true,
		},
	}
	labels := OriginToLabels(o)
	if labels[LabelOriginProject] != "myproj" || labels[LabelOriginSubject] != "user:alice" {
		t.Fatalf("labels missing expected keys: %v", labels)
	}
	if labels[LabelOriginGitDirty] != "true" {
		t.Errorf("dirty label = %q, want true", labels[LabelOriginGitDirty])
	}
	back := OriginFromLabels(labels)
	if !reflect.DeepEqual(back, o) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", back, o)
	}
}

func TestOriginFromLabelsNilWhenAbsent(t *testing.T) {
	if got := OriginFromLabels(nil); got != nil {
		t.Errorf("OriginFromLabels(nil) = %v, want nil", got)
	}
	// Only unrelated labels present -> no origin.
	if got := OriginFromLabels(map[string]string{LabelApp: "web", LabelManaged: "true"}); got != nil {
		t.Errorf("OriginFromLabels(unrelated) = %v, want nil", got)
	}
}

func TestOriginToLabelsOmitsEmptyFields(t *testing.T) {
	labels := OriginToLabels(&api.Origin{Project: "p", Host: "h"})
	if _, ok := labels[LabelOriginUser]; ok {
		t.Error("empty User was emitted")
	}
	if _, ok := labels[LabelOriginGitDirty]; ok {
		t.Error("nil git emitted a dirty label")
	}
	if len(labels) != 2 {
		t.Errorf("labels = %v, want exactly project+host", labels)
	}
}
