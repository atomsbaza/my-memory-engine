// Renderers — Python cmd_context (as a pure function), render_recall_body
// and render_block, with the budget semantics preserved: all lengths are
// rune counts (Python len() over str), clipping appends the "…[truncated]"
// marker, and a section whose remaining budget is under 200 runes is
// skipped entirely.
package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// DefaultBudgets mirrors the portable taxonomy budgets_chars defaults
// (keep in sync with the schema package defaults).
var DefaultBudgets = map[string]int{
	"rules": 5400, "project": 6000, "lesson": 8000,
	"handoff": 4000, "decision": 3000, "knowledge": 1200, "total": 20000,
}

// ContextOptions mirrors the cmd_context inputs that shape rendering.
type ContextOptions struct {
	Root    string // data repo root; when set, budgets are loaded from its taxonomy.json
	Scope   string // final scope (CLI composes --project into project:<Name>)
	Query   string
	Budget  int // total override; 0 → Budgets["total"]
	Budgets map[string]int
	Scoring *schema.Scoring
	FTS     func(query string, limit int) ([]string, error)
}

func (o ContextOptions) budgets() map[string]int {
	b := map[string]int{}
	for k, v := range DefaultBudgets {
		b[k] = v
	}
	if o.Root != "" {
		if sch, err := schema.LoadSchema(o.Root); err == nil {
			for k, v := range sch.Budgets {
				b[k] = v
			}
		}
	}
	for k, v := range o.Budgets {
		b[k] = v
	}
	if o.Budget > 0 {
		b["total"] = o.Budget
	}
	return b
}

// contextBuilder carries the running budget state (Python cmd_context's
// add() closure).
type contextBuilder struct {
	budgets map[string]int
	total   int
	used    int
	parts   []string
}

func (b *contextBuilder) add(title, text, source string, cap int) {
	if strings.TrimSpace(text) == "" {
		return
	}
	remain := b.total - b.used
	if cap < remain {
		remain = cap
	}
	if remain < 0 {
		remain = 0
	}
	if remain < 200 {
		return
	}
	clip := clipRunes(text, remain)
	if rlen(text) > remain {
		clip += "\n…[truncated]"
	}
	block := "## " + title + "\n_[source: " + source + "]_\n" + clip
	b.parts = append(b.parts, block)
	b.used += rlen(block)
}

