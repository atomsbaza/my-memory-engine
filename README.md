# my-memory-engine

A local-first, cross-harness **agent memory engine** written in Go. It is the rewrite of the
original Python CLI (`~/my-memory`, frozen at tag `python-final`) that powers persistent memory
for AI coding agents: markdown records in a git repo are the source of truth, everything else
(index, cache) is derived and rebuildable.

**Status: production for personal use.** The engine is the writer of record for the `claude` and
`omp` harnesses (live probes 19/19 PASS). Phases P0–P4 of the migration plan are complete; the
repo is `BLOCKED — public gate pending` on two owner-only items: cross-platform (Linux/Windows)
native evidence (G3) and an explicit publication approval.

```
License: MIT (see LICENSE) · Language: Go 1.27 · Module: github.com/atomsbaza/my-memory-engine
S5 recall gate: recall@5 0.8667 · MRR 0.85 · 30 pairs · hook latency p50 61 ms (limit 150 ms)
```

## Features

| Area | What you get |
|---|---|
| **Record store** | Markdown + YAML frontmatter, one file per fact, in a git working tree. Layers: `rule`, `project`, `lesson`, `decision`, `handoff`, `knowledge` plus append-only `ops/`. IDs are content-addressed: `sha1(layer\|scope\|title\|body)[:8]`. |
| **Hybrid retrieval** | SQLite FTS5 (BM25) + TF-IDF sparse vectors over 400-char chunks, fused with Reciprocal Rank Fusion (k=60). Byte-for-byte ranking parity with the Python baseline. |
| **Lifecycle governance** | `archive` (retention score < threshold → cold storage), `promote` (lesson → rule when `uses ≥ N` and `successes == uses`), `compact` (old handoffs merged into knowledge records), `supersede`, `cite`. |
| **Decay** | Per-record `decay` (0–1) and layer-aware `half_life` (rule ∞, project 30d, handoff 3d, else 14d). Unreinforced facts sink; reinforced facts resist decay. |
| **Write guards** | Secret scan, absolute-path refusal, PII regex (email/phone/SSN/card), prompt-injection warnings, near-duplicate block (jaccard ≥ 0.88) / warn band (0.60–0.88). Failures are observable — never silent. |
| **Hooks** | Three events (`session-start`, `user-prompt`, `stop`) × four output formats (`text`, `json`, `hermes`, `cursor`) × seven harness registries. Fail-open with a runtime log line; hooks always `exit 0` and never push. |
| **MCP server** | 13 frozen tools over stdio JSON-RPC (`memory_context`, `memory_search`, `memory_show`, `memory_cite`, `memory_add`, `memory_list`, `memory_verify`, `memory_stats`, `memory_sync`, `memory_archive`, `memory_promote`, `memory_compact`, `memory_trace`). |
| **Eval gate** | 30-pair golden corpus, recall@5 + MRR + threshold sweep, fail-closed against `eval/baseline.json`. Runs as a recurring gate on every engine change that can affect retrieval. |
| **Sync** | Any git remote (GitHub / Bitbucket / self-hosted) through the git CLI. Opt-in per memory home, never `--force`, conflicts stop with a named file. |
| **Observability** | `mem trace <query>` prints the RRF breakdown (fts/vec/scored ranks, retention score) plus per-layer context budgets; `mem stats` reports decay buckets and harness coverage. |
| **Installer** | `mem-install` wires MCP for nine harnesses (claude, codex, gemini, opencode, cursor, hermes, omp, kiro-cli, qoder) plus hooks where supported — idempotent, byte-backup before every write, warns on `core.hooksPath`. |
| **Benchmark** | `tools/benchmark-hook.sh` gates hook p50 latency ≤ 150 ms on a 480-record synthetic corpus (with a delay-based negative control). |

## Build

```sh
go build -o bin/mem ./cmd/mem      # engine
go build -o bin/mem-install ./cmd/mem-install   # harness installer
go test ./...                       # 15 packages, all green

./bin/mem --version                 # mem version 0.1.0 (single source of truth:
                                    # internal/version, also reported in the MCP handshake)
```

Current release: **v0.1.0** (see git tags).

No external services, no daemons, no runtime dependencies beyond the `git` CLI (used only for
commits and explicit sync). Python 3 is **not** required.

## Usage

