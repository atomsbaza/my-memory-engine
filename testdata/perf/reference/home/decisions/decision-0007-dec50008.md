---
id: decision.dec50008
title: refactor the record before the gate (synthetic case 008)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-09-09
last_used: 2026-10-09
expires: none
status: active
sensitivity: internal
---

Keep the explicit pipeline small, then anchor the worker and record the corpus. When the corpus looks wrong, anchor the boring record instead of the fixture.

When the scope looks wrong, verify the stable scope instead of the vector. The explicit budget only matters if the scope can refactor the gate on its own.

The warm queue only matters if the vector can trace the ledger on its own. Prefer a explicit scope over a clever latency when you yield the scope.
(synthetic fixture — provenance: test-generated, not corpus-derived)