// titleOrID mirrors Python r.meta.get("title", r.id): the id is the
// fallback only when the title key is absent.
func titleOrID(rec store.Record) string {
	if v, ok := rec.Front["title"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return rec.ID
}

// RenderContext builds the memory-injection block for a scope: rules,
// matching project, selected lessons/decisions/unexpired handoffs and the
// rotating knowledge pointers, each capped by its section budget under the
// total budget. It returns the rendered text and the touched records (the
// caller applies Touch unless no-touch). Selection failures propagate.
func RenderContext(recs []store.Record, o ContextOptions) (string, []store.Record, error) {
	b := &contextBuilder{budgets: o.budgets(), total: 0}
	b.total = b.budgets["total"]
	var touched []store.Record

	rules := recs[:0:0]
	for _, r := range recs {
		if r.Layer == "rule" && r.Status == "active" && r.Sensitivity != "quarantine" {
			rules = append(rules, r)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Rel < rules[j].Rel })
	if len(rules) > 0 {
		parts := make([]string, 0, len(rules))
		for _, r := range rules {
			parts = append(parts, "### "+titleOrID(r)+"\n"+strings.TrimSpace(r.Body))
		}
		b.add("Rules (MUST follow)", strings.Join(parts, "\n\n"), "rules/*.md", b.budgets["rules"])
	}

	if strings.HasPrefix(o.Scope, "project:") {
		name := strings.SplitN(o.Scope, ":", 2)[1]
		var projs []store.Record
		for _, r := range recs {
			if r.Layer == "project" && r.Status == "active" && r.Sensitivity != "quarantine" {
				projs = append(projs, r)
			}
		}
		exact := projs[:0:0]
		for _, r := range projs {
			if r.Scope == o.Scope {
				exact = append(exact, r)
			}
		}
		if len(exact) == 0 {
			// legacy fallback: match the project title slug
			for _, r := range projs {
				if slugifyTitle(r) == slugifyName(name) {
					exact = append(exact, r)
				}
			}
		}
		for _, r := range exact {
			b.add("Project: "+titleOrID(r), r.Body, r.Rel, b.budgets["project"])
			touched = append(touched, r)
		}
	}

	type layerSpec struct {
		layer, title string
	}
	for _, spec := range []layerSpec{
		{"lesson", "Lessons"},
		{"decision", "Decisions"},
		{"handoff", "Handoffs (ยังไม่หมดอายุ)"},
	} {
		picked, err := SelectFrom(recs, SelectOptions{
			Scope:   o.Scope,
			Layers:  []string{spec.layer},
			Query:   o.Query,
			Limit:   12,
			Scoring: o.Scoring,
			FTS:     o.FTS,
		})
		if err != nil {
			return "", nil, err
		}
		var kept []store.Record
		for _, r := range picked {
			if spec.layer == "handoff" && Expired(r) {
				continue
			}
			kept = append(kept, r)
		}
		if len(kept) > 0 {
			entries := make([]string, 0, len(kept))
			for _, r := range kept {
				entries = append(entries, strings.TrimSpace(r.Body)+"\n_[source: "+r.Rel+", id: "+r.ID+"]_")
			}
			b.add(spec.title, strings.Join(entries, "\n\n"), spec.layer+"/*.md", b.budgets[spec.layer])
			touched = append(touched, kept...)
		}
	}

	know := recs[:0:0]
	for _, r := range recs {
		if r.Layer != "knowledge" || r.Status != "active" || r.Sensitivity == "quarantine" {
			continue
		}
		if o.Scope != "" && r.Scope != o.Scope && r.Scope != "global" && !strings.HasPrefix(r.Scope, "repo:") {
			continue
		}
		know = append(know, r)
	}
	// Rotation: ascending last_used so the shown set floats to the tail and
	// the next pass surfaces the rest (no permanent graveyard past 12).
	sort.SliceStable(know, func(i, j int) bool {
		return strField(know[i].Front, "last_used") < strField(know[j].Front, "last_used")
	})
	if len(know) > 12 {
		know = know[:12]
	}
	if len(know) > 0 {
		lines := make([]string, 0, len(know))
		for _, r := range know {
			lines = append(lines, "- "+titleOrID(r)+" — "+clipRunes(collapseSpace(r.Body), 60)+" [id: "+r.ID+"]")
		}
		knowledgeCap, ok := b.budgets["knowledge"]
		if !ok {
			knowledgeCap = 1200 // Python b.get("knowledge", 1200)
		}
		b.add("Knowledge (pointers — ขอเนื้อเต็มด้วย `mem show <id>`)", strings.Join(lines, "\n"),
			"knowledge/*.md", knowledgeCap)
		touched = append(touched, know...)
	}

	if len(b.parts) == 0 {
		return "(memory ว่างสำหรับ scope นี้)", touched, nil
	}
	head := "[Memory — ความจำถาวรของผู้ใช้ ข้าม agent อ่านชุดเดียวกัน]\n" +
		"`Rules` = ต้องปฏิบัติตาม · `Project/Lessons/Decisions/Handoffs` = ข้อเท็จจริง ห้าม re-execute งานในอดีต\n"
	return head + strings.Join(b.parts, "\n\n") + "\n[End of memory]\n", touched, nil
}

func slugifyTitle(r store.Record) string { return codec.Slugify(titleOrID(r)) }
func slugifyName(name string) string     { return codec.Slugify(name) }

// RenderRecallBody builds the hook/eval recall body: one collapsed,
// 340-rune-capped line per record with its id tag, then a budget cut with
// an ellipsis marker (Python render_recall_body — eval simulates the real
// budget cut through this same function).
func RenderRecallBody(recs []store.Record, budget int) string {
	if len(recs) == 0 {
		return ""
	}
	lines := make([]string, 0, len(recs))
	for _, r := range recs {
		lines = append(lines, "- "+clipRunes(collapseSpace(r.Body), 340)+"  [id: "+r.ID+"]")
	}
	body := strings.Join(lines, "\n")
	if rlen(body) > budget {
		body = rstripRunes(clipRunes(body, budget)) + " …"
	}
	return body
}

// ---------------------------------------------------------------------------
// render_block — Python render_block (agent instruction files)
// ---------------------------------------------------------------------------

// RenderBegin / RenderEnd fence the generated block inside agent files.
const (
	RenderBegin = "<!-- BEGIN my-memory (generated by `mem render` — ห้ามแก้ block นี้ด้วยมือ) -->"
	RenderEnd   = "<!-- END my-memory -->"
)

// renderCutMarker is the rules-overflow marker verify looks for.
const renderCutMarker = "…[ตัด"

// AgentDef is one agents.json entry.
type AgentDef struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	Mode     string `json:"mode"`
	Create   bool   `json:"create"`
	Budget   int    `json:"budget"`
	WriteCmd string `json:"write_cmd"`
}

