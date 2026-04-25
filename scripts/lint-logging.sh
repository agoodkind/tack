#!/usr/bin/env bash
# lint-logging: enforce structured-logging discipline.
#
# Fails when any production Go file outside cmd/ uses fmt.Print* or stdlib
# log.* for diagnostics. cmd/ is exempt because CLI tools are allowed to
# write user-facing output. Test files and generated code are exempt by path.
#
# This script is the single audit gate. Add new exemptions sparingly; the
# whole point is to keep the discipline tight enough to spot regressions.

set -euo pipefail

cd "$(dirname "$0")/.."

# Files we lint: every *.go under internal/, excluding tests and the
# telemetry package that owns the gklog wiring.
mapfile -t files < <(
  find internal -type f -name '*.go' \
    -not -name '*_test.go' \
    -not -path 'internal/test/*' \
    -not -path 'internal/telemetry/*'
)

violations=0

# Banned: fmt.Print, fmt.Println, fmt.Printf, fmt.Fprint*-to-stdout.
for f in "${files[@]}"; do
  matches=$(grep -nE '\bfmt\.Print(ln|f)?\b|\bfmt\.Fprint(ln|f)?\([[:space:]]*os\.Stdout' "$f" || true)
  if [ -n "$matches" ]; then
    echo "$f: banned fmt.Print* (use slog instead):" >&2
    echo "$matches" >&2
    violations=$((violations + $(echo "$matches" | wc -l)))
  fi
done

# Banned: stdlib log package (log.Print*, log.Fatal*, log.Panic*). The
# stdlib log import itself is the signal.
for f in "${files[@]}"; do
  if grep -qE '"log"' "$f"; then
    echo "$f: banned import of stdlib log; use log/slog or gklog instead" >&2
    violations=$((violations + 1))
  fi
done

if [ "$violations" -gt 0 ]; then
  echo "" >&2
  echo "lint-logging: $violations violation(s) found" >&2
  exit 1
fi

echo "lint-logging: clean"
