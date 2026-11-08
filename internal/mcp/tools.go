// Tool handlers — the MCP face of one domain operation each.
//
// Output formatting mirrors the CLI command for the same operation
// (internal/cli): the Python baseline served both entrypoints from one
// `run_cmd(cmd_*)` implementation, so the parity gate compares like with like.
// The domain call is the same in both cases — only the presentation is
// repeated here, because internal/cli imports this package and cannot be
// imported back.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/sync"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// server is the per-session state every handler needs: the resolved home and
// the harness identity that becomes the default record source.
type server struct {
	root  string
	agent string
}

// emptyContext is the renderer's "nothing for this scope" marker.
const emptyContext = "(memory ว่างสำหรับ scope นี้)"

// defaultSource is Python run_cmd's default_source: agent:<harness> when the
// server was started for one, agent:mcp otherwise.
func (s *server) defaultSource() string {
	if s.agent != "" {
		return "agent:" + s.agent
	}
	return "agent:mcp"
}

// agentRow is the agent column of a recall-log row (Python
// os.environ.get("MEM_AGENT", "-")).
func (s *server) agentRow() string {
	if s.agent != "" {
		return s.agent
	}
	return "-"
}

// result renders the Python mcp_tool_call contract: the tool's stdout text,
// with the captured stderr prepended when the call failed, and "(no output)"
// when neither produced anything.
func result(out, errText string, failed bool) (*mcpsdk.CallToolResult, any, error) {
	text := strings.TrimSpace(out)
	if failed {
		text = strings.TrimSpace(strings.TrimSpace(errText) + "\n" + text)
	}
	if text == "" {
		text = strings.TrimSpace(errText)
		if text == "" {
			text = "(no output)"
		}
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
		IsError: failed,
	}, nil, nil
}

// ---------------------------------------------------------------------------
// Home plumbing (the CLI's home helpers, which this package cannot import)
// ---------------------------------------------------------------------------

// scan reads every record under the home (skip rules apply: ops/ append-only
// and "_" prefixed paths are not records).
func (s *server) scan() ([]store.Record, error) {
	recs, err := store.Scan(s.root)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", s.root, err)
	}
	return recs, nil
}

// schemaOf loads the effective schema of the home.
func (s *server) schemaOf() (schema.Schema, error) {
	sch, err := schema.LoadSchema(s.root)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("schema %s: %w", s.root, err)
	}
	return sch, nil
}

// ftsSeam opens the derived index and returns its search function plus a close
// hook. With no index file the seam is nil (selection still works from the
// lexical vector ranks) unless rebuild is set, in which case the index is
// rebuilt first — the Python "if not ctx.db.exists(): reindex(quiet=True)".
func (s *server) ftsSeam(rebuild bool) (func(query string, limit int) ([]string, error), func(), error) {
	path := index.IndexPath(s.root)
	if _, err := os.Stat(path); err != nil {
		if !rebuild {
			return nil, func() {}, nil
		}
		if rerr := domain.ReindexRoot(s.root); rerr != nil {
			return nil, nil, rerr
		}
	}
	ix, err := index.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("index %s: %w", path, err)
	}
	return ix.Search, func() { _ = ix.Close() }, nil
}

// budgets is the taxonomy budgets_chars map with the engine defaults filled in
// (Python ctx.budgets).
func (s *server) budgets() (map[string]int, error) {
	b := make(map[string]int, len(domain.DefaultBudgets))
	for k, v := range domain.DefaultBudgets {
		b[k] = v
	}
	sch, err := s.schemaOf()
	if err != nil {
		return nil, err
	}
	for k, v := range sch.Budgets {
		b[k] = v
	}
	return b, nil
}

// logEvent records one recall-log row; a failure is not fatal for the tool but
// must stay observable, so it lands in the gitignored runtime log.
func (s *server) logEvent(row map[string]any) {
	if err := domain.LogEvent(s.root, row); err != nil {
		_ = store.LogRuntime(s.root, nowStamp()+" mcp: "+firstLine(err.Error()))
	}
}

// nowStamp is the RFC3339 timestamp written into recall-log rows.
func nowStamp() string { return time.Now().Format(time.RFC3339) }

