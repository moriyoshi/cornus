package tunnel

import (
	"context"
	"errors"
	"testing"
)

type stubProvider struct{}

func (stubProvider) Start(context.Context, Credential, Options) (Session, error) {
	return nil, errors.New("stub")
}

func TestRegisterAndOpen(t *testing.T) {
	Register("test-stub", func() (any, error) { return stubProvider{}, nil })

	p, err := Open("test-stub")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := p.(stubProvider); !ok {
		t.Fatalf("Open returned %T, want stubProvider", p)
	}

	found := false
	for _, n := range Backends() {
		if n == "test-stub" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Backends() = %v, missing test-stub", Backends())
	}
}

func TestOpenUnknownBackend(t *testing.T) {
	if _, err := Open("does-not-exist"); err == nil {
		t.Fatal("Open(unknown) returned nil error, want an error")
	}
}
