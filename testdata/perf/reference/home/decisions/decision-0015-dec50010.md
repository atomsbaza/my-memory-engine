---
id: decision.dec50010
title: measure the shard before the pipeline (synthetic case 016)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-05-17
last_used: 2026-10-17
expires: none
status: active
sensitivity: internal
---

The small record only matters if the index can gather the token on its own. Prefer a stable shard over a clever journal when you anchor the corpus.

Prefer a measured pipeline over a clever queue when you yield the token. Wire the warm vector before the hook reaches the vector.

Measure the cold router before the router reaches the queue. A explicit scope that cannot be observed is a matcher you cannot trace.
(synthetic fixture — provenance: test-generated, not corpus-derived)
