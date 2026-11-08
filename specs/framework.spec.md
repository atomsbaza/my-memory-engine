---
status: active
owner: opencode
stage: SPEC
updated: 2026-10-19
---

# Spec: my-memory engine framework (product level)

## TL;DR

สเปกนี้บอกว่า engine ของ **my-memory agent-memory framework** ต้องทำอะไรและรู้ได้อย่างไรว่าเสร็จ: **25 functional requirement + 12 non-functional requirement** ที่สืบจาก `docs/spec/design.md` rev 5 และ `docs/spec/plan.md` (37 tasks) แบบ traceability เต็มทุกข้อ
ไม่มีข้อใด re-design หรือ re-plan — design/plan คือ contract; สเปกนี้คือ "acceptance surface" ที่ phase spec, acceptance criterion, test skeleton และ gate จะอ้างอิงกลับมาที่ id ในไฟล์นี้

---

## 1. Problem

agent ทุกตัวของผู้ใช้ (6 harness) จำสิ่งเดิมซ้ำ ๆ ไม่ได้ และความจำที่จำได้ก็กระจายอยู่ตาม config/skill ของแต่ละ harness — ที่มีอยู่คือ engine Python ไฟล์เดียว 2,171 บรรทัด [OBS: `~/my-memory/bin/mem`] ซึ่งผูกกับ runtime Python, กลืน error บางจุด (silent commit failure), เขียนไฟล์แบบไม่ atomic และแชร์ index เดียวกับ implementation อื่นไม่ได้ **สำหรับ repo owner / maintainer (solo)** ปัญหาคือ "(ก) จะแจกจ่ายให้คนอื่นใช้ได้โดยไม่ต้องมี Python และ (ข) จะย้ายมา engine ใหม่โดยไม่ทำข้อมูลที่ใช้งานจริง (หลักร้อย records — ตัวเลขนับตาม GAP-7) เสียหายหรือเปลี่ยนพฤติกรรมที่ harness พึ่งอยู่" **สำหรับ agent harness** ปัญหาคือสัญญาเดิม (MCP 9 tools, hook 4 formats) ต้องไม่แตก **สำหรับ outside contributor** ปัญหาคือต้องมี engine repo สาธารณะที่ build/test/review ได้โดยไม่ต้องเข้าถึงข้อมูลส่วนตัวของผู้ใช้เลย

## 2. Actors / users

| Actor | ต้องการอะไร | ข้อจำกัดที่ผูกกับ actor นี้ |
|---|---|---|
| **Repo owner / maintainer** (solo) | engine ที่ build/verify ได้ในเครื่อง, ตัดสินภาษาและ license ได้เอง, rollback ได้ราย harness | เวลาเป็น constraint หลัก (K6) · เป็นเจ้าของ OQ1–OQ7 ทุกข้อ · เป็นผู้ sign-off ทุก gate |
| **Agent harnesses** (claude, codex, gemini, opencode, cursor, hermes — 6 ตัวคือ cutover scope) | MCP 13 tools + hook 4 output formats เดิม โดยไม่ต้องแก้ config ที่ติดตั้งแล้ว | hook ต้อง `exit 0` เสมอและห้าม push (K7) · stdout ต้องสะอาด (gemini ต้องได้ JSON บริสุทธิ์) |
| **Outside contributor** | อ่าน/รัน/ทดสอบ engine ได้จาก repo สาธารณะโดยไม่ต้องมีข้อมูลจริง | เห็นได้เฉพาะ code + schema defaults + synthetic fixtures + docs · ห้ามเข้าถึง corpus/prompt log/record จริง |
| **Owner ในฐานะผู้ใช้ข้อมูล** | local-first เต็มรูปแบบ, sync เป็น opt-in, secrets อยู่ในเครื่อง | default path ห้ามมี network call (K2) |

## 3. Scope ของสเปกนี้ และความสัมพันธ์กับ design/plan

- **สเปกนี้ owns:** requirement statement (EARS), traceability ไป design section + plan task, verification hint ระดับ "สังเกตได้อย่างไร", capability map, trust boundary, glossary, open questions/gaps, approval state
- **สเปกนี้ไม่ owns:** architecture decision (design §3–§5), task ลำดับ/command/expected exit code (plan), acceptance criterion + test skeleton ระดับ requirement (phase spec ใน follow-up dispatch), gate verdict (on-disk evidence)
- **ถ้า spec ขัดกับ design/plan:** design/plan ชนะ และ spec ต้องแก้ — ห้ามแก้ design ให้เข้ากับ spec เงียบ ๆ (AGENTS.md)
- **สถานะ implementation:** ยังไม่มีโค้ดใน repo นี้ (0 บรรทัด), ไม่มี remote, ไม่มีอะไรถูก push [verified 2026-10-19]

## 4. Capability map (user-visible surface)

### 4.1 ตาราง surface + จำนวนที่นับได้จริง

| Surface | จำนวน | นับจากอะไร | หมายเหตุ |
|---|---|---|---|
| **CLI legacy subcommands** | **18** | `grep -n 'add_parser(' ~/my-memory/bin/mem` ที่ data repo HEAD `2333e53` (2026-10-19) | `context, search, list, show, add, touch, cite, review, supersede, verify, reindex, render, sync, import, hook, stats, eval, mcp` — ตรงกับ exploration §1 และ design §2.3 K4 |
| **CLI ที่ design เพิ่ม** | **+2 → 20** | design §3.2 ("18 subcommands เดิม + init + doctor") · §6.1 cli row | `init` = scaffold memory home ใหม่สำหรับผู้ใช้ OSS · `doctor` = file↔index↔remote consistency — **ยังไม่มีในโค้ดปัจจุบัน** โดยเจตนา (เป็นงาน P2.2) |
| **MCP tools** | **9** | `MCP_TOOLS` ที่ `bin/mem:1744` | `memory_context, memory_search, memory_show, memory_cite, memory_add, memory_list, memory_verify, memory_stats, memory_sync` — ตรงกับ design K5 (แก้ doc เดิมที่เขียน 8) |
| **Hook output formats** | **4** | `hook_emit` (`bin/mem:1320–1345`) | `text` (claude/codex) · `json` (`hookSpecificOutput.additionalContext`, gemini) · `hermes` (`{"context"}`) · `cursor` (`{"additional_context"}` + `continue:true`) |
| **Hook events** | **3** | `cmd_hook` (`bin/mem:1348–1420`) | `session-start` (budget 2500) · `user-prompt` (budget 1400, เงียบถ้า content token < 2) · `stop` (reindex + local commit เท่านั้น — ไม่ push) |
| **Cutover harness targets** | **6** | design §2.1 G-6 · §9.3 P3 · plan P3.1–P3.6 | claude, codex, gemini, opencode, cursor, hermes |
| **Agent registry entries** | **7** | `schema/agents.json` (data repo) | เพิ่ม `kirocrew` (mode `pull`) เกิน 6 cutover targets — ดู GAP-6; registry เป็น data-driven ไม่ใช่ scope ของ cutover |
| **Record layers** | **7** | `schema/taxonomy.json` | `rule, project, lesson, decision, handoff, knowledge` (one-per-*, canonical markdown) + `ops` (append-only, ไม่ใช่ record ต่อ fact) |
| **Derived index** | **1 ไฟล์ต่อ engine** | design §5.2 · §6.1 index row · plan P1.5 | `.memory/index-<engine>.sqlite` + `schema_version` — ห้ามทับ `index.sqlite` ของ Python |
| **Sync transport** | **1** (MVP) | design §5.2 · §6.1 sync row | git CLI subprocess เท่านั้น; provider adapter/object store = เฟส 2 |
| **Installers** | **3** | design §6.1 harness row | `mem-hook.sh`, `install.sh` (gated), opencode plugin (JS) |
| **Canonical records (ปัจจุบัน)** | **206 ไฟล์** ตอน HEAD `2333e53` | file count ใน 6 layers: lessons 149 · rules 6 · decisions 18 · knowledge 21 · handoffs 6 · projects 6 | plan Task 0 บันทึก **201 records / 173 active / 28 superseded** ที่ `d0d311d`; design §3.1 เขียน "~160 records" — ตัวเลขคนละเวลา/คนละฐานการนับ ดู GAP-7 |