// timeToday is the ISO date used for the touched-today counter.
func timeToday() string { return time.Now().Format("2006-01-02") }

// firstLine returns the first line of s, trimmed (runtime-log summaries).
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// truncateRunes clips s to n runes (Python s[:n]).
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// ---------------------------------------------------------------------------
// memory_context
// ---------------------------------------------------------------------------

func (s *server) handleContext(_ context.Context, _ *mcpsdk.CallToolRequest, in ContextArgs) (*mcpsdk.CallToolResult, any, error) {
	scope := in.Scope
	if in.Project != "" {
		scope = "project:" + in.Project
	}

	text, touched, err := s.contextText(scope, in.Query, in.Budget)
	if err != nil {
		return result("", err.Error(), true)
	}
	if text != emptyContext {
		ids := make([]string, 0, len(touched))
		for _, r := range touched {
			ids = append(ids, r.ID)
		}
		s.logEvent(map[string]any{
			"t": nowStamp(), "type": "inject", "event": "session-start",
			"agent": s.agentRow(), "session": "-",
			"scope": defaultScope(scope),
			"ids":   ids, "chars": utf8.RuneCountInString(text),
		})
	}
	if !in.NoTouch && len(touched) > 0 {
		if _, terr := domain.Touch(s.root, touched); terr != nil {
			return result(text, terr.Error(), true)
		}
	}
	return result(text, "", false)
}

// contextText renders the memory block for one scope and reports the records
// the renderer surfaced (the caller decides whether to touch them).
func (s *server) contextText(scope, query string, budget int) (string, []store.Record, error) {
	recs, err := s.scan()
	if err != nil {
		return "", nil, err
	}
	sch, err := s.schemaOf()
	if err != nil {
		return "", nil, err
	}
	fts, closer, err := s.ftsSeam(false)
	if err != nil {
		return "", nil, err
	}
	defer closer()
	scoring := sch.Scoring
	return domain.RenderContext(recs, domain.ContextOptions{
		Root:    s.root,
		Scope:   scope,
		Query:   query,
		Budget:  budget,
		Scoring: &scoring,
		FTS:     fts,
	})
}

// defaultScope renders the log row's scope column.
func defaultScope(scope string) string {
	if scope == "" {
		return "-"
	}
	return scope
}

// ---------------------------------------------------------------------------
// memory_search
// ---------------------------------------------------------------------------

func (s *server) handleSearch(_ context.Context, _ *mcpsdk.CallToolRequest, in SearchArgs) (*mcpsdk.CallToolResult, any, error) {
	recs, err := s.scan()
	if err != nil {
		return result("", err.Error(), true)
	}
	fts, closer, err := s.ftsSeam(true)
	if err != nil {
		return result("", err.Error(), true)
	}
	defer closer()
	sch, err := s.schemaOf()
	if err != nil {
		return result("", err.Error(), true)
	}
	scoring := sch.Scoring

	var layers []string
	if in.Layer != "" {
		layers = []string{in.Layer}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	bd := &domain.Breakdown{}
	hits, err := domain.SelectFrom(recs, domain.SelectOptions{
		Scope:             in.Scope,
		Layers:            layers,
		Query:             in.Query,
		Limit:             limit,
		IncludeInactive:   in.IncludeInactive,
		IncludeQuarantine: in.IncludeInactive,
		Scoring:           &scoring,
		FTS:               fts,
		Breakdown:         bd,
	})
	if err != nil {
		return result("", err.Error(), true)
	}
	if len(hits) == 0 {
		return result("(ไม่พบ)", "", false)
	}

	var b strings.Builder
	for _, r := range hits {
		first := " "
		if lines := strings.Split(strings.TrimSpace(r.Body), "\n"); len(lines) > 0 {
			first = truncateRunes(lines[0], 150)
		}
		fmt.Fprintf(&b, "%s  [%s/%s] %s\n", r.ID, r.Layer, r.Scope, r.Rel)
		fmt.Fprintf(&b, "    %s\n", first)
		if in.Scores {
			rk := bd.Ranks[r.ID]
			fmt.Fprintf(&b, "    rrf_score=%.4f  fts_rank=%s  vec_rank=%s  score_rank=%s\n",
				bd.RRF[r.ID], rankOr(rk.FTS), rankOr(rk.Vec), rankOr(rk.Scored))
		}
	}
	return result(b.String(), "", false)
}

// rankOr renders one fusion rank ("—" when the record is not in that list).
func rankOr(rank int) string {
	if rank <= 0 {
		return "—"
	}
	return strconv.Itoa(rank)
}

// ---------------------------------------------------------------------------
// memory_show
// ---------------------------------------------------------------------------

func (s *server) handleShow(_ context.Context, _ *mcpsdk.CallToolRequest, in ShowArgs) (*mcpsdk.CallToolResult, any, error) {
	if in.ID == "" {
		return result("", "ต้องส่ง id", true)
	}
	recs, err := s.scan()
	if err != nil {
		return result("", err.Error(), true)
	}
	var matches []store.Record
	for _, r := range recs {
		if r.ID == in.ID || strings.HasPrefix(r.ID, in.ID) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return result("", "mem: ไม่พบ record: "+in.ID, true)
	}
	r := matches[0]
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return result("", err.Error(), true)
	}

	// Usage signal for eval --from-log: `show` means the agent opened a record
	// after it was injected. It does not bump uses (cite is that channel).
	s.logEvent(map[string]any{
		"t": nowStamp(), "type": "show", "id": r.ID,
		"agent": s.agentRow(), "session": "-",
	})
	return result(string(data), "", false)
}

