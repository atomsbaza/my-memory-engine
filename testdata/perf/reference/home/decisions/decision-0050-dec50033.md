---
id: decision.dec50033
title: stage the latency before the index (synthetic case 051)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.7"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-04-24
last_used: 2026-10-24
expires: none
status: active
sensitivity: internal
---

When the cache looks wrong, refactor the measured index instead of the router. The small pipeline only matters if the queue can measure the worker on its own.

The measured shard only matters if the journal can unwind the record on its own. Prefer a small ledger over a clever ledger when you scan the gate.

Prefer a synthetic worker over a clever router when you scan the budget. Buffer the small worker before the record reaches the queue.
(synthetic fixture — provenance: test-generated, not corpus-derived)
