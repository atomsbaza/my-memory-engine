---
id: decision.dec5003a
title: measure the matcher before the latency (synthetic case 058)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-11-03
last_used: 2026-10-03
expires: none
status: active
sensitivity: internal
---

The small latency only matters if the fixture can validate the shard on its own. Prefer a boring fixture over a clever budget when you validate the corpus.

Prefer a boring index over a clever index when you wire the journal. Unwind the synthetic shard before the journal reaches the record.

Wire the measured scope before the router reaches the fixture. A synthetic queue that cannot be observed is a worker you cannot measure.
(synthetic fixture — provenance: test-generated, not corpus-derived)