### 4.2 ตัวเลข metric ที่ห้ามเอามาปนกัน

| Metric | ค่า | ที่มา | ใช้อย่างไร |
|---|---|---|---|
| **Assertion sites** ใน suite เดิม | **187** | `exploration.md` §1/§6.1 (grep count, 2026-10-17) | ใช้เป็น **inventory** ของพฤติกรรมที่ต้อง port — ไม่ใช่ pass count |
| **Runtime pass count** ของ suite เดิม | **399 passed / 0 failed, exit 0** | `plan.md` Task 0 (วัดจริง 2026-10-19, data repo `main` @ `d0d311d`) | ใช้เป็น **drift check**: run ใหม่แล้ว printed `N` ต้องเท่ากับที่บันทึก; ไม่ตรง = หยุด ไม่ใช่แก้ baseline ให้เขียว |
| **Re-count วันนี้** | 352 assertion-helper call sites (`chk`/`has`/`hasre`/`lacks`), `chk` 219 ครั้ง | grep บน `tests/test-mem.sh` @ `2333e53` (2026-10-19) | พิสูจน์ว่า **นิยาม metric ยังไม่ถูก pin** และไฟล์โตขึ้นหลัง exploration → GAP-5 |

**กฎ:** ห้ามเขียน "187 tests", ห้ามเขียน "399 assertions" และห้ามใช้ตัวเลขใดแทน contract — contract คือ "suite exit 0 และ `0 failed`" ส่วนจำนวนเป็น drift check ที่ต้องบันทึกพร้อมวันที่/SHA

## 5. Functional requirements (25)

รูปแบบ: **EARS** → **Trace** (design section · plan task) → **Verify** (สังเกตได้อย่างไร ไม่ใช่ AC ของ phase spec)

### 5.1 Records & format (8)

#### REQ-REC-01 · Canonical record = 1 markdown file + frontmatter ต่อ 1 fact

**EARS**
- THE SYSTEM SHALL persist each memory fact as exactly one markdown file with YAML-subset frontmatter inside the canonical git working tree rooted at `MEM_HOME`.
- THE SYSTEM SHALL NOT treat the derived index, runtime log, or any cache as a source of record bytes.

**Trace:** `design.md` §2.3 K1 · §5.2 (LocalStore row) · §6.1 (codec, store) · `plan.md` P1.2, P1.4
**Verify:** `mem verify` = `0 problems` บน fixture home; จำนวนไฟล์ต่อ layer เท่ากับที่ `mem stats` รายงาน; ลบ `.memory/` แล้วไม่มี fact หาย

#### REQ-REC-02 · Byte-exact codec

**EARS**
- WHEN a record is parsed and re-serialised without semantic change, THE SYSTEM SHALL reproduce the same bytes: frontmatter keys in the fixed `FRONT_ORDER`, the same quoting decisions, inline vs block list preserved, unknown keys appended after known keys, body bytes unchanged.
- WHEN a corpus-wide round-trip completes, THE SYSTEM SHALL leave `git diff` empty on the test clone.

**Trace:** `design.md` §3.3 step 3 · §6.1 codec row · §9.2 ข (S3) · `plan.md` P1.2, P1.7
**Verify:** private parity runner `--gate S3` บน fresh real clone → `git diff --exit-code` ว่าง; เปลี่ยน quoting 1 จุดใน control → gate คืน non-zero

#### REQ-REC-03 · Stable record IDs

**EARS**
- THE SYSTEM SHALL derive a record id as `<layer>.<sha1("{layer}|{scope}|{title}|{body}")[:8]>`, where `title` is derived from the first body line when not supplied.
- THE SYSTEM SHALL NOT change any id that already exists in the corpus.

**Trace:** `design.md` §6.1 codec row (id formula) · §9.2 ข (S4) · §2.3 K8 · `plan.md` P1.2, P1.7
**Verify:** S4 recompute ทุก record บน real clone = `matched/total = 100%`; แก้ input 1 field (title) ใน control → gate คืน non-zero

#### REQ-REC-04 · Schema enforcement

**EARS**
- WHEN a record is written or verified, THE SYSTEM SHALL validate it against the active home's `schema/record.schema.json` and `schema/taxonomy.json` — required fields, `id`/`scope`/`status`/`sensitivity` enums, layer↔directory mapping, tag membership — where engine defaults are overridable by the home's own `schema/`; an unknown tag SHALL be reported as a warning rather than a hard failure, matching the baseline.
- THE SYSTEM SHALL include the fields the current engine writes but the schema omits (`superseded_by`, `uses`) and SHALL stamp/report `schema_version`.
- WHEN a `schema_version` is unsupported, THE SYSTEM SHALL refuse rather than overwrite.

**Trace:** `design.md` §6.1 schema row (S1/S2 fixes) · §9.5 · `plan.md` P1.3
**Verify:** valid synthetic fixture ทุก 7 layer ผ่าน; fixture ที่ผิด field/type/layer/dir/required ถูก reject พร้อมชื่อ field; version mismatch → refuse (ไม่เขียนทับ)

#### REQ-REC-05 · Write-time hygiene (guards + dedup + cross-layer)

**EARS**
- WHEN an add is requested, THE SYSTEM SHALL run guards before any write: secret scan and absolute-path scan → exit 2; near-duplicate check against active records of the same layer and scope → `jaccard ≥ 0.88` blocks with exit 3, band `0.60–0.88` warns and continues; for `lesson` adds, a `knowledge` record at `≥ 0.60` similarity produces a canonical-pointer warning.
- WHEN an explicit override (`--force` / `--supersede` / `--supersedes`) is supplied, THE SYSTEM SHALL treat the duplicate as intentional and continue instead of blocking.
- WHEN an add carries `source: external:*` without `--evidence`, THE SYSTEM SHALL reject it as untrusted input.
- THE SYSTEM SHALL NOT persist a record that fails a blocking guard.

