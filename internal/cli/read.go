// Read-side subcommands: context, search, list, show, verify, stats.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// cmdContext renders the memory-injection block for a scope (Python
// cmd_context): rules, the matching project, selected lessons/decisions/
// unexpired handoffs and the rotating knowledge pointers, then touches what it
// surfaced unless --no-touch.
func cmdContext(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	project := fs.String("project", "", "ชื่อ project (สร้าง scope project:<Name>)")
	scope := fs.String("scope", "", "scope ตรง ๆ (global | project:X | repo:o/n | agent:a)")
	query := fs.String("query", "", "คำค้น")
	budget := fs.Int("budget", 0, "budget chars รวม")
	noTouch := fs.Bool("no-touch", false, "ไม่ต้อง bump last_used")
	quiet := fs.Bool("quiet", false, "ไม่ต้องรายงานว่า touch กี่ record")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}

	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	scopeVal := *scope
	if *project != "" {
		scopeVal = "project:" + *project
	}

	text, touched, err := e.contextText(root, contextRequest{Scope: scopeVal, Query: *query, Budget: *budget})
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprintln(e.Stdout, text)

	if text != "(memory ว่างสำหรับ scope นี้)" {
		ids := make([]string, 0, len(touched))
		for _, r := range touched {
			ids = append(ids, r.ID)
		}
		row := map[string]any{
			"t": nowStamp(), "type": "inject", "event": "session-start",
			"agent": agentEnv(), "session": "-",
			"scope": defaultScope(scopeVal),
			"ids":   ids, "chars": runeLen(text),
		}
		if lerr := logEvent(root, row); lerr != nil {
			fmt.Fprintf(e.Stderr, "mem: %v\n", lerr)
		}
	}

	if *noTouch || len(touched) == 0 {
		return ExitOK
	}
	n, terr := domain.Touch(root, touched)
	if terr != nil {
		return e.fail(terr)
	}
	if n > 0 && !*quiet {
		fmt.Fprintf(e.Stderr, "# touched %d records\n", n)
	}
	return ExitOK
}

func defaultScope(scope string) string {
	if scope == "" {
		return "-"
	}
	return scope
}

// cmdSearch scores and ranks records for a query (Python cmd_search).
func cmdSearch(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	scope := fs.String("scope", "", "กรอง scope")
	var layers stringsFlag
	fs.Var(&layers, "layer", "กรอง layer (ซ้ำได้)")
	limit := fs.Int("limit", 8, "จำนวนผลลัพธ์")
	all := fs.Bool("all", false, "รวม superseded/archived และ quarantine ด้วย")
	scores := fs.Bool("scores", false, "แสดง score ของแต่ละผล")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "query"); stop {
		return code
	}
	query := args[0]

	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}
	fts, closer, err := e.ftsSeam(root, true)
	if err != nil {
		return e.fail(err)
	}
	defer closer()
	sch, err := e.schemaOf(root)
	if err != nil {
		return e.fail(err)
	}
	scoring := sch.Scoring

	hits, err := domain.SelectFrom(recs, domain.SelectOptions{
		Scope:             *scope,
		Layers:            layers,
		Query:             query,
		Limit:             *limit,
		IncludeInactive:   *all,
		IncludeQuarantine: *all,
		Scoring:           &scoring,
		FTS:               fts,
	})
	if err != nil {
		return e.fail(err)
	}
	if len(hits) == 0 {
		fmt.Fprintln(e.Stdout, "(ไม่พบ)")
		return ExitError
	}
	for i := range hits {
		r := hits[i]
		first := " "
		if lines := strings.Split(strings.TrimSpace(r.Body), "\n"); len(lines) > 0 {
			first = truncateRunes(lines[0], 150)
		}
		fmt.Fprintf(e.Stdout, "%s  [%s/%s] %s\n", r.ID, r.Layer, r.Scope, r.Rel)
		fmt.Fprintf(e.Stdout, "    %s\n", first)
		if *scores {
			// Additive score (title/tag/body overlap + confidence + recency +
			// uses) — the RRF rank breakdown lives inside SelectFrom and is not
			// exposed yet, so this is the number the ranking starts from.
			fmt.Fprintf(e.Stdout, "    score=%.4f\n", domain.Score(r, query, *scope, scoring))
		}
	}
	return ExitOK
}