// ---------------------------------------------------------------------------
// memory_cite
// ---------------------------------------------------------------------------

// handleCite records one real use of a record: uses + 1 plus the recall-log
// row the eval loop reads. It is the explicit "this changed my decision"
// signal — opening a record (memory_show) does not bump uses.
func (s *server) handleCite(_ context.Context, _ *mcpsdk.CallToolRequest, in CiteArgs) (*mcpsdk.CallToolResult, any, error) {
	if in.ID == "" {
		return result("", "ต้องส่ง id", true)
	}
	full, err := domain.Cite(s.root, in.ID)
	if err != nil {
		return result("", err.Error(), true)
	}
	s.logEvent(map[string]any{
		"t": nowStamp(), "type": "cite", "id": full,
		"agent": s.agentRow(), "session": "-",
	})
	return result(full, "", false)
}

// ---------------------------------------------------------------------------
// memory_add
// ---------------------------------------------------------------------------

func (s *server) handleAdd(_ context.Context, _ *mcpsdk.CallToolRequest, in AddArgs) (*mcpsdk.CallToolResult, any, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return result("", "ต้องส่ง text", true)
	}
	layer := in.Layer
	if layer == "" {
		layer = "lesson"
	}
	scope := in.Scope
	if scope == "" {
		scope = "global"
	}
	confidence := in.Confidence
	if confidence == "" {
		confidence = "0.9"
	}
	decay := "1.0"
	if in.Decay != 0 {
		decay = pyFloat(in.Decay)
	}
	expires := in.Expires
	if expires == "" {
		expires = "none"
	}
	sensitivity := in.Sensitivity
	if sensitivity == "" {
		sensitivity = "internal"
	}
	source := in.Source
	if source == "" {
		source = s.defaultSource()
	}

	res, err := domain.Add(s.root, store.Record{Layer: layer, Scope: scope, Body: text}, domain.AddOptions{
		Title:       in.Title,
		Tags:        in.Tags,
		Confidence:  confidence,
		Decay:       decay,
		HalfLife:    in.HalfLife,
		Source:      source,
		Evidence:    in.Evidence,
		Supersedes:  in.Supersedes,
		Supersede:   in.Supersede,
		Expires:     expires,
		Sensitivity: sensitivity,
		Force:       in.Force,
	})
	if err != nil {
		return result("", err.Error(), true)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\t%s", res.ID, res.Rel)
	if res.Superseded != "" {
		fmt.Fprintf(&b, "\tsuperseded=%s", res.Superseded)
	}
	if !res.Committed && res.CommitReason != "" {
		// The record is on disk; the commit failed. Observable, non-fatal
		// (REQ-STORE-03) — `memory_sync` picks the file up later.
		_ = store.LogRuntime(s.root, nowStamp()+" mcp: memory_add: git commit ล้มเหลว: "+firstLine(res.CommitReason))
	}
	return result(b.String(), "", false)
}