**Trace:** `design.md` §3.3 step 2 · §6.1 domain row · `plan.md` P2.1
**Verify:** domain tests ที่ทั้งสองข้างของทุก threshold (0.879 ผ่าน / 0.881 block) และ exit code 2/3 ตรง; ลบ guard แล้ว test ต้องแดง

#### REQ-REC-06 · Supersede semantics

**EARS**
- WHEN a fact is superseded, THE SYSTEM SHALL keep both files, set the old record to `status: superseded` with `superseded_by`, and the new record to `supersedes: <old-id>`.
- THE SYSTEM SHALL NOT delete or rewrite the superseded body.

**Trace:** `design.md` §6.1 domain row · `plan.md` P2.1
**Verify:** supersede chain test: 2 ไฟล์ยังอยู่, links ถูกทั้งสองทิศ, `stats` นับ superseded เพิ่ม 1

#### REQ-REC-07 · Quarantine

**EARS**
- WHEN an untrusted (`external:*`) fact is accepted, THE SYSTEM SHALL store it with `sensitivity: quarantine` and SHALL exclude quarantined records from every injection/recall path (context, hook recall, render, default search).
- THE SYSTEM SHALL release a quarantined record only through explicit review with evidence, which sets `sensitivity: internal`.
- THE SYSTEM SHALL NOT accept a record claiming `quarantine` without an `external:*` source.

**Trace:** `design.md` §6.1 domain row (review = ทางออก quarantine เดียว) · §3.3 step 2 (quarantine rules คงเดิม) · `plan.md` P2.1
**Verify:** exclusion test ครบทุก path (context/recall/render/search) + review-only unlock + reject-by-default; ถอด filter 1 path → test แดง

#### REQ-REC-08 · Expiry (bi-temporal `invalid_at` อยู่นอก scope)

**EARS**
- WHEN a record carries `expires`, THE SYSTEM SHALL normalise it to ISO form (`none` / `YYYY-MM-DD` / `+Nd` / `+Nw` / `+Nm`) and treat an expired record as expired in scoring and in handoff selection.
- THE SYSTEM SHALL NOT implement bi-temporal `invalid_at` in this scope.

**Trace:** `design.md` §6.1 domain row (expires normalization) · §2.1 G-2 · `plan.md` P2.1
**Verify:** `expires` normalization table test (ทุก input form) + expired handoff ไม่ถูก `pick_for_query` เลือก

### 5.2 Retrieval / domain lifecycle (1)

#### REQ-DOM-01 · Scoring, selection, render, cite

**EARS**
- WHEN retrieval runs, THE SYSTEM SHALL score with the documented additive formula and configured weights (`w_conf`, `w_recency`, `tau_days`, `w_uses`, `threshold_hook` from taxonomy, overridable per call for eval) and SHALL use the same select/`pick_for_query` path for CLI context, hook recall, and eval.
- WHEN `pick_for_query` runs, THE SYSTEM SHALL consider `lesson`/`decision`/`handoff` only, drop expired handoffs, dedupe by id, and return at most `limit` records at or above the threshold.
- WHEN `render` runs, THE SYSTEM SHALL regenerate the pointer/render block inside the registered agent file for every agent whose mode is `render` or `pointer`, within that agent's budget, and in `pointer` mode SHALL emit a pointer rather than a copy of record bodies.
- WHEN an agent's mode is `pull` or `tool`, THE SYSTEM SHALL skip it and report the skip instead of overwriting that surface.
- WHEN `cite` runs, THE SYSTEM SHALL increment `uses` under an exclusive lock and refresh the index.

**Trace:** `design.md` §6.1 domain row · §3.3 · `plan.md` P2.1, P2.6
**Verify:** domain unit tests ต่อ scoring term + budgets; `cite` concurrent test ไม่ทำ `uses` หาย; S5 eval ใช้ path เดียวกับ hook

### 5.3 CLI & MCP (3)

#### REQ-CLI-01 · Command surface

**EARS**
- THE SYSTEM SHALL expose the 18 legacy command families (`context, search, list, show, add, touch, cite, review, supersede, verify, reindex, render, sync, import, hook, stats, eval, mcp`) plus `init` and `doctor`.
- WHEN `init` runs, THE SYSTEM SHALL scaffold a usable memory home for a new user without touching an existing one.
- WHEN `doctor` runs, THE SYSTEM SHALL report file↔index↔remote consistency.

**Trace:** `design.md` §3.2 (18 + init + doctor) · §6.1 cli row · `plan.md` P2.2
**Verify:** command manifest test ยืนยัน 18 legacy + `init` + `doctor`; `init` บน temp HOME แล้ว `verify` = 0 problems; `doctor` ตรวจจับ index ขาด/remote ไม่ตั้ง

#### REQ-CLI-02 · Exit codes + machine-readable output

**EARS**
- THE SYSTEM SHALL use the documented exit-code contract: `0` success, `1` check/eval failure, `2` unsafe input (secret / absolute path), `3` guard block (duplicate / missing screening).
- WHEN `--json` is requested on a command that supports it (`stats --json`, `eval --json`), THE SYSTEM SHALL write a single parseable JSON document to stdout and SHALL NOT interleave human text.

**Trace:** `design.md` §3.3 step 5 (1/2/3 เดิม) · §6.1 cli row · `plan.md` P2.2
**Verify:** integration tests จับ exit code ต่อกรณี; `stats --json`/`eval --json` parse ด้วย JSON parser สำเร็จทุกบรรทัด

#### REQ-MCP-01 · MCP contract (13 tools, additive-only, clean stdio)

**EARS**
- WHEN an MCP client connects over stdio, THE SYSTEM SHALL serve exactly the 13 frozen tools (`memory_context, memory_search, memory_show, memory_cite, memory_add, memory_list, memory_verify, memory_stats, memory_sync, memory_archive, memory_promote, memory_compact, memory_trace`) with unchanged names and arguments, an `initialize` handshake carrying the existing `instructions` and protocolVersion negotiation, plus `tools/list` and `tools/call`.
- THE SYSTEM SHALL keep stdout protocol-pure: no banner, warning, or log line outside protocol frames.
- THE SYSTEM SHALL change tool schemas only additively within this scope.

**Trace:** `design.md` §2.3 K5 · §3.2 · §6.1 mcp row · §8.1 (harness↔mem) · `plan.md` P0.2 (probe), P2.3
**Verify:** `tools/test-mcp-contract.sh` diff `tools/list` กับ frozen snapshot = 13 tools เท่าเดิม; stdout ทุกบรรทัด parse เป็น protocol frame; 6-client matrix ผ่านตาม G7

### 5.4 Hooks & installers (3)

#### REQ-HOOK-01 · Hook formats and events

**EARS**
- WHEN a harness fires `session-start`, `user-prompt`, or `stop`, THE SYSTEM SHALL emit output in that harness's declared format — `text`, `json`, `hermes`, or `cursor` — within the documented budgets (2500 for session-start, 1400 for user-prompt).
- THE SYSTEM SHALL extract the query/session from that harness's own payload shape (per-harness adapter keys), not from a single shared key.

