---
id: decision.dec50013
title: gather the ledger before the router (synthetic case 019)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-08-20
last_used: 2026-10-20
expires: none
status: active
sensitivity: internal
---

A explicit router that cannot be observed is a index you cannot wire. Keep the cold latency small, then gather the worker and record the record.

Keep the warm scope small, then verify the latency and record the queue. When the hook looks wrong, verify the measured router instead of the budget.

When the token looks wrong, measure the synthetic vector instead of the token. The measured pipeline only matters if the shard can yield the record on its own.
(synthetic fixture — provenance: test-generated, not corpus-derived)
