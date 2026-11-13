// MCP Resources + Prompts (KB-patterns P0) — the read-only surfaces beside the
// 13 frozen tools. Protocol separation: reads the host controls (resources the
// client attaches, prompts the user picks) are host-driven and never become
// tool calls; writes stay tools. The tool surface itself is untouched — same
// 13 names, schemas and handlers (tools.frozen.json contract).
//
// Semantics:
//   - Resources and prompts are strictly read-only: no record is touched (no
//     domain.Touch, no uses bump) — they mirror memory_context's no_touch path
//     exactly. For observability a context read still logs one inject row
//     (type "inject", event "resource"/"bootstrap") and a record read logs a
//     show row, the same rows the tool path writes; recall-log rows never
//     mutate records.
//   - Stdout purity is unchanged: handlers only build results; every stdout
//     byte stays SDK JSON-RPC framing.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Static resource URIs and resource template strings. The context template is
// RFC 6570: `memory://context{?q,scope,budget}` expands to
// memory://context?q=…&scope=…&budget=n (each parameter optional).
const (
	RulesURI           = "memory://rules"
	StatsURI           = "memory://stats"
	RecordURITemplate  = "memory://record/{id}"
	ContextURITemplate = "memory://context{?q,scope,budget}"

	// BootstrapPrompt is the single served prompt name.
	BootstrapPrompt = "memory_bootstrap"
)

// Context resource budget semantics: default 20000 chars, clamped to
// [contextBudgetMin, 20000] (a nonsense budget must not silently become an
// unbounded pack).
const (
	contextBudgetDefault = 20000
	contextBudgetMin     = 500
)

// addResources registers the two static resources, the two resource templates
// and the bootstrap prompt on an already-built server. Static lists: no
// listChanged notifications (the surface never changes at runtime).
func (s *server) addResources(sv *mcpsdk.Server) {
	sv.AddResource(&mcpsdk.Resource{
		URI:         RulesURI,
		Name:        "rules",
		Title:       "Active rules",
		MIMEType:    "text/markdown",
		Description: "Active rule records (id + title + body), sorted by rel path — the must-follow constraints injected every session.",
	}, s.handleRulesResource)

	sv.AddResource(&mcpsdk.Resource{
		URI:         StatsURI,
		Name:        "stats",
		Title:       "Memory stats",
		MIMEType:    "application/json",
		Description: "The memory_stats tool's payload as JSON text: record counts, layers, sources, decay buckets, harness coverage.",
	}, s.handleStatsResource)

	sv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: RecordURITemplate,
		Name:        "record",
		Title:       "One record by id",
		MIMEType:    "text/markdown",
		Description: "One record in full (front-matter + body). {id} resolves by exact id or unique prefix like `mem show`.",
	}, s.handleRecordResource)

	sv.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		URITemplate: ContextURITemplate,
		Name:        "context",
		Title:       "Context pack",
		MIMEType:    "text/markdown",
		Description: "Budgeted context pack (RenderContext) with a provenance header of the included record ids. q=query, scope=scope, budget=chars (default 20000, clamped 500..20000).",
	}, s.handleContextResource)

	sv.AddPrompt(&mcpsdk.Prompt{
		Name:        BootstrapPrompt,
		Title:       "Bootstrap memory context",
		Description: "One user message: the budgeted context pack for a scope/query, an instruction to treat it as authoritative memory context, and the record ids to cite.",
		Arguments: []*mcpsdk.PromptArgument{
			{Name: "scope", Description: "scope filter (global | project:<Name> | repo:<owner/name> | agent:<name>); empty = default scope"},
			{Name: "query", Description: "task query for relevance-ranked selection"},
		},
	}, s.handleBootstrapPrompt)
}

// textResource wraps one text payload as a read result.
func textResource(uri, mime, text string) (*mcpsdk.ReadResourceResult, error) {
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{URI: uri, MIMEType: mime, Text: text}},
	}, nil
}

// handleRulesResource renders every active rule as markdown, sorted by rel
// path — the same selection and ordering the context renderer's rules block
// uses.
func (s *server) handleRulesResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	recs, err := s.scan()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", RulesURI, err)
	}
	var rules []store.Record
	for _, r := range recs {
		if r.Layer == "rule" && r.Status == "active" && r.Sensitivity != "quarantine" {
			rules = append(rules, r)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Rel < rules[j].Rel })

	var b strings.Builder
	fmt.Fprintf(&b, "# my-memory — active rules (%d)\n\n", len(rules))
	for _, r := range rules {
		fmt.Fprintf(&b, "## %s — %s\n\n%s\n\n", r.ID, r.Title, strings.TrimSpace(r.Body))
	}
	return textResource(req.Params.URI, "text/markdown", b.String())
}

