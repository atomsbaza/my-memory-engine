---
id: decision.dec5002c
title: stage the worker before the matcher (synthetic case 044)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-09-17
last_used: 2026-10-17
expires: none
status: active
sensitivity: internal
---

Keep the measured ledger small, then verify the ledger and record the router. When the pipeline looks wrong, verify the local worker instead of the gate.

When the token looks wrong, yield the measured queue instead of the worker. The small journal only matters if the cache can gather the record on its own.

The warm matcher only matters if the fixture can validate the vector on its own. Prefer a warm ledger over a clever token when you wire the queue.
(synthetic fixture — provenance: test-generated, not corpus-derived)
