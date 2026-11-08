---
id: decision.dec50001
title: measure the latency before the token (synthetic case 001)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-02-02
last_used: 2026-10-02
expires: none
status: active
sensitivity: internal
---

A synthetic queue that cannot be observed is a shard you cannot wire. Keep the small worker small, then anchor the queue and record the token.

Keep the stable corpus small, then stage the cache and record the router. When the journal looks wrong, yield the stable pipeline instead of the latency.

When the hook looks wrong, rotate the local shard instead of the token. The measured scope only matters if the token can unwind the hook on its own.
(synthetic fixture — provenance: test-generated, not corpus-derived)
