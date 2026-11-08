#!/usr/bin/env bash
# P2.2 CLI gate: the in-process command tests plus the binary-level
# integration tests, with the real exit code recorded on disk next to the
# output (AGENTS.md: every gate run keeps its real exit code).
#
# usage: tools/test-cli.sh [extra go test args]
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
out=tests/cli/test-run.log
mkdir -p tests/cli

{
  echo "# mem P2.2 CLI gate"
  echo "# date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# sha:  $(git rev-parse --short HEAD 2>/dev/null || echo -)"
  echo "# go:   $(go version)"
  go test ./internal/cli ./tests/cli -count=1 -v "$@"
  echo "exit=$?"
} 2>&1 | tee "$out"

# tee's exit status is not the test's: read the recorded one.
grep -E '^exit=[0-9]+$' "$out" | tail -1
exit_code="$(sed -n 's/^exit=//p' "$out" | tail -1)"
[ "${exit_code:-1}" -eq 0 ]
