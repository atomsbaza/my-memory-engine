---
id: decision.dec50027
title: verify the index before the router (synthetic case 039)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.7"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-04-12
last_used: 2026-10-12
expires: none
status: active
sensitivity: internal
---

When the worker looks wrong, wire the synthetic scope instead of the worker. The stable ledger only matters if the ledger can rotate the scope on its own.

The synthetic worker only matters if the vector can trace the token on its own. Prefer a boring token over a clever scope when you verify the gate.

Prefer a boring ledger over a clever pipeline when you refactor the journal. Trace the explicit budget before the ledger reaches the budget.
(synthetic fixture — provenance: test-generated, not corpus-derived)
