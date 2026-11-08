// Package mcp implements the MCP stdio server (design §6 "mcp" row):
// the 9 frozen tools (plan OQ5 — additive-only compatibility premise)
// served over github.com/modelcontextprotocol/go-sdk.
//
// This is the P1 skeleton: server identity, the 9-tool registry, input
// schemas and the stdio transport are final; tool bodies are stubs that
// return a text result. Domain wiring lands in P2.3 (plan.md P2.3
// "MCP stdio SDK adapter, 9 frozen tools"), where the emitted tools/list
// snapshot must match the frozen baseline byte-for-byte.
//
// Stdout purity (design §6; S2/B4 spike class of failure): the package
// itself never writes to os.Stdout — every byte on stdout comes from the
// SDK's JSON-RPC framing, and the server is constructed without a logger
// so no diagnostics can leak into the stream. Anything a future handler
// needs to report goes into the tool result, never into print calls.
package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server identity — MCP_SERVER_NAME/MCP_VERSION in the Python baseline.
const (
	ServerName = "my-memory"
	Version    = "1.0.0"
)

// ToolNames lists the 9 frozen tools (OQ5) in Python mem's MCP_TOOLS order.
// memory_cite/archive/promote/compact exist in the newer Python baseline but
// are outside the frozen set and stay out until the compatibility decision
// says otherwise — additive only.
var ToolNames = []string{
	"memory_context",
	"memory_search",
	"memory_show",
	"memory_add",
	"memory_list",
	"memory_verify",
	"memory_stats",
	"memory_sync",
	"memory_trace",
}

// ---------------------------------------------------------------------------
// Input schemas — structs mirroring the frozen Python MCP_TOOLS inputSchema
// for each tool. AddTool infers the JSON Schema from these; required fields
// are the non-pointer fields without omitempty, matching the baseline's
// "required" lists.
// ---------------------------------------------------------------------------

// ContextArgs is memory_context's schema: budgeted retrieval by task.
type ContextArgs struct {
	Query   string `json:"query,omitempty" jsonschema:"งานที่กำลังจะทำ"`
	Project string `json:"project,omitempty" jsonschema:"ชื่อ project ใน my-memory/projects"`
	Scope   string `json:"scope,omitempty" jsonschema:"global | project:<Name> | repo:<owner/name>"`
	Budget  int    `json:"budget,omitempty" jsonschema:"chars สูงสุด (default ตาม taxonomy)"`
	NoTouch bool   `json:"no_touch,omitempty" jsonschema:"true = ไม่อัปเดต last_used"`
}

// SearchArgs is memory_search's schema: query is required.
type SearchArgs struct {
	Query           string `json:"query" jsonschema:"คำค้น"`
	Layer           string `json:"layer,omitempty" jsonschema:"rule | project | lesson | decision | handoff | knowledge"`
	Scope           string `json:"scope,omitempty"`
	Limit           int    `json:"limit,omitempty" jsonschema:"default 8"`
	IncludeInactive bool   `json:"include_inactive,omitempty" jsonschema:"รวม superseded/archived"`
	Scores          bool   `json:"scores,omitempty" jsonschema:"true = แสดง rrf_score/fts_rank/vec_rank/score_rank"`
}

// ShowArgs is memory_show's schema: id is required.
type ShowArgs struct {
	ID string `json:"id" jsonschema:"id ของ record"`
}