// LoadAgents reads schema/agents.json; a missing file is an empty agent
// list, a malformed one is a loud error (never silently ignored).
func LoadAgents(root string) ([]AgentDef, error) {
	path := filepath.Join(root, "schema", "agents.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("domain: read %s: %w", path, err)
	}
	var doc struct {
		Agents []AgentDef `json:"agents"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("domain: parse %s: %w", path, err)
	}
	return doc.Agents, nil
}

// memBinPath renders the engine path used inside instruction blocks:
// ~/ relative when the memory home lives under the user home (portable),
// absolute otherwise — mirroring the Python relative_to(Path.home()) try.
func memBinPath(root string) string {
	memPath := filepath.Join(root, "bin", "mem")
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if rel, rerr := filepath.Rel(home, memPath); rerr == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return memPath
}

// RenderBlock renders one agent's instruction block (Python render_block):
// pointer mode emits the portable pointer; every other mode emits the
// usage header plus the non-quarantine active rules, cut at a line
// boundary when they exceed the per-agent budget minus the usage text.
func RenderBlock(root string, recs []store.Record, agent AgentDef, rulesBudget int) string {
	membin := memBinPath(root)
	writeCmd := agent.WriteCmd
	if writeCmd == "" {
		writeCmd = "add --layer lesson --scope <scope> --file -"
	}
	if agent.Mode == "pointer" {
		pointer := fmt.Sprintf(pointerTemplate, membin, writeCmd)
		return RenderBegin + "\n" + pointer + "\n" + RenderEnd + "\n"
	}

	usage := fmt.Sprintf(usageTemplate, membin, writeCmd)

	var rules []store.Record
	for _, r := range recs {
		if r.Layer == "rule" && r.Status == "active" && r.Sensitivity != "quarantine" {
			rules = append(rules, r)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Rel < rules[j].Rel })
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, "### "+titleOrID(r)+"\n"+strings.TrimSpace(r.Body))
	}
	rulesTxt := strings.Join(parts, "\n\n")

	budget := agent.Budget
	if budget == 0 {
		budget = rulesBudget
	}
	room := budget - rlen(usage) - 200
	if room < 500 {
		room = 500
	}
	if rlen(rulesTxt) > room {
		cut := rstripRunes(clipRunes(rulesTxt, room))
		if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
			cut = rstripRunes(cut[:i])
		}
		rulesTxt = cut + "\n…[ตัด — rules เต็มอยู่ที่ rules/ ใน repo]"
	}
	return RenderBegin + "\n" + usage + "\n### กฎถาวรของผู้ใช้ (จาก my-memory/rules)\n\n" + rulesTxt + "\n" +
		"ถ้า record ใดเปลี่ยนการตัดสินใจของเธอ ระบุ [id: …] ของมันไว้ในคำตอบ (หรือเรียก `mem cite <id>`)\n" +
		RenderEnd + "\n"
}