// pyFloat renders a float the way Python's repr does for the values the add
// schema carries: whole numbers keep one decimal ("1.0"), the rest are short.
func pyFloat(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ---------------------------------------------------------------------------
// memory_list
// ---------------------------------------------------------------------------

func (s *server) handleList(_ context.Context, _ *mcpsdk.CallToolRequest, in ListArgs) (*mcpsdk.CallToolResult, any, error) {
	recs, err := s.scan()
	if err != nil {
		return result("", err.Error(), true)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 40
	}
	var layers []string
	if in.Layer != "" {
		layers = []string{in.Layer}
	}
	hits, err := domain.SelectFrom(recs, domain.SelectOptions{
		Scope:             in.Scope,
		Layers:            layers,
		Limit:             limit,
		IncludeQuarantine: true, // N4: list sees quarantine; search/context hide it
	})
	if err != nil {
		return result("", err.Error(), true)
	}
	if in.Layer != "" {
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].Layer != hits[j].Layer {
				return hits[i].Layer < hits[j].Layer
			}
			return hits[i].ID < hits[j].ID
		})
	}

	var b strings.Builder
	for _, r := range hits {
		fmt.Fprintf(&b, "%s  [%s/%s/%s] %s  %s\n", r.ID, r.Layer, r.Scope, r.Status, createdOf(r), r.Title)
	}
	return result(b.String(), "", false)
}

// ---------------------------------------------------------------------------
// memory_verify
// ---------------------------------------------------------------------------

func (s *server) handleVerify(_ context.Context, _ *mcpsdk.CallToolRequest, _ VerifyArgs) (*mcpsdk.CallToolResult, any, error) {
	problems, warnings, err := domain.Verify(s.root)
	if err != nil {
		return result("", err.Error(), true)
	}
	rep, err := store.ScanWithReport(s.root)
	if err != nil {
		return result("", err.Error(), true)
	}
	n := len(rep.Records) + len(rep.Broken)

	var b strings.Builder
	for _, w := range warnings {
		fmt.Fprintf(&b, "warn: %s\n", w)
	}
	for _, p := range problems {
		fmt.Fprintf(&b, "FAIL: %s\n", p)
	}
	fmt.Fprintf(&b, "\n%d records · %d problems · %d warnings\n", n, len(problems), len(warnings))
	return result(b.String(), "", len(problems) > 0)
}

// ---------------------------------------------------------------------------
// memory_stats
// ---------------------------------------------------------------------------

// decayBuckets mirrors Python DECAY_BUCKETS (label, floor) in order.
var decayBuckets = []struct {
	Label string
	Floor float64
}{
	{"1.0", 1.0},
	{"0.75-0.99", 0.75},
	{"0.5-0.74", 0.5},
	{"0.25-0.49", 0.25},
	{"<0.25", 0.0},
}

func decayBucket(d float64) string {
	for _, b := range decayBuckets {
		if d >= b.Floor {
			return b.Label
		}
	}
	return decayBuckets[len(decayBuckets)-1].Label
}

// rawAgent is the agents.json entry as written on disk: stats needs the hooks
// and mcp wiring, which domain.AgentDef deliberately does not model (render
// only needs the file/mode/budget).
type rawAgent struct {
	Name  string            `json:"name"`
	Mode  string            `json:"mode"`
	File  string            `json:"file"`
	Hooks map[string]string `json:"hooks"`
	MCP   struct {
		Name string `json:"name"`
	} `json:"mcp"`
}