**Trace:** `design.md` §3.2 · §6.1 hook row · §2.3 K5 · `plan.md` P2.4
**Verify:** format snapshot tests ครบทั้ง 4 formats ครอบทุก harness entry ที่ประกาศ hook; gemini output parse ด้วย strict JSON; payload จากแต่ละ harness ถูก extract (ไม่ใช่ key เดียวใช้ร่วม)

#### REQ-HOOK-02 · Fail-open with observability

**EARS**
- WHEN any hook event runs — including when the engine binary, git, the log path, or the index is broken — THE SYSTEM SHALL exit `0`.
- THE SYSTEM SHALL NOT push and SHALL NOT make a network call from a hook.
- WHEN a hook fails internally, THE SYSTEM SHALL append one line to the gitignored runtime log under `.memory/` and SHALL NOT write that failure into a committed `ops/` ledger.

**Trace:** `design.md` §2.3 K7 · §3.1 (failure modes) · §3.3 step 4 · §3.2 note (runtime log ≠ ops ledger) · `plan.md` P2.4
**Verify:** fault injection (missing engine / unwritable log / git failure) → exit 0 ทุกกรณี + มี log line; fake git นับ push = 0

#### REQ-INST-01 · Installer safety

**EARS**
- WHEN the installer runs without an apply flag, THE SYSTEM SHALL print the planned changes only (dry-run) and SHALL NOT modify any harness file.
- WHEN apply is explicitly requested (`--yes` or interactive TTY confirmation), THE SYSTEM SHALL write a `.bak-<timestamp>` backup per changed file, be idempotent on re-run, and refuse to operate on a linked worktree.
- THE SYSTEM SHALL NOT modify a harness configuration that was not selected.

**Trace:** `design.md` §6.1 harness/installers row (gated `--yes`/TTY + `.bak-<ts>`, คงพฤติกรรมเดิม) · `plan.md` P2.4 (sandbox dry-run/apply/idempotency/backup)
**Verify:** HOME-sandbox test: dry-run ไม่แตะไฟล์, apply สร้าง `.bak-<ts>`, run ซ้ำไม่เพิ่ม backup; linked worktree ถูก refuse; real `$HOME` ไม่ถูกแตะใน test

### 5.5 Store & index (5)

#### REQ-STORE-01 · `MEM_HOME` resolution + data/engine separation

**EARS**
- WHEN resolving the memory home, THE SYSTEM SHALL check `MEM_HOME`, then `MY_MEMORY_HOME`, then walk upward for `schema/taxonomy.json`, then the documented default, and SHALL fail loudly with a diagnosable error if none resolves.
- THE SYSTEM SHALL NOT embed a machine-specific absolute path (e.g. a home directory containing a real user name) in the binary, defaults, docs, or repo files; a documented `~/`-relative default is permitted.

**Trace:** `design.md` §6.1 store row (R7) · §7 item 2 · `plan.md` P1.4
**Verify:** resolution-order tests (แต่ละ env/None) + missing home → non-zero พร้อมข้อความ; `grep` absolute path ใน binary/`schema/` = 0

#### REQ-STORE-02 · Scan rules and discovery

**EARS**
- WHEN scanning for records, THE SYSTEM SHALL skip path components whose name starts with `_` and SHALL skip append-only layers (`ops/`).
- WHEN a file has no parsable frontmatter, THE SYSTEM SHALL NOT silently treat it as a record outside verify contexts.
- THE SYSTEM SHALL report the same record count in `verify`/`stats` before and after a no-op operation.

**Trace:** `design.md` §5.2 (LocalStore scan rules) · §6.1 store row (R6) · `plan.md` P1.4
**Verify:** fixture ที่มี `_`-dir และ `ops/` → ไม่ถูกนับ; count ก่อน/หลัง no-op เท่ากัน; broken file ปรากฏใน `verify` ไม่หายเงียบ

#### REQ-STORE-03 · Atomic persistence + observable commit result

**EARS**
- WHEN persisting a record, THE SYSTEM SHALL write to a temp file in the same directory and rename it into place (atomic).
- WHEN local git is unavailable or the commit fails, THE SYSTEM SHALL leave the record on disk and report `committed: false` with a reason, plus one runtime-log line.
- THE SYSTEM SHALL NOT swallow a commit error.

**Trace:** `design.md` §3.1 failure modes · §3.3 step 4 · §6.1 store row (H3) · `plan.md` P1.4, P2.1
**Verify:** crash test ระหว่าง temp-write กับ rename → record เดิมครบ; no-git test → record อยู่ + `committed:false` + log

#### REQ-IDX-01 · Derived FTS5 index

**EARS**
- THE SYSTEM SHALL maintain a derived SQLite FTS5 index (`unicode61` tokenizer, `bm25` ordering) at `.memory/index-<engine>.sqlite`, stamped with `schema_version`.
- THE SYSTEM SHALL NOT write the Python engine's `index.sqlite`, and SHALL NOT treat the index as a source of truth.

**Trace:** `design.md` §5.2 (derived index row) · §6.1 index row (H1) · `plan.md` P1.5
**Verify:** index tests: create/query/rebuild; `find "$MEM_HOME/.memory" -name 'index*.sqlite*'` ก่อน/หลัง → Python index ไม่ถูกแตะ; index สอง engine พร้อมกันไม่ทับ

#### REQ-IDX-02 · Index recovery

**EARS**
- WHEN the index is deleted, corrupt, or `schema_version`-mismatched, THE SYSTEM SHALL rebuild it from markdown on demand and continue answering queries.
- WHEN FTS5 is unavailable at runtime, THE SYSTEM SHALL fall back to substring matching and say so.

**Trace:** `design.md` §5.2 · §3.1 (index พัง → ลบ `.memory/` แล้ว rebuild) · §9.5 · `plan.md` P1.5
**Verify:** `rm .memory/index-*.sqlite` → search ยังได้ผล (rebuild); corrupt file → rebuild; FTS5-disabled build → fallback path ทำงาน

### 5.6 Sync (2)

#### REQ-SYNC-01 · Explicit, opt-in generic Git sync

**EARS**
- WHEN `mem sync` is invoked, THE SYSTEM SHALL verify the home's contract, refresh the derived index, commit local changes, fetch, `pull --rebase --autostash` (retry ≤ 3), then push without `--force`, to a remote explicitly configured for that home.
- THE SYSTEM SHALL default to no remote, and SHALL work with any generic Git host (GitHub, Bitbucket, GitLab, self-hosted) through git CLI only.
- THE SYSTEM SHALL NOT store provider credentials or speak a provider REST data API in this scope.

**Trace:** `design.md` §5.1 · §5.3 (consent boundary) · §5.4 · §5.5 · §6.1 sync row · `plan.md` P2.2
**Verify:** local bare-repo E2E: commit/fetch/rebase/push ผ่าน; `git remote -v` ของ real clone ไม่เปลี่ยน; default home → "local commit เท่านั้น"

#### REQ-SYNC-02 · Conflict handling

