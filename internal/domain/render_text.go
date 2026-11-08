// Code generated from the Python reference ~/my-memory/bin/mem render_block
// (f-string templates, byte-exact); do not hand-edit the string bodies.
package domain

// pointerTemplate is the pointer-mode block body (%[1]s = mem binary
// path, %[2]s = agent write command).
const pointerTemplate = "## Memory — ความจำถาวรของผู้ใช้ (my-memory)\n\nกฎถาวรและความจำทั้งหมด (rules / projects / lessons / decisions / handoffs) อยู่ใน private git repo `~/my-memory`\nก่อนทำงานให้ดึงที่เกี่ยวข้อง และเมื่อจบงานให้เขียน lesson ที่คุ้มค่ากลับไป:\n\n```sh\n%[1]s context --project <Project> --query \"<งานที่กำลังทำ>\"   # อ่านก่อนทำงาน\n%[1]s search \"<คำค้น>\" --limit 5                              # ค้นเพิ่ม\n%[1]s %[2]s   # เขียนเมื่อจบงาน\n```\n\nถ้ามี MCP server `my-memory` ให้ใช้ tool `memory_context` / `memory_search` / `memory_add` แทนได้\nถ้า record ใดเปลี่ยนการตัดสินใจของเธอ ระบุ [id: …] ของมันไว้ในคำตอบ (หรือเรียก `mem cite <id>`)\nไฟล์นี้เป็น pointer ที่ `mem render` สร้างใหม่ได้ — ห้ามคัดลอกเนื้อหา memory มาวางซ้ำ"

// usageTemplate is the render-mode usage header (same placeholders).
const usageTemplate = "## My memory — portable memory ข้าม agent (อ่านก่อนทำงาน, เขียนตอนจบงาน)\n\n```sh\n%[1]s context --project <Project> --query \"<งานที่กำลังทำ>\"   # ดึงความจำที่เกี่ยวข้อง\n%[1]s search \"<คำค้น>\" --limit 5                              # ค้นเพิ่ม\n%[1]s show <id>                                               # เปิด record เต็ม\n```\n\nถ้า harness นี้ต่อ MCP server `my-memory` อยู่ ให้ใช้ tool `memory_context` / `memory_search` / `memory_add` แทนคำสั่ง shell (ผลเท่ากัน)\n\nเขียนเมื่อจบงาน (lesson ถาวร / project state / handoff):\n```sh\n%[1]s %[2]s\n```\n"
