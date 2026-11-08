---
id: decision.dec5001a
title: stage the queue before the router (synthetic case 026)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-03-27
last_used: 2026-10-27
expires: none
status: active
sensitivity: internal
---

Keep the boring router small, then measure the token and record the worker. When the latency looks wrong, unwind the portable matcher instead of the token.

When the pipeline looks wrong, gather the explicit matcher instead of the journal. The local worker only matters if the matcher can stage the pipeline on its own.

The portable router only matters if the worker can rotate the token on its own. Prefer a small hook over a clever worker when you wire the router.
(synthetic fixture — provenance: test-generated, not corpus-derived)
