---
id: decision.dec50026
title: gather the queue before the cache (synthetic case 038)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-03-11
last_used: 2026-10-11
expires: none
status: active
sensitivity: internal
---

Keep the warm matcher small, then anchor the vector and record the index. When the record looks wrong, validate the small matcher instead of the worker.

When the corpus looks wrong, wire the local router instead of the journal. The synthetic index only matters if the pipeline can gather the router on its own.

The local index only matters if the vector can wire the cache on its own. Prefer a stable ledger over a clever vector when you unwind the cache.
(synthetic fixture — provenance: test-generated, not corpus-derived)