// AddArgs is memory_add's schema: text is required.
type AddArgs struct {
	Text        string   `json:"text" jsonschema:"body ของ record (markdown)"`
	Layer       string   `json:"layer,omitempty" jsonschema:"lesson | decision | handoff | project | knowledge (default lesson)"`
	Scope       string   `json:"scope,omitempty" jsonschema:"default global"`
	Title       string   `json:"title,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Confidence  string   `json:"confidence,omitempty" jsonschema:"0.0-1.0 · ต่ำกว่า 1.0 ต้องมี evidence"`
	Decay       float64  `json:"decay,omitempty" jsonschema:"0.0-1.0 (default 1.0)"`
	HalfLife    string   `json:"half_life,omitempty" jsonschema:"14d | 30d | inf"`
	Evidence    []string `json:"evidence,omitempty"`
	Expires     string   `json:"expires,omitempty" jsonschema:"none | YYYY-MM-DD | +21d (handoff ต้องมี)"`
	Supersedes  string   `json:"supersedes,omitempty" jsonschema:"id ของ record เดิมที่เป็นเรื่องเดียวกัน"`
	Supersede   bool     `json:"supersede,omitempty" jsonschema:"true = แทนที่ record ที่ซ้ำโดยอัตโนมัติ"`
	Source      string   `json:"source,omitempty" jsonschema:"default = agent ของ harness ที่เรียก"`
	Sensitivity string   `json:"sensitivity,omitempty" jsonschema:"public | internal | private | quarantine"`
	Force       bool     `json:"force,omitempty"`
}

// ListArgs is memory_list's schema.
type ListArgs struct {
	Layer string `json:"layer,omitempty"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"default 40"`
}

// VerifyArgs is memory_verify's schema: no parameters.
type VerifyArgs struct{}

// StatsArgs is memory_stats's schema: no parameters.
type StatsArgs struct{}

// SyncArgs is memory_sync's schema.
type SyncArgs struct {
	Message string `json:"message,omitempty"`
	NoPush  bool   `json:"no_push,omitempty"`
}

// TraceArgs is memory_trace's schema: query is required.
type TraceArgs struct {
	Query string `json:"query" jsonschema:"คำค้นที่ต้องการ debug"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"default 10"`
}

// ---------------------------------------------------------------------------
// Server construction
// ---------------------------------------------------------------------------

// NewServer builds the my-memory MCP server with the 9 frozen tools
// registered. Handlers are stubs until P2.3: each returns a text result
// naming the tool, which keeps the initialize/tools/list/tools/call
// contract exercisable end-to-end before the domain exists.
func NewServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: Version}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "memory_context", Description: "ดึงความจำที่เกี่ยวข้องกับงาน (rules + project + lessons + decisions + handoffs) ตาม budget — เรียกตอนเริ่มงานหรือเมื่อเปลี่ยนงาน"}, handleContext)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_search", Description: "ค้น record ด้วยคำค้น (FTS + ranking) คืน id / layer / บรรทัดแรก"}, handleSearch)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_show", Description: "เปิด record เต็ม (frontmatter + body) ด้วย id"}, handleShow)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_add", Description: "เขียน record ใหม่ (1 record = 1 fact) · lesson ต้องมี Rule./NOT./Evidence. · ระบบกันซ้ำ + กัน secret/absolute path ให้อัตโนมัติ"}, handleAdd)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_list", Description: "ไล่รายการ record (id, layer, scope, created, title)"}, handleList)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_verify", Description: "ตรวจสุขภาพ memory (schema/secret/path/dup/index) — รันหลังเขียนหลาย record"}, handleVerify)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_stats", Description: "สถิติ + ความครอบคลุมของแต่ละ harness (ใครเคยเขียน record บ้าง)"}, handleStats)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_sync", Description: "commit (+ pull --rebase + push) ความจำขึ้น remote — ใช้เมื่อจบงานและมี network"}, handleSync)
	mcp.AddTool(s, &mcp.Tool{Name: "memory_trace", Description: "debug retrieval — RRF breakdown (rrf_score + fts/vec/scored ranks) ของ query พร้อม decay/half_life และประมาณการ budget ต่อ layer"}, handleTrace)

	return s
}

// RunStdio serves the 9 tools on the MCP stdio transport (os.Stdin /
// os.Stdout, newline-delimited JSON-RPC — the SDK owns both streams).
func RunStdio(ctx context.Context) error {
	return NewServer().Run(ctx, &mcp.StdioTransport{})
}

// ---------------------------------------------------------------------------
// Stub handlers — P2.3 replaces each with a domain call. They share
// stubResult so callers can see the skeleton is live and which tool ran.
// ---------------------------------------------------------------------------

func handleContext(ctx context.Context, req *mcp.CallToolRequest, args ContextArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_context"), nil, nil
}

func handleSearch(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_search"), nil, nil
}

func handleShow(ctx context.Context, req *mcp.CallToolRequest, args ShowArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_show"), nil, nil
}

func handleAdd(ctx context.Context, req *mcp.CallToolRequest, args AddArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_add"), nil, nil
}

func handleList(ctx context.Context, req *mcp.CallToolRequest, args ListArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_list"), nil, nil
}

func handleVerify(ctx context.Context, req *mcp.CallToolRequest, args VerifyArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_verify"), nil, nil
}

func handleStats(ctx context.Context, req *mcp.CallToolRequest, args StatsArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_stats"), nil, nil
}

func handleSync(ctx context.Context, req *mcp.CallToolRequest, args SyncArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_sync"), nil, nil
}

func handleTrace(ctx context.Context, req *mcp.CallToolRequest, args TraceArgs) (*mcp.CallToolResult, any, error) {
	return stubResult("memory_trace"), nil, nil
}

// stubResult is the single text result every stub returns.
func stubResult(name string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("%s: stub — wired to internal/domain in P2.3", name)},
		},
	}
}
