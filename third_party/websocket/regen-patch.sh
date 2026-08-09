#!/bin/sh
# Regenerate cornus.patch: the fork's complete diff against pristine upstream.
# See third_party/p9/regen-patch.sh for why this exists and why it is normalised.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
ver=$(sed -n 's/.*| Version | `\(.*\)` |.*/\1/p' "$here/README.md")
[ -n "$ver" ] || { echo "cannot read the vendored version from README.md" >&2; exit 1; }
up="$(go env GOMODCACHE)/github.com/coder/websocket@$ver"
[ -d "$up" ] || { echo "upstream $ver is not in the module cache" >&2; exit 1; }
diff -ru --exclude=cornus.patch --exclude=regen-patch.sh \
        --exclude=README.md --exclude=README.upstream.md "$up" "$here" |
  # Normalise EVERY occurrence of the two absolute paths, not just the ---/+++
  # lines: `diff -r` also echoes a `diff -ru ... <upstream> <fork>` header before
  # each file, and leaving those unnormalised embeds the checkout location in the
  # patch. That makes the committed patch reproducible only in the tree it was
  # generated in, so the CI gate would fail on every runner and in every worktree.
  sed -e "s|$up|upstream|g" -e "s|$here|fork|g" |
  # Then drop diff's timestamps, which are per-file mtimes and equally unstable.
  sed -E -e 's/^(---|\+\+\+) ([^[:space:]]+)[[:space:]].*/\1 \2/'
