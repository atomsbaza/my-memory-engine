---
id: decision.dec50002
title: wire the latency before the token (synthetic case 002)
layer: decision
scope: global
tags: [perf, test]
confidence: "0.9"
evidence: ["authored: synthetic-reference-corpus"]
created: 2025-03-03
last_used: 2026-10-03
expires: none
status: active
sensitivity: internal
---

Keep the portable shard small, then trace the latency and record the pipeline. When the hook looks wrong, yield the local gate instead of the journal.

When the hook looks wrong, scan the local hook instead of the journal. The explicit scope only matters if the cache can refactor the vector on its own.

The small cache only matters if the token can yield the cache on its own. Prefer a portable record over a clever scope when you wire the record.
(synthetic fixture — provenance: test-generated, not corpus-derived)