**EARS**
- WHEN a rebase conflict occurs, THE SYSTEM SHALL stop, name the conflicting files, explain the recovery path, and return a non-zero result.
- THE SYSTEM SHALL NOT auto-merge frontmatter or continue a push after a conflict.

**Trace:** `design.md` §5.3 (sync conflicts) · §5.4 (sequence, alt branch) · `plan.md` P2.2
**Verify:** conflict fixture → non-zero + ชื่อไฟล์ใน output; ไม่มี commit/push ต่อหลัง conflict

### 5.7 Eval (1)

#### REQ-EVAL-01 · Private recall-parity gate on the real corpus

**EARS**
- WHEN the private recall gate runs on a fresh real clone, THE SYSTEM SHALL evaluate the golden 30-pair corpus and report `n_pairs = 30`, `recall5 ≥ 0.8667`, `mrr ≥ 0.85`, exiting `0` only when the ported verdict passes both thresholds.
- THE SYSTEM SHALL re-run this gate for every engine version or configuration change that can affect retrieval, including after P4.
- THE SYSTEM SHALL NOT substitute the public synthetic eval (G1) for this gate, and SHALL NOT copy the corpus or its results into the public repo/CI.

**Trace:** `design.md` §6.1 eval row · §9.2 ข (S5) · §9.4 (G1 ≠ S5) · `plan.md` P2.6
**Verify:** `MEM_HOME="$REAL_CLONE" "$ENGINE_BIN" eval --json` → exit 0 + metrics ผ่าน; perturb 1 scoring parameter → non-zero; artifact อยู่ใน private `$WF/parity/`

### 5.8 Compatibility & cutover (2)

#### REQ-COMPAT-01 · Compatibility contract (no versioned break in this scope)

**EARS**
- THE SYSTEM SHALL keep the record format, record ids, the 13 MCP tool schemas, and the 4 hook output formats backward compatible within this scope (additive-only changes).
- WHEN a breaking change is proposed, THE SYSTEM SHALL require a versioned migration plus a separate design decision; this spec SHALL NOT authorize it.

**Trace:** `design.md` §9.1 · §8.1 (harness↔mem row) · §9.5 · `plan.md` P2.3, P2.4 (OQ5 premise)
**Verify:** frozen `tools/list` snapshot diff = 0; hook format snapshots = 0 diff; byte-exact S3 + id S4 (REQ-REC-02/03) ถือเป็นส่วนหนึ่งของ contract นี้

#### REQ-CUTOVER-01 · Staged, reversible cutover of the 6 harnesses

**EARS**
- WHEN a harness is cut over, THE SYSTEM SHALL switch only that harness and run its live probe: `mem stats` shows writes from `agent:<name> > 0`, hook exits `0`, and no push occurs.
- THE SYSTEM SHALL retain a config backup that restores the Python engine for that harness, and SHALL cut over serially, each requiring owner acceptance before the next.
- WHEN the probe fails, THE SYSTEM SHALL restore that harness before touching any other.

**Trace:** `design.md` §2.1 G-6 · §9.3 P3 · §9.4 (G7, manual matrix) · `plan.md` P3.1–P3.6
**Verify:** `tools/cutover-probe.sh --harness <name> --live` exit 0 + `stats` มี `agent:<name>`; rollback test คืน Python แล้ว probe ยังผ่าน

## 6. Non-functional requirements (12)

| ID | EARS (condensed) | Trace | Verify |
|---|---|---|---|
| **NFR-LANG-01** | WHEN the language gate closes at the end of P0, THE SYSTEM SHALL have its implementation language chosen only from measured results persisted on disk; THE SYSTEM SHALL NOT present any Bun-side claim as measured fact (the Bun evidence set contains no `[MEASURED]`). | `design.md` §4.1 · §9.2 ก · AGENTS.md (evidence discipline) · `plan.md` P0.13 | `language-gate.md` + per-spike `result.md` อยู่บนดิสก์, `verify-language-gate.sh` exit 0, owner sign-off; claim ที่ไม่มี measurement ต้องติดป้าย inference |
| **NFR-PERF-01** | THE SYSTEM SHALL keep hook p50 ≤ 150 ms on the synthetic reference corpus; WHEN records ≥ 400 or hook p50 > 150 ms, incremental indexing becomes a documented reopen trigger (not an MVP capability). | `design.md` §4.4 · §6.1 index row · §9.4 (G6) · §11.3 · `plan.md` P2.7 | `./tools/benchmark-hook.sh --corpus testdata/perf/reference` รายงาน p50 ≤ 150 ms; injected delay → non-zero |
| **NFR-NET-01** | THE SYSTEM SHALL NOT make a network call on the default path; only `sync` (and future opt-in provider/backup commands) may egress, and each SHALL be explicitly configured per home. | `design.md` §5.3 (offline guarantee) · §8.3 · §2.1 G-4 · `plan.md` P1.4, P1.6 | no-network container: suite เขียว; inject network call → test แดง |
| **NFR-SEC-01** | THE SYSTEM SHALL keep secrets on the machine: MVP SHALL store no credential, SHALL rely on the user's git credential helper, and SHALL NOT put a token in config, binary, or log. | `design.md` §5.5 · §5.1 · §8.1 (tool↔secrets) · `plan.md` P2.2 | grep credential/keyring fields ใน source/config/log = 0; gitleaks exit 0 |
| **NFR-PRIV-01** | THE SYSTEM SHALL NOT contain real corpus, prompt logs, personal records, or secrets in this repo. | `design.md` §7 item 2 · §2.3 K2 · `plan.md` P4.2 | `gitleaks detect` = no leaks; tracked-path grep ไม่มี `lessons/`, `knowledge/`, `ops/`, `handoffs/`, `docs/workflow/`; manual review ทุก fixture |
| **NFR-PRIV-02** | WHEN an artifact contains real corpus data (spikes, parity, baseline, run logs), THE SYSTEM SHALL keep it only in the private data repo (`$WF`), never here. | `design.md` §7 item 4 · §9.2 · `plan.md` preamble + P1.7/P2.5/P2.6 | engine tree ไม่มี artifact ที่ derive จาก corpus จริง; `testdata/` เป็น synthetic + provenance label; private runner อยู่ใน `$WF/parity/` |
| **NFR-OPS-01** | THE SYSTEM SHALL NOT fail silently: every partial failure SHALL surface as an explicit result field, a non-zero exit where the contract allows, or a runtime-log line. | `design.md` §3.1 · §3.3 step 4 · RK7 · `plan.md` P1.4, P2.1 | fault-injection suite: missing git, unwritable log, corrupt index, failing remote — ทุกกรณี observable; ไม่มี empty catch |
| **NFR-OPS-02** | WHEN a phase gate runs, THE SYSTEM SHALL persist command, real exit code, version/SHA, date, and probe result on disk so work resumes from the first missing/failed step. | `design.md` §9.1 (probe + count per round) · §9.2 · `plan.md` P2.5, P3 (resumability) | artifact ต่อ family/gate มีครบ 5 ฟิลด์; restart กลางทาง resume ที่ family แรกที่ยังไม่มี PASS |
| **NFR-QUAL-01** | THE SYSTEM SHALL keep public CI synthetic-only and separate from the private real-corpus gate; public CI SHALL NOT be presented as recall parity. | `design.md` §6.1 eval row (MAJOR-1 split) · §9.4 (G1) · `plan.md` P1.6, P2.6 | CI config ไม่มี corpus จริง; G1 ใช้ synthetic baseline ของ fixture ชุดนั้น; S5 artifact อยู่ใต้ `$WF` เท่านั้น |
| **NFR-PORT-01** | WHEN a target OS is declared, THE SYSTEM SHALL be verified natively on it; cross-compilation SHALL NOT substitute for native runtime evidence. | `design.md` §9.2 ก · §8.2 (CI matrix) · `plan.md` P0.1/P0.3 + "Under-specified" ข้อ 5 | native transcript ต่อ target (OS/arch + SQLite version) อยู่บนดิสก์; ไม่มี runner = `BLOCKED` ไม่ใช่ pass |
| **NFR-COMPAT-01** | THE SYSTEM SHALL document counts (commands, MCP tools, tests) that match the code; docs SHALL generate counts from code rather than hand-maintaining them. | `design.md` §9.5 (last bullet) · §11.2 (T1–T4 drift) · `plan.md` P1.6 | generated count test: จำนวน command ใน docs == `add_parser` count, tool count == `MCP_TOOLS` length; CI แดงเมื่อ drift |
| **NFR-RELEASE-01** | THE SYSTEM SHALL NOT be published without the hygiene/gitleaks gate and an explicit owner approval; no remote SHALL exist before both. | `design.md` §7 item 3 · §8.2 · §2.1 G-5 · `plan.md` P4.2, P4.3 | `git remote -v` ว่างก่อนอนุมัติ; gitleaks `no leaks found`; approval บันทึกเป็น artifact + สถานะ `BLOCKED — public gate pending` ถ้าไม่มี |

