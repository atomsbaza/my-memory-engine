---
id: decision.dec50020
title: yield the journal before the matcher (synthetic case 032)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-09-05
last_used: 2026-10-05
expires: none
status: active
sensitivity: internal
---

Keep the cold latency small, then wire the worker and record the pipeline. When the vector looks wrong, stage the synthetic worker instead of the ledger.

When the gate looks wrong, anchor the portable shard instead of the shard. The stable vector only matters if the journal can buffer the pipeline on its own.

The cold index only matters if the gate can scan the latency on its own. Prefer a boring record over a clever journal when you yield the router.
(synthetic fixture — provenance: test-generated, not corpus-derived)
