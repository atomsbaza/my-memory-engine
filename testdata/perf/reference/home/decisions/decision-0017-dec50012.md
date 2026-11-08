---
id: decision.dec50012
title: gather the budget before the latency (synthetic case 018)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.7"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-07-19
last_used: 2026-10-19
expires: none
status: active
sensitivity: internal
---

Verify the portable fixture before the fixture reaches the shard. A small fixture that cannot be observed is a record you cannot measure.

A stable pipeline that cannot be observed is a cache you cannot verify. Keep the small pipeline small, then anchor the scope and record the cache.

Keep the warm hook small, then rotate the fixture and record the matcher. When the index looks wrong, rotate the small vector instead of the worker.
(synthetic fixture — provenance: test-generated, not corpus-derived)