## 7. Global out of scope (ตรงกับ design §2.2 / §6.1 later column)

| Out of scope | ทำไม (อ้าง design) | Reopen trigger |
|---|---|---|
| semantic / vector search | §2.2 (trigger เดิม: paraphrase MRR ≤ 0.40 ขณะ authored ≥ 0.70 สองรอบติด หรือ records > ~3,000) | §11.3 · OQ6 |
| provider REST adapters (GitHub/Bitbucket API เป็น data backend) | §5.1/§5.2 (provider = metadata เท่านั้น, เฟส 2) | §11.3 (rate-limit เปลี่ยน) |
| object-store sink / BackupSink port | §5.2 (backup ≠ sync; bash kit เดิมใช้ต่อ) | เฟส 2 |
| MCP HTTP transport | §6.1 mcp row (stdio เท่านั้น) | §11.3 |
| incremental index | §6.1 index row (MVP = full rebuild) | records ≥ 400 หรือ p50 > 150 ms |
| daemon / service | §3.1 A3 (ปฏิเสธ) | workload เปลี่ยนจน A3 justified |
| multi-user / hosted service | §2.2 (สืบทอด non-goal เดิม) | ไม่มีใน scope นี้ |
| background LLM extraction | §2.2 | — |
| graph DB | §2.2 | — |
| `invalid_at` bi-temporal | REQ-REC-08 · design ไม่มีใน MVP | future versioned migration (§9.5) |
| public packaging/distribution (GoReleaser, notarize, SBOM, Homebrew) | §8.2 (fast-follow หลัง P3, gate ด้วย OQ1/OQ2 + approval) | หลัง cutover |

**หมายเหตุ:** การอยู่ในตารางนี้ไม่ได้ให้สิทธิ์สร้าง scope ใหม่ — ทุก reopen ต้องผ่าน design review ตาม `design.md` §11.3 และ `plan.md` (Open items)

## 8. Trust & data boundary (stated as requirements)

| # | Requirement (EARS) | ผูกกับ |
|---|---|---|
| **TB-1** | THE SYSTEM SHALL perform zero network calls on the default path; network egress SHALL occur only in an explicitly configured `sync` (and, later, opt-in provider/backup commands). | NFR-NET-01 |
| **TB-2** | THE SYSTEM SHALL NOT transmit, log, or persist credentials: MVP holds none and defers to the user's git credential helper; no plaintext token fallback SHALL exist. | NFR-SEC-01 |
| **TB-3** | THE SYSTEM SHALL NOT place real corpus, prompt logs (`ops/recall-log.jsonl`), personal records, or secrets in this repo, its CI, or its artifacts. | NFR-PRIV-01 |
| **TB-4** | WHEN an artifact derives from real data, THE SYSTEM SHALL store it only in the private data repo (`$WF`: spikes, parity, baseline, cutover logs). | NFR-PRIV-02 |
| **TB-5** | THE SYSTEM SHALL read data only through `MEM_HOME` and SHALL NOT hardcode a machine-specific absolute path in engine code, defaults, or docs. | REQ-STORE-01 |
| **TB-6** | THE SYSTEM SHALL keep the engine→data flow one-directional: the data repo installs the engine; the engine repo never references the data repo. | `design.md` §7 item 2 |
| **TB-7** | WHEN a hook fires, THE SYSTEM SHALL NOT push and SHALL NOT call the network (fail-open, local only). | REQ-HOOK-02 |

**ยอมรับตรง ๆ (ไม่เคลมเกินจริง):** fixture provenance label เป็น metadata check ไม่ใช่ข้อพิสูจน์ว่าเนื้อหา synthetic จริง — hygiene ของเนื้อหาจริงบังคับด้วย manual review + checklist + secret/absolute-path scan + owner approval ก่อน public push ครั้งแรก (`design.md` §7 item 3)

## 9. Acceptance criteria (cross-cutting, owned by this file)

AC ต่อไปนี้คือ invariant ระดับ product ที่ไม่มี phase spec ไหน owns; AC ระดับ requirement + test skeleton อยู่ใน phase spec (ดู `specs/README.md`)

**AC-1 · Byte-exact round trip** (REQ-REC-02)
```gherkin
Given a fresh clone of the real data repo in a private temp directory
When every record is read and re-written by the new codec
Then git diff --exit-code is empty on that clone
  And changing one quoting decision in the control copy makes the gate exit non-zero
```

**AC-2 · ID stability** (REQ-REC-03)
```gherkin
Given the full corpus on that clone
When ids are recomputed from layer, scope, title and body
Then matched/total equals 100%
  And mutating one id input field in the control copy makes the gate non-zero
```

**AC-3 · Untrusted add is quarantined and invisible** (REQ-REC-05, REQ-REC-07)
```gherkin
Given an add with source "external:web" and no evidence
When the add is attempted
Then it is rejected with exit 3
  And when the same add carries evidence it is stored with sensitivity quarantine
  And that record never appears in context, hook recall, render output, or default search
  And only explicit review with evidence releases it to internal
```

**AC-4 · Write survives a broken git, visibly** (REQ-STORE-03, NFR-OPS-01)
```gherkin
Given a memory home where git is absent or the commit fails
When a valid record is added
Then the record file exists on disk with valid frontmatter
  And the response reports committed: false with a reason
  And one line is appended to the gitignored runtime log under .memory/
  And the hook path still exits 0
```