```sh
# point MEM_HOME at your memory repo (or let it find ~/my-memory)
export MEM_HOME=~/my-memory

mem context                         # rules + scoped records + knowledge pointers (budgeted)
mem search "fts5 ranking" --limit 5
mem trace "security guard"          # RRF breakdown + layer allocation
mem add --layer lesson --scope global --file - --title "..." < note.md
mem verify                          # schema/secret/path/dup/index audit
mem stats                           # counts, sources, decay buckets, harness coverage
mem archive --dry-run               # retention-based archival plan
mem promote --dry-run               # lesson → rule candidates
mem compact --dry-run               # old handoff → knowledge plan
mem eval --json                     # retrieval gate against baseline

mem mcp                             # MCP server on stdio (wire from any MCP client)
mem hook --agent claude --event user-prompt --format json   # harness hook entry point
```

## Architecture

Single modular-monolith binary; every entrypoint (CLI / MCP / hook) calls the same domain layer.
Markdown in the data repo is canonical; the SQLite index under `.memory/` is derived and can be
deleted and rebuilt at any time. Network access exists only in `mem sync`, and only to the remote
you configured.

```
harness (claude, omp, …)
   │  MCP stdio · hooks · CLI
   ▼
engine core ── domain / codec / schema / store / index / sync
   │                    │
   ▼                    ▼
canonical git tree   derived index (.memory/, gitignored)
   └── opt-in: git remote (any host, never force-pushed)
```

Full design: [`docs/spec/design.md`](docs/spec/design.md) (rev 5, passed an independent
architecture gate with 0 blockers). Migration plan and phase gates:
[`docs/spec/plan.md`](docs/spec/plan.md), [`specs/`](specs/).

## Verification story

- **Unit + integration:** `go test ./...` — 15 packages, including fault-injected atomic writes,
  guard refusals, dedup boundary controls, hook fail-open, sync conflict paths and MCP contract
  tests against the frozen snapshot.
- **Differential parity:** every command family (context/search, show/list, write lifecycle,
  verify/stats, render, hook, MCP) is compared byte-for-byte against the Python baseline on a
  fresh clone — **133 probes, 7/7 families PASS** (artifacts live in the private data repo).
- **S5 recall gate:** recall@5 0.8667 / MRR 0.85 on the real corpus, re-run whenever retrieval
  code changes, artifact bound to the binary sha256.
- **Hygiene:** gitleaks (tree + full history) clean, govulncheck clean, 518 synthetic fixtures
  reviewed with provenance labels, private paths scrubbed, identifier gate with a recorded
  `(path, line-shape)` exception for `LICENSE` only.

## Migration state

| Harness | Engine | Notes |
|---|---|---|
| claude | ✅ Go | MCP + hooks cut over, live probe 19/19 |
| omp | ✅ Go | MCP + hooks + extension cut over, live probe 19/19 |
| kiro-cli | ✅ Go (MCP) | registered in `~/.kiro/settings/mcp.json` (global scope), tools 13/13, write/read verified through `kiro-cli chat --trust-all-tools` |
| qoder | ✅ Go (MCP) | registered in `~/.qoder/settings.json` (user scope), tools 13/13, write/read verified through `qoder -p --permission-mode bypassPermissions` |
| codex, gemini, cursor, hermes, opencode | ⏳ deferred | still point at the frozen Python CLI (reads work; writes are refused with a pointer to this engine) |

The Python CLI in the data repo is **read-only legacy** (tag `python-final`): write commands exit
3 with a pointer to this engine; `bin/mem-backup` still works. Rollback per harness:
`tools/cutover-switch.sh --harness <name> --to python` (proven byte-exact).

## Data boundary

- This repo must never contain real corpus records, prompt logs, personal data or secrets —
  `testdata/` holds synthetic fixtures with provenance labels only, enforced by the hygiene gate.
- Real data (records, parity artifacts, spike outputs) lives in the private data repo.
- The repo is currently **private**. Making it public additionally requires the owner's recorded
  approval (`parity/P4/status.md` in the data repo tracks the remaining gate items: G3
  cross-platform native evidence and the publication signature).

## Docs

- [`docs/spec/design.md`](docs/spec/design.md) — architecture contract (rev 5)
- [`docs/spec/plan.md`](docs/spec/plan.md) — 37-task migration plan with gates
- [`specs/phases/`](specs/phases/) — per-phase specs (P0 language gate … P4 public release)
- [`docs/cli-commands.md`](docs/cli-commands.md) — generated CLI manifest (drift-gated by test)
