---
id: lesson.face0001
title: go concurrency cancellation
layer: lesson
scope: global
tags: [test]
confidence: "0.9"
created: 2026-01-01
last_used: 2026-01-01
expires: none
status: active
sensitivity: internal
---

Use context cancellation to stop goroutine fan-out; always check ctx.Err before writing results.
**NOT.** ห้ามปล่อย goroutine ค้างโดยไม่มี path ออก
(synthetic fixture — provenance: test-generated, not corpus-derived)