func loadRawAgents(root string) ([]rawAgent, error) {
	path := filepath.Join(root, "schema", "agents.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("agents.json: %w", err)
	}
	var doc struct {
		Agents []rawAgent `json:"agents"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("schema/agents.json: %w", err)
	}
	return doc.Agents, nil
}

// harnessStat is one row of the coverage table.
type harnessStat struct {
	Agent       string
	Mode        string
	Instruction string
	MCP         string
	Writes      int
	Newest      string
	Hooks       string
}

// statsReport is the assembled statistics document (Python statsPayload).
type statsReport struct {
	Records        int
	Active         int
	Superseded     int
	Archived       int
	TouchedToday   int
	ByLayer        map[string]int
	BySource       map[string]int
	NewestBySource map[string]string
	DecayBuckets   map[string]int
	Harness        []harnessStat
	NeverWrote     []string
}

func (s *server) handleStats(_ context.Context, _ *mcpsdk.CallToolRequest, _ StatsArgs) (*mcpsdk.CallToolResult, any, error) {
	recs, err := s.scan()
	if err != nil {
		return result("", err.Error(), true)
	}
	agents, err := loadRawAgents(s.root)
	if err != nil {
		return result("", err.Error(), true)
	}

	p := statsReport{
		Records:        len(recs),
		ByLayer:        map[string]int{},
		BySource:       map[string]int{},
		NewestBySource: map[string]string{},
		DecayBuckets:   map[string]int{},
	}
	for _, b := range decayBuckets {
		p.DecayBuckets[b.Label] = 0
	}
	today := timeToday()
	for _, r := range recs {
		p.ByLayer[r.Layer]++
		src := stringField(r.Front, "source")
		if src == "" {
			src = "?"
		}
		p.BySource[src]++
		if created := createdOf(r); created > p.NewestBySource[src] {
			p.NewestBySource[src] = created
		}
		if lastOf(r) == today {
			p.TouchedToday++
		}
		switch r.Status {
		case "active":
			p.Active++
			p.DecayBuckets[decayBucket(r.Decay)]++
		case "superseded":
			p.Superseded++
		}
	}
	p.Archived = archivedCount(s.root)

	for _, a := range agents {
		state := "—"
		if a.Mode == "render" || a.Mode == "pointer" {
			if data, rerr := os.ReadFile(expandUser(a.File)); rerr != nil {
				state = "no file"
			} else if strings.Contains(string(data), domain.RenderBegin) {
				state = "block ok"
			} else {
				state = "no block"
			}
		}
		hooks := "—"
		if len(a.Hooks) > 0 {
			names := make([]string, 0, len(a.Hooks))
			for k := range a.Hooks {
				names = append(names, k)
			}
			sort.Strings(names)
			hooks = strings.Join(names, ",")
		}
		mcpName := a.MCP.Name
		if mcpName == "" {
			mcpName = "—"
		}
		key := "agent:" + a.Name
		p.Harness = append(p.Harness, harnessStat{
			Agent:       a.Name,
			Mode:        a.Mode,
			Instruction: state,
			MCP:         mcpName,
			Writes:      p.BySource[key],
			Newest:      p.NewestBySource[key],
			Hooks:       hooks,
		})
		if a.Mode != "pull" && p.BySource[key] == 0 {
			p.NeverWrote = append(p.NeverWrote, a.Name)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "records %d · active %d · superseded %d · archived %d · แตะวันนี้ %d\n",
		p.Records, p.Active, p.Superseded, p.Archived, p.TouchedToday)
	fmt.Fprintf(&b, "by layer:   %s\n", sortedCounts(p.ByLayer))
	fmt.Fprintf(&b, "by source:  %s\n", sortedCounts(p.BySource))
	decay := make([]string, 0, len(decayBuckets))
	for _, bk := range decayBuckets {
		if p.DecayBuckets[bk.Label] > 0 {
			decay = append(decay, fmt.Sprintf("%s %d", bk.Label, p.DecayBuckets[bk.Label]))
		}
	}
	if len(decay) == 0 {
		decay = []string{"—"}
	}
	fmt.Fprintf(&b, "decay:      %s\n", strings.Join(decay, " · "))
	fmt.Fprintln(&b, "\nharness coverage (writes = จำนวน record ที่ harness นั้นเป็น source):")
	fmt.Fprintf(&b, "  %-11s %-8s %-11s %-10s %6s  %-10s hooks\n", "agent", "mode", "instruction", "mcp", "writes", "newest")
	for _, h := range p.Harness {
		newest := h.Newest
		if newest == "" {
			newest = "—"
		}
		fmt.Fprintf(&b, "  %-11s %-8s %-11s %-10s %6d  %-10s %s\n", h.Agent, h.Mode, h.Instruction, h.MCP, h.Writes, newest, h.Hooks)
	}
	if len(p.NeverWrote) > 0 {
		fmt.Fprintf(&b, "\nเตือน: %s ยังไม่เคยเขียน record — ตรวจว่า hook/MCP ถูกติดตั้งจริง "+
			"(harness/install.sh) ไม่ใช่แค่ block ใน instruction file\n", strings.Join(p.NeverWrote, ", "))
	}
	return result(b.String(), "", false)
}

// sortedCounts renders "tag count · tag count", highest count first then name.
func sortedCounts(m map[string]int) string {
	type kv struct {
		K string
		V int
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].V != pairs[j].V {
			return pairs[i].V > pairs[j].V
		}
		return pairs[i].K < pairs[j].K
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s %d", p.K, p.V))
	}
	return strings.Join(parts, " · ")
}

// archivedCount counts *.md under _archive/ (normal scans do not see them).
func archivedCount(root string) int {
	dir := filepath.Join(root, "_archive")
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			n++
		}
		return nil
	})
	return n
}

// stringField reads a front-matter string value ("" when absent or mistyped).
func stringField(front map[string]any, key string) string {
	if v, ok := front[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// createdOf returns the record's created date.
func createdOf(r store.Record) string { return stringField(r.Front, "created") }

// lastOf returns the record's last_used date.
func lastOf(r store.Record) string { return stringField(r.Front, "last_used") }

// expandUser resolves a ~-prefixed path (unresolvable paths stay literal).
func expandUser(p string) string {
	resolved, err := store.ExpandPath(p)
	if err != nil {
		return p
	}
	return resolved
}

// ---------------------------------------------------------------------------
// memory_sync
// ---------------------------------------------------------------------------

func (s *server) handleSync(_ context.Context, _ *mcpsdk.CallToolRequest, in SyncArgs) (*mcpsdk.CallToolResult, any, error) {
	if err := domain.ReindexRoot(s.root); err != nil {
		return result("", err.Error(), true)
	}
	message := in.Message
	if message == "" {
		message = "mem: sync (mcp)"
	}

	// The CLI prints sync progress on stderr, and so does this handler's error
	// path: stdout belongs to the JSON-RPC framing.
	var progress strings.Builder
	res, runErr := sync.Run(s.root, sync.Options{
		Message: message,
		NoPush:  in.NoPush,
		Log:     &progress,
	})

	out := reportSync(res)
	if runErr != nil {
		errText := strings.TrimSpace(progress.String() + "\nmem: " + runErr.Error())
		return result(out, errText, true)
	}
	return result(out, "", false)
}

// reportSync renders the human summary of a sync run.
func reportSync(res sync.Result) string {
	var b strings.Builder
	if res.Committed {
		fmt.Fprintf(&b, "committed %d files\n", res.CommittedFiles)
	} else {
		fmt.Fprintln(&b, "ไม่มีอะไรเปลี่ยน")
	}
	for _, note := range res.Notes {
		fmt.Fprintf(&b, "%s\n", note)
	}
	if res.Pushed {
		fmt.Fprintln(&b, "pushed")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// memory_archive — decay/archival scheduler
// ---------------------------------------------------------------------------

// defaultArchiveThreshold is the baseline's `mem archive` default: records
// whose retention score falls below it are archival candidates.
const defaultArchiveThreshold = 0.15

func (s *server) handleArchive(_ context.Context, _ *mcpsdk.CallToolRequest, in ArchiveArgs) (*mcpsdk.CallToolResult, any, error) {
	threshold := defaultArchiveThreshold
	if in.Threshold != nil {
		threshold = *in.Threshold
	}
	res, err := domain.Archive(s.root, domain.ArchiveOptions{Threshold: threshold, DryRun: in.DryRun})
	// A run that fails after moving some files keeps its progress lines: the
	// caller sees which records already left the active tree.
	return result(renderArchive(res), errText(err), err != nil)
}

// renderArchive reproduces the baseline's `mem archive` stdout.
func renderArchive(res domain.ArchiveResult) string {
	var b strings.Builder
	if res.DryRun {
		for _, c := range res.Candidates {
			fmt.Fprintf(&b, "DRY  %s  S=%.3f  [%s/%s] %s\n", c.ID, c.Score, c.Layer, c.Scope, c.Rel)
		}
		fmt.Fprintf(&b, "dry-run: %d candidates (threshold %s) — ยังไม่ย้าย\n",
			len(res.Candidates), pyFloat(res.Threshold))
		return b.String()
	}
	for _, m := range res.Moved {
		fmt.Fprintf(&b, "archived %s (S=%.3f) -> %s\n", m.ID, m.Score, m.Dest)
	}
	fmt.Fprintf(&b, "archive: moved %d/%d candidates (of %d active, threshold %s)\n",
		len(res.Moved), len(res.Candidates), res.Active, pyFloat(res.Threshold))
	return b.String()
}

// ---------------------------------------------------------------------------
// memory_promote — proven lessons become rules
// ---------------------------------------------------------------------------

func (s *server) handlePromote(_ context.Context, _ *mcpsdk.CallToolRequest, in PromoteArgs) (*mcpsdk.CallToolResult, any, error) {
	res, err := domain.Promote(s.root, domain.PromoteOptions{
		MinUses: in.MinUses, Force: in.Force, DryRun: in.DryRun,
	})
	return result(renderPromote(res), errText(err), err != nil)
}

// renderPromote reproduces the baseline's `mem promote` stdout.
func renderPromote(res domain.PromoteResult) string {
	var b strings.Builder
	if res.DryRun {
		for _, c := range res.Candidates {
			fmt.Fprintf(&b, "DRY  %s  uses=%d successes=%d  [%s] %s\n",
				c.ID, c.Uses, c.Successes, c.Scope, c.Rel)
		}
		fmt.Fprintf(&b, "dry-run: %d candidates (min-uses %d, %s) — ยังไม่เขียน\n",
			len(res.Candidates), res.MinUses, res.Mode)
		return b.String()
	}
	for _, p := range res.Promoted {
		fmt.Fprintf(&b, "promoted %s -> %s (%s)\n", p.From, p.To, p.Rel)
	}
	fmt.Fprintf(&b, "promote: %d/%d lessons -> rules (min-uses %d, %s)\n",
		len(res.Promoted), len(res.Candidates), res.MinUses, res.Mode)
	return b.String()
}

// ---------------------------------------------------------------------------
// memory_compact — sleep-time handoff compaction
// ---------------------------------------------------------------------------

func (s *server) handleCompact(_ context.Context, _ *mcpsdk.CallToolRequest, in CompactArgs) (*mcpsdk.CallToolResult, any, error) {
	res, err := domain.Compact(s.root, domain.CompactOptions{
		Days: in.Days, MinGroup: in.MinGroup, DryRun: in.DryRun,
	})
	return result(renderCompact(res), errText(err), err != nil)
}

// renderCompact reproduces the baseline's `mem compact` stdout.
func renderCompact(res domain.CompactResult) string {
	var b strings.Builder
	if res.DryRun {
		for _, g := range res.Groups {
			fmt.Fprintf(&b, "DRY  scope=%s: %d handoffs — %s\n", g.Scope, len(g.IDs), strings.Join(g.IDs, ", "))
		}
		fmt.Fprintf(&b, "dry-run: %d groups (>= %d handoffs, older than %dd) — ยังไม่เขียน\n",
			len(res.Groups), res.MinGroup, res.Days)
		return b.String()
	}
	handoffs := 0
	for _, w := range res.Written {
		fmt.Fprintf(&b, "compacted %d handoffs (%s) -> %s (%s)\n", len(w.IDs), w.Scope, w.ID, w.Rel)
		handoffs += len(w.IDs)
	}
	fmt.Fprintf(&b, "compact: %d groups / %d handoffs -> knowledge (min-group %d, older than %dd)\n",
		len(res.Written), handoffs, res.MinGroup, res.Days)
	return b.String()
}

// errText renders a domain error for result()'s stderr slot.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// memory_trace
// ---------------------------------------------------------------------------

func (s *server) handleTrace(_ context.Context, _ *mcpsdk.CallToolRequest, in TraceArgs) (*mcpsdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.Query) == "" {
		return result("", "ต้องส่ง query", true)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	recs, err := s.scan()
	if err != nil {
		return result("", err.Error(), true)
	}
	fts, closer, err := s.ftsSeam(true)
	if err != nil {
		return result("", err.Error(), true)
	}
	defer closer()
	sch, err := s.schemaOf()
	if err != nil {
		return result("", err.Error(), true)
	}
	scoring := sch.Scoring

	bd := &domain.Breakdown{}
	hits, err := domain.SelectFrom(recs, domain.SelectOptions{
		Scope:     in.Scope,
		Query:     in.Query,
		Limit:     limit,
		Scoring:   &scoring,
		FTS:       fts,
		Breakdown: bd,
	})
	if err != nil {
		return result("", err.Error(), true)
	}

	qt := domain.Tokens(in.Query)
	var b strings.Builder
	fmt.Fprintf(&b, "trace '%s'", in.Query)
	if in.Scope != "" {
		fmt.Fprintf(&b, " · scope=%s", in.Scope)
	}
	fmt.Fprintf(&b, " · limit %d\n", limit)
	if len(qt) > 0 {
		toks := make([]string, 0, len(qt))
		for t := range qt {
			toks = append(toks, t)
		}
		sort.Strings(toks)
		fmt.Fprintf(&b, "  tokens: %s\n", strings.Join(toks, " "))
		rrf := domain.RRF()
		fmt.Fprintf(&b, "  RRF: K=%d · w_fts=%s · w_vector=%s · w_scored=%s (each list top-50)\n",
			int(rrf.K), pyFloat(rrf.WFTS), pyFloat(rrf.WVector), pyFloat(rrf.WScored))
	} else {
		fmt.Fprintln(&b, "  tokens: (ว่าง — ไม่มี q_tokens, RRF ไม่ทำงาน เรียงด้วย score เพียว ๆ)")
	}
	if len(hits) == 0 {
		fmt.Fprintln(&b, "  (ไม่พบ candidate)")
	}
	for i, r := range hits {
		rk := bd.Ranks[r.ID]
		fmt.Fprintf(&b, "%2d. %s  [%s/%s]\n", i+1, r.ID, r.Layer, r.Scope)
		fmt.Fprintf(&b, "      rrf_score=%.4f  fts_rank=%s  vec_rank=%s  score_rank=%s\n",
			bd.RRF[r.ID], rankOr(rk.FTS), rankOr(rk.Vec), rankOr(rk.Scored))
		fmt.Fprintf(&b, "      score=%.2f  decay=%.2f  half_life=%s  S=%.2f  uses=%d\n",
			domain.Score(r, in.Query, in.Scope, scoring), r.Decay,
			halfLifeLabel(domain.HalfLifeDays(r)), domain.RetentionScore(r), r.Uses)
		fmt.Fprintf(&b, "      %s\n", r.Rel)
	}

	caps, err := s.budgets()
	if err != nil {
		return result("", err.Error(), true)
	}
	var active []store.Record
	for _, r := range recs {
		if r.Status == "active" {
			active = append(active, r)
		}
	}
	b.WriteString("\nlayer allocation (chars cap · active recs · est ถ้า include ทั้ง layer, ตัดที่ cap):\n")
	for _, key := range []string{"rules", "project", "lesson", "decision", "handoff", "knowledge"} {
		cap, ok := caps[key]
		if !ok {
			continue
		}
		layer := key
		if key == "rules" {
			layer = "rule"
		}
		n := 0
		est := 0
		for _, r := range active {
			if r.Layer != layer {
				continue
			}
			n++
			est += recordTextLen(r)
		}
		if est > cap {
			est = cap
		}
		fmt.Fprintf(&b, "  %-9s cap %5d · %3d recs · est %5d chars\n", layer, cap, n, est)
	}
	if total, ok := caps["total"]; ok && total > 0 {
		fmt.Fprintf(&b, "  %-9s cap %5d\n", "total", total)
	}
	return result(b.String(), "", false)
}

// recordTextLen is the rune length of the record's full file text (Python
// len(r.text)), the unit the budget estimate uses.
func recordTextLen(r store.Record) int {
	return utf8.RuneCountInString(codec.DumpFront(r.Front) + "\n" + r.Body)
}

// halfLifeLabel renders a half-life the way Python trace does: "inf" or "30d".
func halfLifeLabel(hl float64) string {
	if math.IsInf(hl, 1) {
		return "inf"
	}
	return strconv.FormatFloat(hl, 'g', -1, 64) + "d"
}
