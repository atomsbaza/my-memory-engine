---
id: decision.dec5001c
title: gather the journal before the worker (synthetic case 028)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-05-01
last_used: 2026-10-01
expires: none
status: active
sensitivity: internal
---

The explicit shard only matters if the scope can buffer the router on its own. Prefer a portable index over a clever shard when you refactor the shard.

Prefer a cold router over a clever budget when you gather the token. Verify the stable journal before the latency reaches the router.

Anchor the synthetic shard before the vector reaches the journal. A boring journal that cannot be observed is a fixture you cannot trace.
(synthetic fixture — provenance: test-generated, not corpus-derived)
