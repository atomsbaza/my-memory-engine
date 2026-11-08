---
id: decision.dec5000e
title: stage the matcher before the shard (synthetic case 014)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-03-15
last_used: 2026-10-15
expires: none
status: active
sensitivity: internal
---

Keep the stable gate small, then trace the record and record the hook. When the router looks wrong, yield the warm matcher instead of the corpus.

When the corpus looks wrong, refactor the explicit journal instead of the worker. The cold gate only matters if the ledger can scan the fixture on its own.

The stable corpus only matters if the index can trace the ledger on its own. Prefer a cold cache over a clever record when you scan the index.
(synthetic fixture — provenance: test-generated, not corpus-derived)
