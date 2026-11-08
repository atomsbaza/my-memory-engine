# `testdata/perf/reference` — synthetic reference corpus (G6)

provenance: synthetic — generated for the hook latency gate only (`docs/spec/plan.md` P2.7, gate G6).

## What this is

A my-memory home (`home/`) with **480 synthetic records** across the six layers,
plus the fixture `schema/taxonomy.json`. It exists so the hook latency gate can
run on a corpus large enough to stress the read path — the design's incremental
index trigger is `records >= 400` or `hook p50 > 150 ms`.

| Layer | Records |
|---|---|
| `rules/` | 40 |
| `projects/` | 20 |
| `lessons/` | 280 |
| `decisions/` | 60 |
| `handoffs/` | 40 |
| `knowledge/` | 40 |
| **Total** | **480** |

## Provenance rules

- No record is copied, sampled, or derived from `~/my-memory` or any real
  corpus, prompt log, or personal record. The content is assembled from a fixed
  word list; titles are `verb + noun + noun` and every body ends with
  `(synthetic fixture — provenance: test-generated, not corpus-derived)`.
- Generation is deterministic: seed `20261108`, fixed per-layer counts, IDs
  `layer.<8 hex>` in a reserved hex range (`0a00…`, `0b00…`, `0c00…`, `0d00…`,
  `1e55…`, `dec5…`). Regenerating reproduces the same file set.
- `first_used` / `created` dates are synthetic and carry no meaning beyond
  satisfying the record format.

## How the gate uses it

`tools/benchmark-hook.sh --corpus testdata/perf/reference`:

1. refuses a corpus without this file or without the `provenance: synthetic`
   label, and a corpus with fewer than 400 records;
2. copies `home/` into a throwaway temp directory — the committed fixtures are
   never written to;
3. builds the derived index once (`mem reindex`, untimed setup), then measures
   `mem hook` for the two injection events (`session-start`, `user-prompt`) and
   reports the per-event p50 against the 150 ms G6 threshold.

The derived index (`.memory/index-engine.sqlite`), the recall log, and the
benchmark artifact are never committed; `.gitignore` covers all three.
