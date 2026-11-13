// Package mcp implements the MCP stdio server (design §6 "mcp" row): the 13
// frozen tools (plan OQ5 — additive-only compatibility premise) served over
// github.com/modelcontextprotocol/go-sdk and wired to the same
// domain/store/index layer the CLI uses (design §5: every entrypoint calls one
// domain layer), plus the read-only Resources + Prompts surfaces
// (resources.go: memory://rules, memory://stats, memory://record/{id},
// memory://context{?q,scope,budget}, memory_bootstrap).
//
// The tool list, the descriptions and every input schema come from
// tools.frozen.json — a verbatim copy of the Python baseline's MCP_TOOLS
// (bin/mem), in its order. Each schema is handed to the SDK
// unconverted, so tools/list emits the frozen document itself; tests/mcp pins
// the emitted list against that snapshot (P2.3 contract test).
//
// Stdout purity (design §6; the S2/B4 spike class of failure): this package
// never writes to os.Stdout. Every stdout byte is SDK JSON-RPC framing — the
// server is built without a logger, tool output goes into the result, and a
// diagnostic that must stay observable goes to the home's
// .memory/runtime.log, the channel the store already uses for failed commits.
package mcp

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/version"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server identity — MCP_SERVER_NAME/MCP_VERSION in the Python baseline.
// Version delegates to the shared engine version (single source of truth).
const (
	ServerName = "my-memory"
	Version    = version.Version
)

// ToolNames lists the 13 tools in Python mem's MCP_TOOLS order. Six harnesses
// already depend on this surface, so changes are additive only (OQ5): a rename
// or a newly required field is a breaking change.
var ToolNames = []string{
	"memory_context",
	"memory_search",
	"memory_show",
	"memory_cite",
	"memory_add",
	"memory_list",
	"memory_verify",
	"memory_stats",
	"memory_sync",
	"memory_archive",
	"memory_promote",
	"memory_compact",
	"memory_trace",
}

// Options configures one MCP server instance.
type Options struct {
	// Root is the resolved memory home. Empty means store.FindRoot() (env +
	// upward walk), exactly like the CLI's default resolution.
	Root string
	// Agent names the harness this server speaks for (CLI --agent). Empty
	// falls back to $MEM_AGENT, then to "agent:mcp" as the record source.
	Agent string
}

// NewServer builds the my-memory MCP server with the 13 tools
// registered. Every handler is wired to the domain layer; the initialize
// handshake carries the baseline instructions text.
func NewServer(o Options) (*mcpsdk.Server, error) {
	root, err := resolveRoot(o.Root)
	if err != nil {
		return nil, err
	}
	agent := o.Agent
	if agent == "" {
		agent = os.Getenv("MEM_AGENT")
	}
	srv := &server{root: root, agent: agent}

	s := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: ServerName, Version: Version},
		&mcpsdk.ServerOptions{
			Instructions: instructions(root, agent),
			// The baseline advertised tools only; resources + prompts
			// (KB-patterns P0) are additive: host-controlled reads as
			// resources, bootstrap as a prompt, writes stay tools. All three
			// lists are static — no listChanged notifications, matching the
			// frozen tool capability.
			Capabilities: &mcpsdk.ServerCapabilities{
				Tools:     &mcpsdk.ToolCapabilities{},
				Resources: &mcpsdk.ResourceCapabilities{},
				Prompts:   &mcpsdk.PromptCapabilities{},
			},
		},
	)
	mcpsdk.AddTool(s, mustTool("memory_context"), srv.handleContext)

	mcpsdk.AddTool(s, mustTool("memory_search"), srv.handleSearch)
	mcpsdk.AddTool(s, mustTool("memory_show"), srv.handleShow)
	mcpsdk.AddTool(s, mustTool("memory_cite"), srv.handleCite)
	mcpsdk.AddTool(s, mustTool("memory_add"), srv.handleAdd)
	mcpsdk.AddTool(s, mustTool("memory_list"), srv.handleList)
	mcpsdk.AddTool(s, mustTool("memory_verify"), srv.handleVerify)
	mcpsdk.AddTool(s, mustTool("memory_stats"), srv.handleStats)
	mcpsdk.AddTool(s, mustTool("memory_sync"), srv.handleSync)
	mcpsdk.AddTool(s, mustTool("memory_archive"), srv.handleArchive)
	mcpsdk.AddTool(s, mustTool("memory_promote"), srv.handlePromote)
	mcpsdk.AddTool(s, mustTool("memory_compact"), srv.handleCompact)
	mcpsdk.AddTool(s, mustTool("memory_trace"), srv.handleTrace)

	// Resources + prompts (read-only surfaces beside the frozen tools) — see
	// resources.go.
	srv.addResources(s)

	return s, nil
}

