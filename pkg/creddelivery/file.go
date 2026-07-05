package creddelivery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cornus/pkg/credential"
)

// Render encodes a credential in one of the neutral file formats:
//
//   - "" / "json": the cornus-native {"values":{...},"expiration":"..."} object.
//   - "env": sorted KEY=VALUE lines (expiration omitted).
//   - "raw": a single value (the sole value, or the "value"/"token" key).
//   - "aws-credentials": an ini [default] profile (AWS shared-credentials file).
//
// Cloud-specific formats are just renderers over the neutral credential — the
// broker itself stays agnostic.
func Render(cred credential.Credential, format string) ([]byte, error) {
	switch format {
	case "", "json":
		return json.MarshalIndent(cred, "", "  ")
	case "env":
		keys := sortedKeys(cred.Values)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%s\n", k, cred.Values[k])
		}
		return []byte(b.String()), nil
	case "raw":
		v, err := singleValue(cred.Values)
		if err != nil {
			return nil, err
		}
		return []byte(v), nil
	case "aws-credentials":
		return awsCredentialsINI(cred.Values), nil
	default:
		return nil, fmt.Errorf("creddelivery: unknown file format %q", format)
	}
}

// WriteFile renders cred and atomically writes it to path (0600), creating parent
// directories as needed.
func WriteFile(path, format string, cred credential.Credential) error {
	data, err := Render(cred, format)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cred-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func singleValue(m map[string]string) (string, error) {
	if len(m) == 1 {
		for _, v := range m {
			return v, nil
		}
	}
	for _, k := range []string{"value", "token"} {
		if v, ok := m[k]; ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("creddelivery: raw format needs a single value or a \"value\"/\"token\" key (have %v)", sortedKeys(m))
}

// awsCredentialsINI renders the AWS shared-credentials [default] profile from the
// neutral values, accepting the canonical (AccessKeyId) or snake_case keys.
func awsCredentialsINI(v map[string]string) []byte {
	get := func(aliases ...string) string {
		for _, a := range aliases {
			if val, ok := v[a]; ok {
				return val
			}
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("[default]\n")
	fmt.Fprintf(&b, "aws_access_key_id = %s\n", get("AccessKeyId", "aws_access_key_id", "access_key_id"))
	fmt.Fprintf(&b, "aws_secret_access_key = %s\n", get("SecretAccessKey", "aws_secret_access_key", "secret_access_key"))
	if tok := get("SessionToken", "Token", "aws_session_token", "session_token"); tok != "" {
		fmt.Fprintf(&b, "aws_session_token = %s\n", tok)
	}
	return []byte(b.String())
}
