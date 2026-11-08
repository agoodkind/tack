#!/usr/bin/env bash
# Guards against silent drift of the vendored container overlays. Each overlay is
# a full-file ":ro" bind-mount that replaces a file inside a pinned upstream
# image (see docker-compose.yml). For each overlay we record the sha256 of the
# untouched upstream file (*.upstream.sha256) and our change as a unified diff
# (*.patch). This script pulls the pinned image, extracts the real file, and
# fails loudly when either:
#   1. the upstream file changed under the pinned tag (sha256 mismatch), or
#   2. the committed overlay no longer equals upstream + patch.
# We store the upstream's hash rather than the file itself: the upstream yugabyted
# is ~600 KB and its public default constants trip secret scanners. Tracked as
# TACK-302. Tags come from the source of truth: FDB_VERSION in the Makefile and
# the x-yugabyte-image anchor in docker-compose.yml, the one place the ledger
# image is declared (the yugabyte service and tack-ops both reference it).
set -euo pipefail

cd "$(dirname "$0")/.."

note() { printf '%s\n' "$*" >&2; }

fdb_version=$(sed -nE 's/^FDB_VERSION[[:space:]]*\?=[[:space:]]*([^[:space:]]+).*/\1/p' Makefile | head -n1)
yb_image=$(sed -nE 's#^x-yugabyte-image:[[:space:]]*&[^[:space:]]+[[:space:]]+(yugabytedb/yugabyte:[^[:space:]]+).*#\1#p' docker-compose.yml | head -n1)

if [[ -z "$fdb_version" ]]; then note "could not read FDB_VERSION from Makefile"; exit 2; fi
if [[ -z "$yb_image" ]]; then note "could not read the x-yugabyte-image anchor from docker-compose.yml"; exit 2; fi

# Each entry: image | path-inside-image | overlay | upstream-sha256 | patch
entries=(
  "foundationdb/foundationdb:${fdb_version}|/var/fdb/scripts/fdb.bash|fdb-overlay/fdb.bash|fdb-overlay/fdb.bash.upstream.sha256|fdb-overlay/fdb.bash.patch"
  "${yb_image}|/home/yugabyte/bin/yugabyted|yugabyte-overlay/yugabyted|yugabyte-overlay/yugabyted.upstream.sha256|yugabyte-overlay/yugabyted.patch"
)

fail=0
for entry in "${entries[@]}"; do
  IFS='|' read -r image path overlay shafile patch <<<"$entry"
  note "==> $overlay  (from $image:$path)"

  missing=0
  for f in "$overlay" "$shafile" "$patch"; do
    if [[ ! -f "$f" ]]; then note "FAIL: missing vendored file $f"; missing=1; fi
  done
  if [[ "$missing" -ne 0 ]]; then fail=1; continue; fi

  docker pull -q "$image" >/dev/null

  extracted=$(mktemp)
  if ! docker run --rm --entrypoint cat "$image" "$path" > "$extracted" 2>/dev/null; then
    note "FAIL: could not extract $path from $image"
    fail=1; rm -f "$extracted"; continue
  fi

  want=$(tr -d '[:space:]' < "$shafile")
  got=$(sha256sum "$extracted" | cut -d' ' -f1)
  if [[ "$want" != "$got" ]]; then
    note "DRIFT: $image:$path changed (sha256 $got, recorded $want)."
    note "       The pinned image's upstream file moved. Re-vendor:"
    note "         1) extract the new upstream from the image"
    note "         2) re-apply the intended change to produce $overlay"
    note "         3) refresh the hash:  sha256sum <upstream> | cut -d' ' -f1 > $shafile"
    note "         4) refresh the patch: diff -u <upstream> $overlay > $patch"
    note "         5) verify on QA before prod"
    fail=1
  fi

  reconstructed=$(mktemp)
  cp "$extracted" "$reconstructed"
  if ! patch -s "$reconstructed" < "$patch" 2>/dev/null; then
    note "DRIFT: $patch does not apply cleanly to upstream $path. Regenerate it:"
    note "         diff -u <upstream> $overlay > $patch"
    fail=1
  elif ! diff -u "$overlay" "$reconstructed" >/dev/null 2>&1; then
    note "DRIFT: $overlay does not equal upstream + $patch. Regenerate the patch:"
    note "         diff -u <upstream> $overlay > $patch"
    fail=1
  fi

  rm -f "$extracted" "$reconstructed"
done

if [[ "$fail" -ne 0 ]]; then
  note ""
  note "Overlay drift check FAILED (see above). Details: TACK-302."
  exit 1
fi
note "Overlay drift check passed: every overlay matches its pinned upstream (sha256) + patch."
