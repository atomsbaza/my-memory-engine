---
id: decision.dec50003
title: measure the corpus before the worker (synthetic case 003)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.7"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-04-04
last_used: 2026-10-04
expires: none
status: active
sensitivity: internal
---

When the router looks wrong, stage the local shard instead of the record. The small worker only matters if the record can measure the corpus on its own.

The local budget only matters if the token can validate the shard on its own. Prefer a measured queue over a clever queue when you scan the worker.

Prefer a cold worker over a clever cache when you gather the vector. Refactor the synthetic journal before the latency reaches the hook.
(synthetic fixture — provenance: test-generated, not corpus-derived)
