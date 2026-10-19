# my-memory-engine

Public-ready engine repo สำหรับ **my-memory agent-memory framework** — rewrite engine ของ private data repo `~/my-memory` ให้ไม่ใช่ Python อีกต่อไป

> **สถานะปัจจุบัน: มีแต่ spec ยังไม่มีโค้ด** — repo นี้เก็บ design + plan เท่านั้น ยังไม่มีการ implement, ไม่มี remote, ไม่มีอะไรถูก push

## Status

- **Design:** rev 5 — ผ่าน independent architecture gate แล้ว (**0 BLOCKER / 0 MAJOR / 1 NIT** ซึ่งแก้แล้ว)
- **Implementation plan:** 37 tasks (Task 0 baseline + P0 13 + P1 7 + P2 7 + P3 6 + P4 3) — ทุก task มี command, expected exit code และ gate ที่ปิด
- **Implementation:** ยังไม่เริ่ม (0 บรรทัดโค้ด) · **no remote** · **nothing pushed**
- **Language:** ยังไม่ตัดสิน — อยู่ใน P0 language gate

## Read order

1. [`docs/spec/diff-summary.md`](docs/spec/diff-summary.md) — ภาพรวมว่ามีอะไรอยู่ที่ไหน + decision landscape
2. [`docs/spec/plan.md`](docs/spec/plan.md) — 37 tasks, gates, verification commands
3. [`docs/spec/design.md`](docs/spec/design.md) — design rev 5 (contract)
4. [`docs/spec/research.md`](docs/spec/research.md) · [`docs/spec/research-bun-addendum.md`](docs/spec/research-bun-addendum.md) · [`docs/spec/exploration.md`](docs/spec/exploration.md) — research + current-state exploration

## Phase / gate model (5 บรรทัด)

- **P0** = pre-code feasibility spikes + language gate — Go เป็น *leading conditional* · Bun + TypeScript เป็น *first-class candidate* · Rust เป็น *fallback*; ตัดสินจากผลที่ **วัดจริงและลงดิสก์** เท่านั้น
- **P1** = local-only engine scaffold (exit gate: S3/S4 parity)
- **P2** = domain + CLI + MCP + hooks (exit gate: S5)
- **P3** = staged cutover ทีละ harness (6 ตัว) พร้อม rollback ได้
- **P4** = archive + public gate (hygiene/gitleaks + owner approval ก่อนมี remote)

## ยังเป็น gate / ยังเป็นสิทธิ์ของ owner

- **OQ1 license — ยังไม่เลือก จึงยังไม่มีไฟล์ `LICENSE` โดยเจตนา**
- **OQ2 ชื่อ repo = `my-memory-engine`** (ตัดสิน 2026-10-19) แต่ **module path / package name ยังรอ P0 language gate**
- **ภาษา** ยังไม่เลือก (Go / Bun+TypeScript / Rust)
- Design ยังเป็น `DRAFT — รอ user review/approval` — **architect APPROVE ไม่เท่ากับ user approval**
- Remote creation / push / publication ต้องมีคำสั่งชัดจาก owner เท่านั้น

## Data boundary

- Private data repo (`~/my-memory`) เป็นที่อยู่ของ records และ working artifacts ทุกชิ้นที่มีข้อมูล corpus จริง (`$WF` — spikes, parity, baseline) — artifacts พวกนี้ **ห้ามเข้า repo นี้**
- **Repo นี้ห้ามมี real corpus, prompt log, personal records หรือ secrets** — ใช้ synthetic fixtures พร้อม provenance label เท่านั้น
- หมายเหตุตรง ๆ: spec ที่อยู่ใน `docs/spec/` ยังมี absolute private path และรายละเอียดส่วนตัวอื่น ๆ อยู่ — **ต้อง scrub ก่อน public push ทุกครั้ง**; gate ที่บังคับเรื่องนี้คือ gitleaks/hygiene gate ที่ P4 (พร้อม manual review ของทุก fixture)
- **ห้ามสร้าง remote หรือ push** โดยไม่มีคำสั่งชัดจาก owner — P4 public gate + owner approval เป็นเงื่อนไขขั้นต่ำ
