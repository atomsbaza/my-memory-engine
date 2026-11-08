#!/usr/bin/env bash
# P2.2 sync gate: local bare-repo end-to-end (commit → fetch → pull --rebase
# --autostash → push), the conflict contract and the no-network default paths.
# No remote host and no credentials are involved.
#
# usage: tools/test-sync-local.sh [extra go test args]
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
out=tests/sync/test-run.log
mkdir -p tests/sync

{
  echo "# mem P2.2 local sync gate"
  echo "# date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# sha:  $(git rev-parse --short HEAD 2>/dev/null || echo -)"
  echo "# git:  $(git --version)"
  go test ./internal/sync ./tests/sync -count=1 -v "$@"
  echo "exit=$?"
} 2>&1 | tee "$out"

grep -E '^exit=[0-9]+$' "$out" | tail -1
exit_code="$(sed -n 's/^exit=//p' "$out" | tail -1)"
[ "${exit_code:-1}" -eq 0 ]
