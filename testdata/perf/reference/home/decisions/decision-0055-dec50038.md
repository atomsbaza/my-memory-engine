---
id: decision.dec50038
title: buffer the corpus before the journal (synthetic case 056)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-09-01
last_used: 2026-10-01
expires: none
status: active
sensitivity: internal
---

Keep the explicit queue small, then gather the budget and record the ledger. When the vector looks wrong, stage the synthetic shard instead of the journal.

When the queue looks wrong, validate the small matcher instead of the shard. The portable journal only matters if the queue can refactor the record on its own.

The warm vector only matters if the hook can scan the cache on its own. Prefer a local fixture over a clever pipeline when you wire the index.
(synthetic fixture — provenance: test-generated, not corpus-derived)
