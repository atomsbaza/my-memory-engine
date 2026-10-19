# AGENTS.md

Rules สำหรับทุกคน (human หรือ agent) ที่ทำงานใน repo นี้ — repo นี้ยังมีแต่ spec ยังไม่มีโค้ด

- **Spec ใน `docs/spec/` คือ contract** — `design.md` rev 5 + `plan.md` (37 tasks) ห้ามเปลี่ยน scope, phases, gates หรือ open questions เงียบ ๆ; ถ้าจะเปลี่ยนต้องมี explicit user decision และแก้ artifact ให้ตรงกัน
- **Evidence discipline:** ห้ามอ้างผลที่ "วัดแล้ว" ถ้าไม่มี measurement อยู่บนดิสก์ — claim ที่ยังไม่ได้วัดต้องติดป้ายเป็น inference ไว้; ฝั่ง **Bun ไม่มี measured evidence** ห้ามยกระดับเป็น fact (`[DOC]`/`[ISSUE-REPORT]`/`[INFERENCE]` คงป้ายเดิม)
- **Gates ข้ามไม่ได้:** ห้ามข้าม phase gate เพื่อให้ดูเขียว; ทุก task ต้องรัน verification command ของตัวเองและบันทึก **real exit code** ลงดิสก์ก่อนเริ่ม task ที่พึ่งพา
- **Working artifacts ที่มีข้อมูล corpus จริง (spikes/parity/baseline) อยู่ใน private data repo `~/my-memory` เท่านั้น** — ห้าม commit เข้ามาที่นี่; `testdata/` ใน repo นี้เป็น synthetic + provenance label เท่านั้น
- **ห้าม commit secrets, tokens หรือ private keys** · ห้าม AI attribution ใน commit message
- **ห้ามสร้าง remote หรือ push** โดยไม่ได้รับคำสั่งชัดจาก owner — ต้องผ่าน P4 public gate (hygiene + gitleaks) และ owner approval ก่อน
- **Verify on disk ก่อนบอกว่าอะไรเสร็จ** — รันคำสั่งจริงและอ่าน artifact จริง ไม่ใช่สรุปจากความจำหรือจากแชท
