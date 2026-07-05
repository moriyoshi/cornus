// Package static is a zero-dependency credential source that returns a fixed
// credential from its config. It is the hermetic backend used by unit and E2E
// tests (no cloud, no network) and is handy for a literal API key.
//
// Config keys:
//
//   - "values": a JSON object of the credential's values. When set it is the
//     whole value set.
//   - otherwise: every config key except the reserved ones ("values",
//     "expiration") becomes a credential value verbatim.
//   - "file": when set, its file is read and its contents become the value under
//     the key named by "key" (default "value").
//   - "expiration": optional RFC3339 timestamp stamped onto the credential.
package static

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"cornus/pkg/credential"
)

func init() { credential.Register("static", newSource) }

type source struct{ cred credential.Credential }

func newSource(cfg map[string]string) (credential.Source, error) {
	reserved := map[string]bool{"values": true, "expiration": true, "file": true, "key": true}
	values := map[string]string{}

	if raw := cfg["values"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, fmt.Errorf("static: parse values: %w", err)
		}
	} else {
		for k, v := range cfg {
			if !reserved[k] {
				values[k] = v
			}
		}
	}

	if path := cfg["file"]; path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("static: read file: %w", err)
		}
		key := cfg["key"]
		if key == "" {
			key = "value"
		}
		values[key] = string(b)
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("static: no values configured")
	}

	cred := credential.Credential{Values: values}
	if exp := cfg["expiration"]; exp != "" {
		t, err := time.Parse(time.RFC3339, exp)
		if err != nil {
			return nil, fmt.Errorf("static: parse expiration: %w", err)
		}
		cred.Expiration = t
	}
	return &source{cred: cred}, nil
}

func (s *source) Fetch(context.Context, map[string]string) (credential.Credential, error) {
	// Return a copy so a consumer cannot mutate the shared map.
	out := credential.Credential{Values: make(map[string]string, len(s.cred.Values)), Expiration: s.cred.Expiration}
	for k, v := range s.cred.Values {
		out.Values[k] = v
	}
	return out, nil
}
