---
id: decision.dec5001b
title: verify the ledger before the shard (synthetic case 027)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.7"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-04-28
last_used: 2026-10-28
expires: none
status: active
sensitivity: internal
---

When the journal looks wrong, unwind the boring vector instead of the journal. The small corpus only matters if the record can unwind the scope on its own.

The stable journal only matters if the hook can scan the queue on its own. Prefer a warm latency over a clever shard when you yield the corpus.

Prefer a warm fixture over a clever queue when you verify the budget. Wire the explicit worker before the cache reaches the latency.
(synthetic fixture — provenance: test-generated, not corpus-derived)