**AC-5 · Default path is offline** (NFR-NET-01)
```gherkin
Given a machine with no network
When context, search, show, add, verify, stats, render and hook run
Then all of them succeed
  And no network call is attempted
  And an injected network call in a control build makes the no-network test fail
```

**AC-6 · Derived index is disposable** (REQ-IDX-02)
```gherkin
Given a memory home with a built index
When the index file is deleted (or corrupted, or version-mismatched)
Then the next query rebuilds it from markdown and returns results
  And no record file is modified by the rebuild
  And when FTS5 is unavailable the substring fallback answers instead
```

**AC-7 · Sync conflict stops, never merges frontmatter** (REQ-SYNC-02)
```gherkin
Given a local commit and a conflicting remote commit touching the same record
When mem sync runs
Then it exits non-zero, names the conflicting file, and stops before push
  And no frontmatter is auto-merged
```

**AC-8 · Hook is fail-open but not silent** (REQ-HOOK-02)
```gherkin
Given a hook event where the engine binary, git, or the log path is broken
When the hook runs
Then it exits 0
  And it performs no push and no network call
  And the failure is recorded when a log path is writable
```

## 10. Glossary + id conventions

### 10.1 Domain terms

| Term | ความหมายในสเปกนี้ |
|---|---|
| **record** | หนึ่ง fact = หนึ่ง markdown file + YAML-subset frontmatter (REQ-REC-01); มี `id`, `layer`, `scope`, `created`, `status`, `sensitivity` เป็น required |
| **layer** | ประเภทของ record ตาม `taxonomy.json`: `rule, project, lesson, decision, handoff, knowledge` (one-per-*) + `ops` (append-only, ไม่ใช่ record ต่อ fact) |
| **corpus** | กลุ่ม record ทั้งหมดใน memory home หนึ่ง ๆ |
| **golden corpus** | ชุด 30 คู่ (query → expected record) ที่ใช้ตัดสิน recall — อยู่ใน private data repo เท่านั้น |
| **baseline** | ค่า metric ที่ผ่านการวัดและบันทึกไว้ (`recall5 = 0.8667`, `mrr = 0.85`, `n_pairs = 30`) ใช้เป็นเกณฑ์เปรียบเทียบ |
| **gate** | เงื่อนไขที่ต้องผ่านด้วยหลักฐานบนดิสก์ก่อนไป step ถัดไป; มี discriminating probe (ทำพังแล้วต้องแดง) |
| **spike** | การทดลอง pre-code ขนาดเล็กเพื่อปิด evidence gap (S1, S2, B1–B7, S6, RS1/RS2) |
| **parity** | พฤติกรรมของ engine ใหม่ตรงกับ Python baseline ในมิติที่กำหนด (bytes, ids, output, recall) |
| **cutover** | การสลับ harness หนึ่งตัวจาก Python ไป engine ใหม่ พร้อม rollback path |
| **`MEM_HOME`** | root ของ memory home (data repo working tree) ที่ engine ใช้ resolve record/schema/index |
| **working tree** | canonical git working tree ของ data repo — source of truth ของ record bytes |
| **FTS5** | SQLite full-text search extension (`unicode61` + `bm25`) ที่ index derived ใช้ |
| **hook** | entrypoint ที่ harness เรียกตาม event (`session-start`, `user-prompt`, `stop`) เพื่อฉีด/บันทึกความจำ |
| **harness** | agent tool ที่ต่อกับ engine (cutover scope = 6: claude, codex, gemini, opencode, cursor, hermes) |
| **quarantine** | สถานะ `sensitivity` ของ record ที่ยังไม่น่าเชื่อถือ — ถูกกันออกจากทุก injection path จนกว่าจะ review |
| **scoring** | สูตร additive ที่จัดอันดับ record (`LAYER_WEIGHT` + keyword overlap + confidence + scope + recency + uses − penalties) |

### 10.2 Id conventions ของชุดสเปกนี้

| Pattern | ใช้กับ | กฎ |
|---|---|---|
| `REQ-<AREA>-NN` | functional requirement | area ∈ `REC, DOM, CLI, MCP, HOOK, INST, STORE, IDX, SYNC, EVAL, COMPAT, CUTOVER`; **ห้าม renumber** แม้ requirement จะถูกแก้ถ้อยคำ |
| `NFR-<AREA>-NN` | non-functional requirement | area ∈ `LANG, PERF, NET, SEC, PRIV, OPS, QUAL, PORT, COMPAT, RELEASE` |
| `AC-<n>` | cross-cutting acceptance criterion ในไฟล์นี้ | running number ต่อไฟล์; AC ระดับ requirement ใช้ `AC-<REQ-ID>-NN` ใน phase spec |
| `OQ<n>` | open question | มาจาก `design.md` §12 — **เจ้าของ id คือ design** ห้ามสร้าง OQ ใหม่ที่นี่ |
| `GAP-<n>` | ช่องว่างที่พบระหว่างทำสเปก | บันทึกไว้ ไม่ตัดสินแทน owner (§11.2) |
| `TB-<n>` | trust boundary requirement | §8 ของไฟล์นี้ |
| `Rn / Hn / Gn / Tn / Sn / En` | อ้างถึงตารางใน `exploration.md`/`design.md`/`research.md` | คงความหมายเดิมของเอกสารต้นทาง |

**Chain of record:** `REQ/NFR` (ไฟล์นี้) → phase spec (AC + test skeleton) → gate (on-disk evidence) → plan task (implementation) — ทุกข้อต้องเดินครบ chain ก่อนถือว่า done

## 11. Open questions และ gaps

### 11.1 Open questions (owner-owned — สเปกนี้ไม่ตัดสิน)

| # | คำถาม | Gate | Owner ต้องตอบอะไร |
|---|---|---|---|
| **OQ1** | License ของ engine repo | ก่อน public push ครั้งแรก (P4) | เลือก license (design แนะนำ Apache-2.0) + ยืนยัน compatibility ก่อน publication; ห้าม create `LICENSE` ก่อนตัดสิน |
| **OQ2** | Owner + module/package identity | **ก่อน P1 scaffold** (hard gate) และยืนยันซ้ำก่อน P4 | ชื่อ repo = `my-memory-engine` (ตัดสิน 2026-10-19) แต่ **namespace ยังเปิด**: Go module path / npm package name / Rust crate name + owner บน GitHub — เปลี่ยนหลัง scaffold = rewrite หลายไฟล์ |
| **OQ3** | Cutover appetite: staged ราย harness vs one-shot | ก่อน P3 | ยืนยัน staged (design/plan ใช้ staged) หรือรับความเสี่ยง one-shot แบบชัดแจ้ง |
| **OQ4** | Platform/packaging matrix | ก่อนล็อก P0 native matrix / release | ระบุ 5 targets + Linux libc variant (glibc/musl) + Windows codesign (ยอมรับ SmartScreen หรือ EV cert) — cross-compile ไม่นับเป็น native pass |
| **OQ5** | Freeze MCP tools + additive-only หรือ breaking (9 ตอนตัดสิน — วันนี้ 13 แบบ additive) | **ก่อน P2** (phase gate) | ยืนยัน freeze; ถ้าเลือก breaking แผน parity/cutover + REQ-COMPAT-01 ของสเปกนี้เป็นโมฆะ ต้องมี design แยก |
| **OQ6** | Semantic search ใน MVP? | defer ได้ (ไม่ block) | ยืนยัน "ไม่ทำ" ตาม trigger เดิม หรืออนุมัติ scope วิจัยใหม่ |
| **OQ7** | ยอมรับ Bun `setCustomSQLite` trade-off ไหม | เปิดเฉพาะถ้า B1–B3 ตกบน macOS | ตอบรับ/ไม่รับ หลังดู B7 evidence; ไม่มีคำตอบ = Bun lock ค้าง (ไม่ใช่ pass) |

