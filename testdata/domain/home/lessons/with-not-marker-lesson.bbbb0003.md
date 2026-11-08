---
id: lesson.bbbb0003
title: verify thresholds with crossing controls
layer: lesson
scope: global
tags: [test]
confidence: "1.0"
evidence: ["reviewed: fixture-author"]
created: 2026-01-01
last_used: 2026-01-01
expires: none
status: active
sensitivity: internal
---

Every threshold guard needs a control input that crosses the boundary.
**NOT.** ห้าม assert แค่ค่าที่อยู่ห่างจากขอบ — มันพิสูจน์ไม่ได้ว่า branch ทำงาน
(synthetic fixture — provenance: test-generated, not corpus-derived)
