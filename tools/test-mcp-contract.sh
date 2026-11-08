#!/usr/bin/env bash
# P2.3 MCP gate: the in-process adapter tests plus the binary-level stdio
# contract tests (tools/list snapshot, positive + malformed calls, stdout
# purity), with the real exit code recorded on disk next to the output
# (AGENTS.md: every gate run keeps its real exit code).
#
# When the private Python baseline is available ($MEM_PY_BIN, default
# ~/my-memory/bin/mem) the script also re-extracts its MCP_TOOLS entry for the
# frozen 13 and diffs it against internal/mcp/tools.frozen.json — that is the
# provenance check for the embedded snapshot. The public repo/CI has no baseline,
# so there the check reports "skipped" instead of failing.
#
# usage: tools/test-mcp-contract.sh [extra go test args]
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
out=tests/mcp/test-run.log
mkdir -p tests/mcp

frozen=internal/mcp/tools.frozen.json
baseline_py="${MEM_PY_BIN:-$HOME/my-memory/bin/mem}"
baseline_state="skipped (no Python baseline at $baseline_py)"
baseline_drift=0

if command -v python3 >/dev/null 2>&1 && [ -f "$baseline_py" ]; then
  tmp="$(mktemp -t mcp-frozen)"
  if python3 - "$baseline_py" "$tmp" <<'PY'
import importlib.machinery
import importlib.util
import json
import sys

path, out = sys.argv[1], sys.argv[2]
loader = importlib.machinery.SourceFileLoader("membin", path)
spec = importlib.util.spec_from_loader("membin", loader)
mod = importlib.util.module_from_spec(spec)
loader.exec_module(mod)

FROZEN = [
    "memory_context", "memory_search", "memory_show", "memory_cite",
    "memory_add", "memory_list", "memory_verify", "memory_stats",
    "memory_sync", "memory_archive", "memory_promote", "memory_compact",
    "memory_trace",
]
byname = {t["name"]: t for t in mod.MCP_TOOLS}
missing = [n for n in FROZEN if n not in byname]
if missing:
    sys.exit("baseline is missing frozen tools: " + ", ".join(missing))
with open(out, "w", encoding="utf-8") as fh:
    fh.write(json.dumps([byname[n] for n in FROZEN], ensure_ascii=False, indent=2) + "\n")
PY
  then
    if diff -u "$frozen" "$tmp" >/dev/null 2>&1; then
      baseline_state="ok ($baseline_py)"
    else
      baseline_state="DRIFT ($baseline_py)"
      baseline_drift=1
      diff -u "$frozen" "$tmp" || true
    fi
  else
    baseline_state="ERROR (could not extract MCP_TOOLS from $baseline_py)"
    baseline_drift=1
  fi
  rm -f "$tmp"
fi

{
  echo "# mem P2.3 MCP gate"
  echo "# date:     $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# sha:      $(git rev-parse --short HEAD 2>/dev/null || echo -)"
  echo "# go:       $(go version)"
  echo "# baseline: $baseline_state"
  go test ./internal/mcp ./tests/mcp -count=1 -v "$@"
  echo "exit=$?"
  echo "baseline=$baseline_state"
} 2>&1 | tee "$out"

# tee's exit status is not the test's: read the recorded one.
exit_code="$(sed -n 's/^exit=//p' "$out" | tail -1)"
if [ "${exit_code:-1}" -ne 0 ]; then
  exit 1
fi

# A live baseline that no longer matches the embedded snapshot means the frozen
# contract drifted: fail the gate even though the tests are green.
if [ "$baseline_drift" -ne 0 ]; then
  exit 1
fi
exit 0
