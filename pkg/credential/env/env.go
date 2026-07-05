// Package env is a zero-dependency credential source that reads a named
// environment variable on the CLIENT (the developer's machine). It is the
// natural way to forward a locally-configured API key — ANTHROPIC_API_KEY,
// OPENAI_API_KEY, etc. — into a workload without copying the secret into the
// image or the deploy spec. The variable is read on each Fetch, so the broker's
// TTL re-fetch picks up a rotated value.
//
// Config keys:
//
//   - "var" (required): the environment variable name to read.
//   - "key": the value name it is returned under (default "value").
//
// The variable must be set and non-empty on the caller, or Fetch errors.
package env

import (
	"context"
	"fmt"
	"os"

	"cornus/pkg/credential"
)

func init() { credential.Register("env", newSource) }

type source struct {
	name string
	key  string
}

func newSource(cfg map[string]string) (credential.Source, error) {
	name := cfg["var"]
	if name == "" {
		return nil, fmt.Errorf("env: missing var")
	}
	key := cfg["key"]
	if key == "" {
		key = "value"
	}
	return &source{name: name, key: key}, nil
}

func (s *source) Fetch(context.Context, map[string]string) (credential.Credential, error) {
	v := os.Getenv(s.name)
	if v == "" {
		return credential.Credential{}, fmt.Errorf("env: %s is unset or empty", s.name)
	}
	return credential.Credential{Values: map[string]string{s.key: v}}, nil
}
