# Spec: my-memory engine framework (product level)

**Status:** DRAFT — awaiting approval
**Created:** 2026-10-19
**Active spec:** [`specs/framework.spec.md`](../specs/framework.spec.md)
**Requested by:** repo owner (`my-memory-engine`)

## What this is

Pointer ตาม convention ของ `spec-writer`: สเปกที่ active คือ `specs/framework.spec.md` (product-level spec ของ engine framework — 25 functional + 12 non-functional requirement, traceability ไป `docs/spec/design.md` rev 5 และ `docs/spec/plan.md`).

## Gate rule

**ห้ามเริ่ม implementation ใด ๆ ก่อน owner เปลี่ยนสถานะสเปกเป็น `APPROVED`** — การอนุมัติ = แก้ `**Status:**` ในไฟล์นี้ (และ mirror ใน `specs/framework.spec.md`) เป็น `APPROVED` พร้อมวันที่

Optional enforcement (owner ตัดสินเองว่าจะ wire หรือไม่): `touch .claude/spec-gate-active` + `PreToolUse` hook ใน `.claude/settings.json` ตาม `spec-writer` skill — **ยังไม่ได้ wire** ใน repo นี้

## Next step

Owner อ่าน `specs/framework.spec.md` §12 (approval checklist) → ตอบ/ยืนยัน OQ1–OQ7 ตาม gate ของแต่ละข้อ → mark `APPROVED`; หลังจากนั้น phase specs + traceability matrix (follow-up dispatch) และ implementation (เริ่มที่ plan Task 0) จึงเริ่มได้

---

<!-- To approve: change Status above to APPROVED and mirror it in specs/framework.spec.md -->
