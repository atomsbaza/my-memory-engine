#!/usr/bin/env bash
# G6 hook latency gate (plan P2.7, design §9.4, NFR-PERF-01).
#
# Runs `mem hook` against the synthetic reference corpus, reports the per-event
# p50 and exits 0 only when every measured event stays within the threshold.
# The corpus is staged into a throwaway directory and indexed there once
# (untimed setup) — the committed fixtures are never written to, and no real
# memory repository is read.
#
# The measurement core is tools/hookbench (Go): it spawns one `mem hook`
# process per sample, so the budget covers process start, record scan, selection
# and render — the same path a harness pays.
#
# Measured events are the two a harness waits on inside the user's turn:
# `session-start` and `user-prompt`. `stop` is excluded on purpose: it rebuilds
# the whole index and performs a local git commit, so it is a write path with
# its own budget, not the per-prompt latency G6 bounds.
#
# usage: tools/benchmark-hook.sh [options]
#   --corpus DIR       synthetic corpus root (default: testdata/perf/reference)
#   --runs N           timed samples per event (default: 30)
#   --warmup N         untimed warm-up runs per event (default: 5)
#   --threshold-ms N   G6 p50 ceiling in milliseconds (default: 150)
#   --events LIST      comma-separated: session-start,user-prompt
#   --delay-ms N       blocking delay per run — negative control, must fail the
#                      gate; 0 (default) measures the real hook
#   --bin PATH         use a prebuilt mem binary instead of `go build ./cmd/mem`
#   --out FILE         JSON artifact path (default: tests/perf/benchmark-hook-latest.json)
#   --no-artifact      print the report but write no artifact (never a passing gate)
#   -h, --help         this text
#
# exit codes: 0 gate PASS · 1 gate FAIL · 2 refused or setup failure
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

usage() {
  cat <<'USAGE'
G6 hook latency gate (plan P2.7, design §9.4, NFR-PERF-01).

Runs `mem hook` against the synthetic reference corpus, reports the per-event
p50 and exits 0 only when every measured event stays within the threshold.

usage: tools/benchmark-hook.sh [options]
  --corpus DIR       synthetic corpus root (default: testdata/perf/reference)
  --runs N           timed samples per event (default: 30)
  --warmup N         untimed warm-up runs per event (default: 5)
  --threshold-ms N   G6 p50 ceiling in milliseconds (default: 150)
  --events LIST      comma-separated: session-start,user-prompt
  --delay-ms N       blocking delay per run — negative control, must fail the
                     gate; 0 (default) measures the real hook
  --bin PATH         use a prebuilt mem binary instead of `go build ./cmd/mem`
  --out FILE         JSON artifact path (default: tests/perf/benchmark-hook-latest.json)
  -h, --help         this text

exit codes: 0 gate PASS · 1 gate FAIL · 2 refused or setup failure

stop is not measured: it rebuilds the whole index and performs a local git
commit, so it is a write path with its own budget, not the per-prompt latency
G6 bounds.
USAGE
}

corpus=testdata/perf/reference
runs=30
warmup=5
threshold_ms=150
delay_ms=0
events=session-start,user-prompt
out=tests/perf/benchmark-hook-latest.json
binary=""
original="$*"

die() {
  echo "benchmark-hook: $2" >&2
  exit "${1:-2}"
}

is_uint() { case "$1" in '' | *[!0-9]*) return 1 ;; *) return 0 ;; esac; }
is_number() { case "$1" in '' | *[!0-9.]* | *.*.*) return 1 ;; *) return 0 ;; esac; }

need() { [ $# -ge 2 ] && [ -n "$2" ] || die 2 "$1 needs a value"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --corpus) need "$@"; corpus=$2; shift 2 ;;
    --runs) need "$@"; runs=$2; shift 2 ;;
    --warmup) need "$@"; warmup=$2; shift 2 ;;
    --threshold-ms) need "$@"; threshold_ms=$2; shift 2 ;;
    --delay-ms) need "$@"; delay_ms=$2; shift 2 ;;
    --events) need "$@"; events=$2; shift 2 ;;
    --bin) need "$@"; binary=$2; shift 2 ;;
    --out) need "$@"; out=$2; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die 2 "unknown argument: $1 (see --help)" ;;
  esac
done

is_uint "$runs" || die 2 "--runs must be an integer, got '$runs'"
is_uint "$warmup" || die 2 "--warmup must be an integer, got '$warmup'"
is_uint "$delay_ms" || die 2 "--delay-ms must be an integer, got '$delay_ms'"
is_number "$threshold_ms" || die 2 "--threshold-ms must be a number, got '$threshold_ms'"
[ "$runs" -ge 1 ] || die 2 "--runs must be >= 1"
[ -n "$events" ] || die 2 "--events must not be empty"

# Provenance boundary first: the gate must never be pointed at a real corpus.
[ -d "$corpus" ] || die 2 "corpus not found: $corpus"
[ -d "$corpus/home" ] || die 2 "corpus has no home/: $corpus"
[ -f "$corpus/PROVENANCE.md" ] || die 2 "refusing $corpus: no PROVENANCE.md — G6 runs on synthetic corpora only"
grep -q 'provenance: synthetic' "$corpus/PROVENANCE.md" ||
  die 2 "refusing $corpus: PROVENANCE.md does not say 'provenance: synthetic'"
records=$(find "$corpus/home" -name '*.md' -type f | wc -l | tr -d ' ')
[ "$records" -ge 400 ] ||
  die 2 "refusing $corpus: $records records; G6 needs >= 400 (the design's incremental-index trigger)"

work=$(mktemp -d "${TMPDIR:-/tmp}/mem-hookbench.XXXXXX") || die 2 "mktemp failed"
trap 'rm -rf "$work"' EXIT INT TERM

stage="$work/home"
mkdir -p "$stage" || die 2 "create stage failed"
cp -R "$corpus/home/." "$stage/" || die 2 "copy corpus failed"

if [ -n "$binary" ]; then
  mem=$binary
  [ -x "$mem" ] || die 2 "--bin is not executable: $mem"
else
  mem="$work/mem"
  go build -o "$mem" ./cmd/mem || die 2 "go build ./cmd/mem failed"
fi

# Index once, untimed: real homes have a derived index from a previous
# reindex, and the hook's read path opens it.
setup_home="$work/.setup-home"
mkdir -p "$setup_home"
if ! MEM_HOME="$stage" HOME="$setup_home" "$mem" reindex >"$work/reindex.log" 2>&1; then
  cat "$work/reindex.log" >&2
  die 2 "mem reindex on the staged corpus failed"
fi

helper="$work/hookbench"
go build -o "$helper" ./tools/hookbench || die 2 "go build ./tools/hookbench failed"

command_line="./tools/benchmark-hook.sh"
if [ -n "$original" ]; then
  command_line="$command_line $original"
fi

"$helper" \
  -bin "$mem" \
  -home "$stage" \
  -corpus "$corpus" \
  -command "$command_line" \
  -events "$events" \
  -runs "$runs" \
  -warmup "$warmup" \
  -threshold-ms "$threshold_ms" \
  -delay-ms "$delay_ms" \
  -out "$out"
exit $?