### 11.2 Gaps ที่พบระหว่างทำสเปก (บันทึก ไม่ตัดสิน)

| # | Gap | ทำไมสำคัญ | ใครปิด |
|---|---|---|---|
| **GAP-1** | **Spike-artifact schema** ไม่ถูกกำหนดใน design (plan "Under-specified" ข้อ 2 ระบุเอง) | NFR-OPS-02 ต้องมีฟิลด์ครบ (command/exit code/SHA/date/probe) แต่ยังไม่มี schema กลาง → artifact ต่าง spike อาจเทียบกันไม่ได้ | plan/tech-lead ก่อน P0.1 (decision ไม่ใช่ของ owner) |
| **GAP-2** | **Runner interfaces** (`private-parity.sh`, `cutover-probe.sh`, `benchmark-hook.sh`) เป็น implementation artifact — design กำหนดแค่ behavior/gate | REQ-EVAL-01/REQ-CUTOVER-01/NFR-PERF-01 อ้าง command ที่ยังไม่มีสัญญา CLI → test skeleton เขียนไม่ได้จนกว่ารunnerจะถูกกำหนด | plan P1.7/P2.4/P2.7 |
| **GAP-3** | **Native-runner availability** — design ประกาศ 5 targets แต่ไม่มีหลักฐานว่ามีเครื่อง/CI runner ครบทุก OS | NFR-PORT-01 + S1/B1–B3/RS1 ต้องการ native runtime; ไม่มี runner = `BLOCKED` ไม่ใช่ pass | owner (ให้ scope/runner) ก่อน P0.1 |
| **GAP-4** | **Credential wire boundary** — design §5.5 ครอบ "ไม่เก็บ credential" แต่ไม่มีข้อกำหนดว่า **credential และข้อมูลอะไรข้าม network ได้** ใน `sync` (record ทั้งไฟล์เป็น plaintext ใน git; การเข้ารหัสเป็นเรื่องของ BackupSink เฟส 2) | TB-1/TB-2/NFR-SEC-01 ยังไม่ตอบว่า home ที่มี record `sensitivity: private` sync ขึ้น remote ได้หรือไม่/ต้องเข้ารหัสก่อนหรือไม่ และ git credential helper ส่งอะไรออกไปได้บ้าง | owner ตัดสินก่อนเปิด `mem sync` กับ remote จริง (P3) — ไม่ใช่แค่เรื่อง token |
| **GAP-5** | **Assertion-count metric ไม่ถูก pin** — exploration นับ 187 (2026-10-17) แต่ re-count วันนี้ได้ 352 helper call sites / 219 `chk` (2026-10-19, HEAD `2333e53`) | NFR-COMPAT-01 และ drift check ของ Task 0 พึ่งตัวเลขนี้; ถ้านิยามไม่ชัด sẽ có "เขียวเท็จ" จากคนละ metric | plan (นิยาม metric + จำนวนที่ใช้เป็น drift check) ก่อน P1.6 |
| **GAP-6** | **Harness scope 6 vs registry 7** — `schema/agents.json` มี 7 entries (เพิ่ม `kirocrew`, mode `pull`) ขณะที่ design/plan กำหนด cutover 6 ตัว | REQ-CUTOVER-01 ระบุ 6; ถ้า `kirocrew` ต้อง cutover ด้วย แผน P3 ต้องเพิ่ม task — ถ้าไม่ ต้องระบุว่ามันใช้ path ไหน | design/plan (ไม่ใช่ owner) — บันทึกไว้ก่อน P3 |
| **GAP-7** | **ตัวเลข corpus ไม่ตรงกันตามเวลา** — design "~160 records"/"127 lessons", plan Task 0 "201 records", วันนี้ 206 md files / 149 lessons | ใช้เป็น drift check ไม่ได้ถ้าไม่รู้ว่าฐานนับคืออะไร (`stats` vs file count vs active only) | plan Task 0 (ระบุ metric) + doc-update (§9.5) |

## 12. Approval

**Status:** DRAFT — awaiting owner approval

### Owner-approval checklist

- [ ] อ่าน `design.md` rev 5 §2 (goal/non-goal/constraints), §9.2 (gates), §12 (OQ) และ `plan.md` (Task 0 + P0–P4) ก่อนอนุมัติสเปกนี้
- [ ] ยืนยันว่า **25 functional + 12 non-functional** ตรงกับ intent และไม่มีข้อใดเพิ่ม scope ที่ design ไม่ได้อนุมัติ
- [ ] ยืนยันว่า global out-of-scope (§7) ตรงกับ `design.md` §2.2/§6.1 และไม่มี item ใดถูกดึงเข้า MVP เงียบ ๆ
- [ ] ยืนยันว่า OQ1–OQ7 ยังเป็นสิทธิ์ของ owner และ gate ของแต่ละข้อยังไม่ถูกเลื่อน
- [ ] ยืนยันว่า gaps GAP-1…GAP-7 ถูกบันทึกเป็น gap — **ไม่ใช่**คำตอบที่สเปกนี้ตัดสินแทน
- [ ] ยืนยัน capability-map counts (§4.1) ว่าตรงกับที่ต้องการใช้เป็น drift baseline
- [ ] ยืนยันว่าสเปกนี้ **ไม่อนุญาตให้เริ่ม implementation** จนกว่าจะ APPROVED (และยังไม่ authorize remote/push/license)
- [ ] เปลี่ยน `**Status:**` เป็น `APPROVED` พร้อมวันที่ และ mirror สถานะใน `.claude/active-spec.md`

### Change log

| Rev | Date | Change | Author |
|---|---|---|---|
| 1 | 2026-10-19 | แรก — 25 functional + 12 NFR, capability map (18 legacy CLI + init/doctor, 9 MCP tools, 4 hook formats, 6 harness), trust boundary, glossary, OQ1–OQ7 + GAP-1…GAP-7, approval block; traceability ไป design rev 5 + plan 37 tasks | opencode |

*สเปกนี้เป็น DRAFT — ไม่มีผลเป็นการอนุมัติ design, ไม่ authorize implementation, remote, push หรือ publication ใด ๆ*
