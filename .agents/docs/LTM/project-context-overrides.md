# Project Context Overrides And Trust

## Summary

A project can carry `cornus-context.{json,yaml,toml}` beside its source tree to overlay a selected connection profile. The feature is useful for repository-local server routing, but working-tree configuration is attacker-influenceable, so discovery has explicit provenance, field, and credential-boundary protections.

## Key Facts

- Precedence is selected context < project override < explicit flags/environment. An explicit endpoint and `CORNUS_TOKEN` remain highest priority.
- `--context-file` / `CORNUS_CONTEXT_FILE` names a required explicit file; `--no-context-file` disables discovery and conflicts with it.
- Automatic discovery stops at a `.git` root or the home directory and chooses the nearest candidate.
- Auto-discovered files contribute only `via-server` unless trusted. `--trust-context-file` / `CORNUS_TRUST_CONTEXT_FILE` or an explicit file allows sensitive fields.

## Details

The merge belongs inside `cmd/cornus/internal/clientconn.ResolveWith`, the resolver shared by every command. Reusable parsing and merge code therefore lives in `pkg/clientconfig` as `LoadContextFile`, `Merge`, `ProjectContextNames`, and resolve-rule validation. TOML converts through a map and JSON into strict YAML unmarshalling, preserving the JSON-tag contract and unknown-field rejection used by JSON and YAML.

On Unix, discovery rejects a file owned by another user or a file inside a world-writable non-sticky directory; a non-Unix stub keeps the package portable. Sensitive fields are everything except `via-server` and are removed by `clientconfig.StripSensitive` unless the user expressed trust. A trusted override that changes endpoint without providing token or kube-auth is still prevented from inheriting the selected context's credential, avoiding token exfiltration to a redirected endpoint. Each skip, strip, drop, and applied override logs through slog.

## Files

- `cmd/cornus/internal/clientconn/projectcontext.go` and platform-specific provenance files.
- `pkg/clientconfig/` - strict loaders, merge, field classification, and sensitive stripping.

## Test Coverage

Tests cover discovery boundaries and nearest-wins selection, candidate priority, explicit/missing/disable/conflict paths, JSON/YAML/TOML parity, strict unknown keys, provenance, stripping, and credential co-location.

## Pitfalls

- Do not merge a discovered override before client resolution or duplicate the precedence logic elsewhere.
- A server-only project file must never inherit a selected bearer token unless explicitly trusted and credential-complete.
