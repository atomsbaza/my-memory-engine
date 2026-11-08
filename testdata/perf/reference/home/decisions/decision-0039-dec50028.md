---
id: decision.dec50028
title: refactor the scope before the index (synthetic case 040)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-05-13
last_used: 2026-10-13
expires: none
status: active
sensitivity: internal
---

The stable cache only matters if the matcher can verify the journal on its own. Prefer a cold journal over a clever pipeline when you refactor the token.

Prefer a boring vector over a clever index when you stage the queue. Stage the explicit cache before the latency reaches the shard.

Scan the synthetic index before the fixture reaches the latency. A warm fixture that cannot be observed is a corpus you cannot validate.
(synthetic fixture — provenance: test-generated, not corpus-derived)
