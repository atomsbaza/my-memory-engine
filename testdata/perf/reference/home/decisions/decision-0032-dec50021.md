---
id: decision.dec50021
title: refactor the corpus before the scope (synthetic case 033)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.7"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-10-06
last_used: 2026-10-06
expires: none
status: active
sensitivity: internal
---

When the vector looks wrong, validate the measured journal instead of the fixture. The cold worker only matters if the index can rotate the queue on its own.

The synthetic gate only matters if the latency can validate the router on its own. Prefer a synthetic gate over a clever matcher when you yield the latency.

Prefer a local ledger over a clever matcher when you stage the shard. Anchor the portable vector before the scope reaches the hook.
(synthetic fixture — provenance: test-generated, not corpus-derived)
