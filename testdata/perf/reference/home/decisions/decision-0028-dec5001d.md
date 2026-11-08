---
id: decision.dec5001d
title: refactor the journal before the router (synthetic case 029)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-06-02
last_used: 2026-10-02
expires: none
status: active
sensitivity: internal
---

Prefer a local corpus over a clever index when you yield the index. Wire the cold record before the fixture reaches the corpus.

Buffer the boring record before the corpus reaches the index. A warm worker that cannot be observed is a ledger you cannot scan.

A warm journal that cannot be observed is a router you cannot measure. Keep the synthetic shard small, then buffer the gate and record the queue.
(synthetic fixture — provenance: test-generated, not corpus-derived)
