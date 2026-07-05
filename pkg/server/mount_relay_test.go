package server

import (
	"context"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// fakeMountingBackend is a deploy.MountingBackend that records the AttachMounts it
// was given, so a test can act as the pod's caretaker and dial the unified relay
// (see caretaker_relay_test.go).
type fakeMountingBackend struct {
	fakeBackend
	mounts chan []deploy.AttachMount
}

func (f *fakeMountingBackend) ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeployStatus, error) {
	st, _ := f.Apply(ctx, spec)
	select {
	case f.mounts <- mounts:
	default:
	}
	return st, nil
}
