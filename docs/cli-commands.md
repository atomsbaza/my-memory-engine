# mem command surface

20 commands: 18 legacy Python families + `init` + `doctor`.

Generated from `cli.Manifest()` — edit internal/cli/cli.go, not this file.

| command | synopsis | help | surface |
|---|---|---|---|
| `context` | `[--project P] [--scope S] [--query Q] [--budget N] [--no-touch] [--quiet]` | ดึงความจำที่เกี่ยวข้องกับงาน | legacy |
| `search` | `<query> [--scope S] [--layer L]... [--limit N] [--all] [--scores]` | ค้น record | legacy |
| `list` | `[--layer L] [--scope S] [--limit N] [--all] [--status ST]` | ไล่รายการ record | legacy |
| `show` | `[<id>] [--path P]` | เปิด record เต็ม | legacy |
| `add` | `--layer L --scope S --file F [flags]` | เพิ่ม record ใหม่ | legacy |
| `touch` | `<id>` | อัปเดต last_used | legacy |
| `cite` | `<id>` | นับว่า record ถูกใช้จริง (uses +1) | legacy |
| `review` | `<id> --evidence E` | ปลด quarantine: sensitivity -> internal (ต้องมี --evidence) | legacy |
| `supersede` | `<old> --with <new> [--no-commit]` | แทนที่ record เก่าด้วยอันใหม่ | legacy |
| `verify` | `—` | ตรวจ schema/secret/path/dup/index | legacy |
| `reindex` | `—` | สร้าง index ใหม่ (derived) | legacy |
| `render` | `[--agent A]... [--dry-run] [--print]` | อัปเดต pointer block ในไฟล์ของแต่ละ agent | legacy |
| `sync` | `[-m MSG] [--no-push]` | commit + pull --rebase + push | legacy |
| `import` | `<kirocrew|dir> [--path P] [--apply]` | ดึงความจำจากระบบอื่น (เข้า repo นี้) | legacy |
| `hook` | `--event E [--agent A] [--query Q] [--format F]` | entry point เดียวที่ harness เรียกเป็น hook | legacy |
| `stats` | `[--json]` | สถิติ + ความครอบคลุมของแต่ละ harness | legacy |
| `eval` | `[--days N] [--json] [--save-baseline] [--from-log] [--weights W]` | วัด recall@5/MRR/noise เทียบ eval/baseline.json (eval loop) | legacy |
| `mcp` | `[--agent A]` | รันเป็น MCP server บน stdio (harness เรียกเป็น tool) | legacy |
| `init` | `[PATH]` | สร้าง memory home ใหม่ (ไม่แตะ home เดิม) | engine |
| `doctor` | `—` | ตรวจความสอดคล้อง file ↔ index ↔ remote | engine |

Exit codes: `0` success · `1` failed check/generic failure · `2` unsafe input or usage error · `3` conflict (dedup block, supersede misuse, sync rebase conflict).