// RunStdio serves the 13 tools on the MCP stdio transport (os.Stdin /
// os.Stdout, newline-delimited JSON-RPC — the SDK owns both streams). A home
// that cannot be resolved fails here, before anything is served, instead of
// turning every tool call into the same error.
func RunStdio(ctx context.Context, o Options) error {
	s, err := NewServer(o)
	if err != nil {
		return err
	}
	return s.Run(ctx, &mcpsdk.StdioTransport{})
}

// resolveRoot resolves the memory home for one server instance.
func resolveRoot(root string) (string, error) {
	if root == "" {
		resolved, err := store.FindRoot()
		if err != nil {
			return "", err
		}
		return resolved, nil
	}
	return store.ExpandPath(root)
}

// ---------------------------------------------------------------------------
// Handshake instructions — the frozen mcp_instructions() text
// ---------------------------------------------------------------------------

// instructionsFmt is Python mcp_instructions() verbatim; the two %s slots are
// the CLI path and the source an add should use.
const instructionsFmt = "นี่คือ my-memory — ความจำถาวรของผู้ใช้ ข้าม harness อ่าน/เขียนชุดเดียวกัน\n" +
	"(private git repo `~/my-memory` · CLI `%s`)\n" +
	"\n" +
	"ก่อนเริ่มงาน: เรียก memory_context ด้วย query = งานที่กำลังทำ (หรือ memory_search)\n" +
	"- `Rules` = กฎที่ต้องปฏิบัติตาม · Lessons/Decisions/Handoffs = ข้อเท็จจริงในอดีต\n" +
	"  ใช้เป็นบริบทเท่านั้น ห้าม re-execute งานเก่า\n" +
	"- ถ้าจำเป็นต้องดูกฎเต็ม: memory_search layer=rule\n" +
	"\n" +
	"เมื่อจบงาน: ถ้าได้บทเรียน/ข้อตัดสินใจ/สถานะงานที่คุ้มค่า ให้เก็บด้วย memory_add\n" +
	"- 1 record = 1 fact · lesson ต้องมี Rule/NOT./Evidence\n" +
	"- source ควรเป็น %s\n" +
	"- ห้ามใส่ secret/token/ข้อมูลส่วนตัว/absolute path ของเครื่อง (ใช้ ~)\n" +
	"- ถ้าเป็นเรื่องเดียวกับของเดิม ให้ memory_search ก่อน แล้วใช้ supersede/supersedes ไม่ใช่เขียนซ้ำ\n" +
	"- ดูแล lifecycle: memory_archive / memory_promote / memory_compact (ส่ง dry_run=true ก่อนเสมอ)\n" +
	"  debug ว่าทำไม record ถึง (ไม่) ถูกเลือก: memory_trace หรือ memory_search {scores: true}\n"

// instructions renders the handshake text for one harness identity.
func instructions(root, agent string) string {
	src := "agent:mcp"
	if agent != "" {
		src = "agent:" + agent
	}
	return fmt.Sprintf(instructionsFmt, memBinPath(root), src)
}

// memBinPath is Python mem_bin_path(): the home's bin/mem as "~/..." when it
// lives under the user's home, the absolute path otherwise.
func memBinPath(root string) string {
	p := filepath.Join(root, "bin", "mem")
	if home, err := os.UserHomeDir(); err == nil {
		if rel, rerr := filepath.Rel(home, p); rerr == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

// ---------------------------------------------------------------------------
// Frozen tool definitions — tools.frozen.json
// ---------------------------------------------------------------------------

// frozenToolsJSON is the Python baseline's MCP_TOOLS, all 13 in its order,
// copied verbatim: name, description and inputSchema.
//
//go:embed tools.frozen.json
var frozenToolsJSON []byte

// FrozenToolsJSON returns the frozen tools/list document this server serves.
// The contract test compares the emitted tools/list against it, and
// tools/test-mcp-contract.sh re-extracts the baseline definition to prove the
// snapshot has not drifted.
func FrozenToolsJSON() []byte { return frozenToolsJSON }

// frozenTool is one entry of tools.frozen.json.
type frozenTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

var (
	frozenOnce sync.Once
	frozenList []frozenTool
	frozenErr  error
)

// frozenTools parses the embedded snapshot once.
func frozenTools() ([]frozenTool, error) {
	frozenOnce.Do(func() {
		if err := json.Unmarshal(frozenToolsJSON, &frozenList); err != nil {
			frozenErr = fmt.Errorf("mcp: parse tools.frozen.json: %w", err)
		}
	})
	return frozenList, frozenErr
}

// mustTool builds the SDK tool definition for one frozen name. A name that is
// missing from the snapshot means registration and the frozen document
// diverged — a programming error, so it panics at startup instead of silently
// serving a partial tool set.
func mustTool(name string) *mcpsdk.Tool {
	tools, err := frozenTools()
	if err != nil {
		panic(err)
	}
	for _, t := range tools {
		if t.Name == name {
			return &mcpsdk.Tool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			}
		}
	}
	panic("mcp: tool missing from tools.frozen.json: " + name)
}