// cmdList walks the record set (Python cmd_list). Default ordering comes from
// the retrieval path; --status filters the whole home exactly instead of being
// cut by ranking/limit first.
func cmdList(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	layer := fs.String("layer", "", "กรอง layer")
	scope := fs.String("scope", "", "กรอง scope")
	limit := fs.Int("limit", 40, "จำนวนแถว")
	all := fs.Bool("all", false, "รวม superseded/archived")
	status := fs.String("status", "", "กรอง status ตรง ๆ (active|superseded|archived|promoted|compacted)")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}

	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}

	var hits []store.Record
	if *status != "" {
		for _, r := range recs {
			if r.Status != *status {
				continue
			}
			if *layer != "" && r.Layer != *layer {
				continue
			}
			if *scope != "" && r.Scope != *scope {
				continue
			}
			hits = append(hits, r)
		}
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].Layer != hits[j].Layer {
				return hits[i].Layer < hits[j].Layer
			}
			return hits[i].ID < hits[j].ID
		})
		if len(hits) > *limit {
			hits = hits[:*limit]
		}
	} else {
		var layerList []string
		if *layer != "" {
			layerList = []string{*layer}
		}
		hits, err = domain.SelectFrom(recs, domain.SelectOptions{
			Scope:             *scope,
			Layers:            layerList,
			Query:             "",
			Limit:             *limit,
			IncludeInactive:   *all,
			IncludeQuarantine: true, // N4: list sees quarantine; search/context hide it
		})
		if err != nil {
			return e.fail(err)
		}
		if *layer != "" {
			sort.SliceStable(hits, func(i, j int) bool {
				if hits[i].Layer != hits[j].Layer {
					return hits[i].Layer < hits[j].Layer
				}
				return hits[i].ID < hits[j].ID
			})
		}
	}

	for _, r := range hits {
		fmt.Fprintf(e.Stdout, "%s  [%s/%s/%s] %s  %s\n", r.ID, r.Layer, r.Scope, r.Status, createdOf(r), r.Title)
	}
	return ExitOK
}

func createdOf(r store.Record) string {
	if v, ok := r.Front["created"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// cmdShow prints one record in full, either by id (exact or unique prefix) or
// by path (Python cmd_show).
func cmdShow(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	path := fs.String("path", "", "เปิดไฟล์ตรง ๆ")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if len(args) == 0 && *path == "" {
		return e.usagef("ต้องระบุ <id> หรือ --path")
	}

	if *path != "" {
		data, err := os.ReadFile(expandUser(*path))
		if err != nil {
			return e.fail(err)
		}
		e.Stdout.Write(data)
		return ExitOK
	}

	id := args[0]
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}
	var matches []store.Record
	for _, r := range recs {
		if r.ID == id || strings.HasPrefix(r.ID, id) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		fmt.Fprintf(e.Stderr, "mem: ไม่พบ record: %s\n", id)
		return ExitError
	}
	r := matches[0]
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return e.fail(err)
	}
	e.Stdout.Write(data)

	// Usage signal for eval --from-log: `show` means the agent opened a record
	// after it was injected. It does not bump uses (cite is that channel).
	row := map[string]any{"t": nowStamp(), "type": "show", "id": r.ID, "agent": agentEnv(), "session": "-"}
	if lerr := logEvent(root, row); lerr != nil {
		fmt.Fprintf(e.Stderr, "mem: %v\n", lerr)
	}
	return ExitOK
}

// cmdVerify runs the record-contract check (Python cmd_verify): problems gate
// (exit 1), warnings are advisory.
func cmdVerify(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	problems, warnings, err := domain.Verify(root)
	if err != nil {
		return e.fail(err)
	}
	rep, err := store.ScanWithReport(root)
	if err != nil {
		return e.fail(err)
	}
	n := len(rep.Records) + len(rep.Broken)

	for _, w := range warnings {
		fmt.Fprintf(e.Stdout, "warn: %s\n", w)
	}
	for _, p := range problems {
		fmt.Fprintf(e.Stdout, "FAIL: %s\n", p)
	}
	fmt.Fprintf(e.Stdout, "\n%d records · %d problems · %d warnings\n", n, len(problems), len(warnings))
	if len(problems) > 0 {
		return ExitError
	}
	return ExitOK
}

// --- stats -----------------------------------------------------------------

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
// and mcp wiring, which AgentDef deliberately does not model (render only needs
// the file/mode/budget).
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
	Agent       string `json:"agent"`
	Mode        string `json:"mode"`
	Instruction string `json:"instruction"`
	MCP         string `json:"mcp"`
	Writes      int    `json:"writes"`
	Newest      string `json:"newest"`
	Hooks       string `json:"hooks"`
}

