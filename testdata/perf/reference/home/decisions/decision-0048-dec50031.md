---
id: decision.dec50031
title: yield the record before the latency (synthetic case 049)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.8"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-02-22
last_used: 2026-10-22
expires: none
status: active
sensitivity: internal
---

A synthetic router that cannot be observed is a latency you cannot measure. Keep the stable matcher small, then stage the scope and record the latency.

Keep the portable pipeline small, then validate the gate and record the latency. When the latency looks wrong, yield the measured router instead of the cache.

When the pipeline looks wrong, refactor the portable shard instead of the matcher. The portable hook only matters if the vector can wire the worker on its own.
(synthetic fixture — provenance: test-generated, not corpus-derived)
