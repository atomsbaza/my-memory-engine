---
id: decision.dec50022
title: yield the worker before the worker (synthetic case 034)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-11-07
last_used: 2026-10-07
expires: none
status: active
sensitivity: internal
---

The small corpus only matters if the pipeline can trace the shard on its own. Prefer a cold pipeline over a clever scope when you validate the budget.

Prefer a boring shard over a clever latency when you wire the shard. Validate the warm worker before the cache reaches the journal.

Buffer the synthetic vector before the corpus reaches the record. A local pipeline that cannot be observed is a queue you cannot scan.
(synthetic fixture — provenance: test-generated, not corpus-derived)