// handleStatsResource serves the memory_stats tool's payload as JSON text.
func (s *server) handleStatsResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, _, _, err := s.statsPayload()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StatsURI, err)
	}
	data, merr := json.MarshalIndent(p, "", "  ")
	if merr != nil {
		return nil, fmt.Errorf("%s: %w", StatsURI, merr)
	}
	return textResource(req.Params.URI, "application/json", string(data)+"\n")
}

// handleRecordResource resolves memory://record/{id} like `mem show`: exact id
// or unique-prefix, first match wins. Not found is a loud resource error.
func (s *server) handleRecordResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	uri := req.Params.URI
	id := strings.TrimPrefix(uri, "memory://record/")
	if id == "" || strings.Contains(id, "/") {
		return nil, fmt.Errorf("%s: id ต้องเป็น record id หรือ unique prefix", uri)
	}
	recs, err := s.scan()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	var matches []store.Record
	for _, r := range recs {
		if r.ID == id || strings.HasPrefix(r.ID, id) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%s: ไม่พบ record: %s", uri, id)
	}
	r := matches[0]
	data, rerr := os.ReadFile(r.Path)
	if rerr != nil {
		return nil, fmt.Errorf("%s: %w", uri, rerr)
	}
	// Observability only — mirrors memory_show's show row; never bumps uses.
	s.logEvent(map[string]any{
		"t": nowStamp(), "type": "show", "id": r.ID,
		"agent": s.agentRow(), "session": "-",
	})
	return textResource(uri, "text/markdown", string(data))
}

// handleContextResource serves the budgeted context pack for
// memory://context?q=…&scope=…&budget=n with a provenance header of the
// included ids. Read-only: it mirrors memory_context's no_touch semantics —
// an inject row is logged for the eval/noise loop, no record is touched.
func (s *server) handleContextResource(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	uri := req.Params.URI
	u, uerr := url.Parse(uri)
	if uerr != nil {
		return nil, fmt.Errorf("%s: %w", uri, uerr)
	}
	q := u.Query()
	budget := contextBudgetDefault
	if raw := q.Get("budget"); raw != "" {
		n, aerr := strconv.Atoi(raw)
		if aerr != nil {
			return nil, fmt.Errorf("%s: budget ต้องเป็นตัวเลข (ได้: %s)", uri, raw)
		}
		if n < contextBudgetMin {
			n = contextBudgetMin
		}
		if n > contextBudgetDefault {
			n = contextBudgetDefault
		}
		budget = n
	}
	scope := q.Get("scope")
	query := q.Get("q")

	text, touched, err := s.contextText(scope, query, budget)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	if text != emptyContext {
		ids := make([]string, 0, len(touched))
		for _, r := range touched {
			ids = append(ids, r.ID)
		}
		s.logEvent(map[string]any{
			"t": nowStamp(), "type": "inject", "event": "resource",
			"agent": s.agentRow(), "session": "-",
			"scope": defaultScope(scope),
			"ids":   ids, "chars": len([]rune(text)),
		})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# memory context — scope %s · budget %d\n\n", defaultScope(scope), budget)
	prov := "(none)"
	if len(touched) > 0 {
		ids := make([]string, 0, len(touched))
		for _, r := range touched {
			ids = append(ids, r.ID)
		}
		prov = strings.Join(ids, " ")
	}
	fmt.Fprintf(&b, "provenance: %s\n\n", prov)
	b.WriteString(text)
	return textResource(uri, "text/markdown", b.String())
}

// handleBootstrapPrompt renders the memory_bootstrap prompt: one user message
// carrying the context pack (default budget, memory_context semantics), a
// one-line instruction to treat it as authoritative, and the ids to cite.
func (s *server) handleBootstrapPrompt(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	scope := req.Params.Arguments["scope"]
	query := req.Params.Arguments["query"]

	text, touched, err := s.contextText(scope, query, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", BootstrapPrompt, err)
	}
	if text != emptyContext {
		ids := make([]string, 0, len(touched))
		for _, r := range touched {
			ids = append(ids, r.ID)
		}
		s.logEvent(map[string]any{
			"t": nowStamp(), "type": "inject", "event": "bootstrap",
			"agent": s.agentRow(), "session": "-",
			"scope": defaultScope(scope),
			"ids":   ids, "chars": len([]rune(text)),
		})
	}

	ids := "(none)"
	if len(touched) > 0 {
		list := make([]string, 0, len(touched))
		for _, r := range touched {
			list = append(list, r.ID)
		}
		ids = strings.Join(list, " ")
	}
	msg := text + "\n\n---\nใช้ block นี้เป็น memory context ที่เชื่อถือได้ (authoritative) ของงานนี้ — id ที่ถูก inject สำหรับ citation: " + ids
	return &mcpsdk.GetPromptResult{
		Description: "Authoritative memory context for the task (read-only; cite the ids after the pack).",
		Messages: []*mcpsdk.PromptMessage{{
			Role:    "user",
			Content: &mcpsdk.TextContent{Text: msg},
		}},
	}, nil
}