// statsPayload mirrors the Python `stats --json` document key for key.
type statsPayload struct {
	Records        int               `json:"records"`
	Active         int               `json:"active"`
	Superseded     int               `json:"superseded"`
	Archived       int               `json:"archived"`
	TouchedToday   int               `json:"touched_today"`
	ByLayer        map[string]int    `json:"by_layer"`
	BySource       map[string]int    `json:"by_source"`
	NewestBySource map[string]string `json:"newest_by_source"`
	DecayBuckets   map[string]int    `json:"decay_buckets"`
	Harness        []harnessStat     `json:"harness"`
	NeverWrote     []string          `json:"never_wrote"`
}

// cmdStats reports record statistics plus per-harness coverage (Python
// cmd_stats). `--json` emits exactly one JSON document and nothing else.
func cmdStats(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	asJSON := fs.Bool("json", false, "พิมพ์เป็น JSON document เดียว")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}
	agents, err := loadRawAgents(root)
	if err != nil {
		return e.fail(err)
	}

	payload := statsPayload{
		Records:        len(recs),
		ByLayer:        map[string]int{},
		BySource:       map[string]int{},
		NewestBySource: map[string]string{},
		DecayBuckets:   map[string]int{},
	}
	for _, b := range decayBuckets {
		payload.DecayBuckets[b.Label] = 0
	}
	for _, r := range recs {
		payload.ByLayer[r.Layer]++
		src := stringField(r.Front, "source")
		if src == "" {
			src = "?"
		}
		payload.BySource[src]++
		if created := createdOf(r); created > payload.NewestBySource[src] {
			payload.NewestBySource[src] = created
		}
		if lastOf(r) == today() {
			payload.TouchedToday++
		}
		switch r.Status {
		case "active":
			payload.Active++
			payload.DecayBuckets[decayBucket(r.Decay)]++
		case "superseded":
			payload.Superseded++
		}
	}
	payload.Archived = archivedCount(root)

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
		mcp := a.MCP.Name
		if mcp == "" {
			mcp = "—"
		}
		key := "agent:" + a.Name
		payload.Harness = append(payload.Harness, harnessStat{
			Agent:       a.Name,
			Mode:        a.Mode,
			Instruction: state,
			MCP:         mcp,
			Writes:      payload.BySource[key],
			Newest:      payload.NewestBySource[key],
			Hooks:       hooks,
		})
		if a.Mode != "pull" && payload.BySource[key] == 0 {
			payload.NeverWrote = append(payload.NeverWrote, a.Name)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false) // Python json.dumps(ensure_ascii=False) parity
		if err := enc.Encode(payload); err != nil {
			return e.fail(err)
		}
		return ExitOK
	}

	fmt.Fprintf(e.Stdout, "records %d · active %d · superseded %d · archived %d · แตะวันนี้ %d\n",
		payload.Records, payload.Active, payload.Superseded, payload.Archived, payload.TouchedToday)
	fmt.Fprintf(e.Stdout, "by layer:   %s\n", sortedCounts(payload.ByLayer))
	fmt.Fprintf(e.Stdout, "by source:  %s\n", sortedCounts(payload.BySource))
	decay := make([]string, 0, len(decayBuckets))
	for _, b := range decayBuckets {
		if payload.DecayBuckets[b.Label] > 0 {
			decay = append(decay, fmt.Sprintf("%s %d", b.Label, payload.DecayBuckets[b.Label]))
		}
	}
	if len(decay) == 0 {
		decay = []string{"—"}
	}
	fmt.Fprintf(e.Stdout, "decay:      %s\n", strings.Join(decay, " · "))
	fmt.Fprintln(e.Stdout, "\nharness coverage (writes = จำนวน record ที่ harness นั้นเป็น source):")
	fmt.Fprintf(e.Stdout, "  %-11s %-8s %-11s %-10s %6s  %-10s hooks\n", "agent", "mode", "instruction", "mcp", "writes", "newest")
	for _, h := range payload.Harness {
		newest := h.Newest
		if newest == "" {
			newest = "—"
		}
		fmt.Fprintf(e.Stdout, "  %-11s %-8s %-11s %-10s %6d  %-10s %s\n", h.Agent, h.Mode, h.Instruction, h.MCP, h.Writes, newest, h.Hooks)
	}
	if len(payload.NeverWrote) > 0 {
		fmt.Fprintf(e.Stdout, "\nเตือน: %s ยังไม่เคยเขียน record — ตรวจว่า hook/MCP ถูกติดตั้งจริง "+
			"(harness/install.sh) ไม่ใช่แค่ block ใน instruction file\n", strings.Join(payload.NeverWrote, ", "))
	}
	return ExitOK
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
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
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

func lastOf(r store.Record) string {
	if v, ok := r.Front["last_used"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// truncateRunes clips s to n runes (Python s[:n]).
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func expandUser(p string) string {
	resolved, err := store.ExpandPath(p)
	if err != nil {
		return p
	}
	return resolved
}
