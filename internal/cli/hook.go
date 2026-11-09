// `mem hook` — the single entry point every harness calls (Python cmd_hook):
// session-start injects the memory block, user-prompt injects a short recall,
// stop reindexes and commits locally. The harness-facing adapter layer lives in
// internal/hook; this file owns the engine side — scanning, selection, the
// recall log, touching records and the local commit. Hooks never push, never
// touch the network, and always exit 0: a broken engine/index/log must not
// break the user's turn (REQ-HOOK-02, TB-7).
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/hook"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/sync"
)

// echoIDRe matches the "[id: …]" tags a model echoes back, used by the noise
// metric and by eval --from-log.
var echoIDRe = regexp.MustCompile(`\[id: ([a-z]+\.[0-9a-f]{8,16})\]`)

func cmdHook(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	event := fs.String("event", "", "session-start | user-prompt | stop")
	agent := fs.String("agent", "", "ชื่อ harness (ใช้ในข้อความ/commit message)")
	query := fs.String("query", "", "คำค้น (ถ้าไม่ส่ง จะอ่าน JSON payload จาก stdin)")
	project := fs.String("project", "", "ชื่อ project")
	scope := fs.String("scope", "", "scope ตรง ๆ")
	budget := fs.Int("budget", 0, "budget chars ของ recall (default 2500 / 1400)")
	message := fs.String("message", "", "commit message สำหรับ event=stop")
	format := fs.String("format", "", "text | json | hermes | cursor (default ตาม harness)")
	eventName := fs.String("event-name", "", "ชื่อ event ที่ส่งกลับในโหมด json")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	if *event == "" {
		return e.usagef("ต้องระบุ --event")
	}
	ev, ok := hook.ParseEvent(*event)
	if !ok {
		return e.usagef("event ไม่รู้จัก: %s", *event)
	}
	var formatFlag hook.Format
	if *format != "" {
		parsed, ok := hook.ParseFormat(*format)
		if !ok {
			return e.usagef("format ไม่รู้จัก: %s", *format)
		}
		formatFlag = parsed
	}
	if *agent != "" {
		os.Setenv("MEM_AGENT", *agent)
	}
	scopeVal := *scope
	if scopeVal == "" && *project != "" {
		scopeVal = "project:" + *project
	}
	opt := hook.Options{
		Engine:      hookEngine{env: e},
		Event:       ev,
		Format:      formatFlag,
		Payload:     e.stdinPayload(),
		Agent:       *agent,
		EventName:   *eventName,
		Scope:       scopeVal,
		Query:       *query,
		Message:     *message,
		Budget:      *budget,
		Diagnostics: e.Stderr,
	}
	if root, err := e.root(); err == nil {
		opt.Root = root
	}
	out, code := hook.RunOptions(opt)
	if out != "" {
		io.WriteString(e.Stdout, out)
	}
	return code
}

// hookEngine implements hook.Engine: the engine work each event needs. Wire it
// here instead of in internal/hook so the adapter layer stays free of the
// store, the index and git — and therefore testable on its own.
type hookEngine struct {
	env *Env
}

// Context injects the full memory block (budget 2500).
func (h hookEngine) Context(call hook.Call) (string, error) {
	e := h.env
	root, err := e.root()
	if err != nil {
		return "", err
	}
	budget := call.Budget
	if budget == 0 {
		budget = hook.BudgetSessionStart
	}
	text, touched, err := e.contextText(root, contextRequest{Scope: call.Scope, Query: call.Query, Budget: budget})
	if err != nil {
		return "", err
	}
	if text == "(memory ว่างสำหรับ scope นี้)" || strings.TrimSpace(text) == "" {
		return "", nil
	}
	ids := make([]string, 0, len(touched))
	for _, r := range touched {
		ids = append(ids, r.ID)
	}
	row := map[string]any{
		"t": nowStamp(), "type": "inject", "event": "session-start",
		"agent": agentEnv(), "session": call.Session, "scope": defaultScope(call.Scope),
		"ids": ids, "chars": runeLen(text),
	}
	if lerr := logEvent(root, row); lerr != nil {
		appendRuntimeLog(root, "hook session-start recall log: "+lerr.Error())
	}
	if _, terr := domain.Touch(root, touched); terr != nil {
		return "", terr
	}
	return text, nil
}

// Recall injects a short recall for the user's request (budget 1400). The
// fewer-than-two-content-tokens guard lives in internal/hook, before the engine
// is touched at all.
func (h hookEngine) Recall(call hook.Call) (string, error) {
	e := h.env
	root, err := e.root()
	if err != nil {
		return "", err
	}
	recs, err := e.scan(root)
	if err != nil {
		return "", err
	}
	sch, err := e.schemaOf(root)
	if err != nil {
		return "", err
	}
	fts, closer, err := e.ftsSeam(root, false)
	if err != nil {
		return "", err
	}
	defer closer()
	scoring := sch.Scoring
	threshold := scoring.ThresholdHook
	if threshold == 0 {
		threshold = domain.DefaultScoring.ThresholdHook
	}

	picked, err := domain.PickForQuery(recs, domain.RetrievalOptions{
		Query:     call.Query,
		Scope:     call.Scope,
		Threshold: threshold,
		Limit:     5,
		Scoring:   &scoring,
		FTS:       fts,
		Indexed:   fts != nil,
	})
	if err != nil {
		return "", err
	}
	if len(picked) == 0 {
		return "", nil
	}
	budget := call.Budget
	if budget == 0 {
		budget = hook.BudgetUserPrompt
	}
	body := domain.RenderRecallBody(pickedRecords(picked), budget)
	ids := make([]string, 0, len(picked))
	scores := make([]float64, 0, len(picked))
	touch := make([]store.Record, 0, len(picked))
	for _, p := range picked {
		ids = append(ids, p.Rec.ID)
		scores = append(scores, round2(p.Score))
		if lastOf(p.Rec) != today() {
			touch = append(touch, p.Rec)
		}
	}
	row := map[string]any{
		"t": nowStamp(), "type": "inject", "event": "user-prompt",
		"agent": agentEnv(), "session": call.Session, "scope": defaultScope(call.Scope),
		"prompt_hash": fnv1a32(call.Query), "prompt_head": truncateRunes(call.Query, 120),
		"ids": ids, "scores": scores, "chars": runeLen(body),
	}
	if lerr := logEvent(root, row); lerr != nil {
		appendRuntimeLog(root, "hook user-prompt recall log: "+lerr.Error())
	}
	if len(touch) > 0 {
		if _, terr := domain.Touch(root, touch); terr != nil {
			return "", terr
		}
	}
	return body, nil
}

// Stop reindexes and commits locally — never pushes (TB-7).
func (h hookEngine) Stop(call hook.Call) error {
	e := h.env
	root, err := e.root()
	if err != nil {
		return err
	}
	repo, err := sync.IsRepo(root, sync.Options{})
	if err != nil {
		return err
	}
	if !repo {
		return nil
	}
	row := map[string]any{"t": nowStamp(), "type": "session_end", "agent": agentEnv(), "session": call.Session}
	if lerr := logEvent(root, row); lerr != nil {
		appendRuntimeLog(root, "hook stop recall log: "+lerr.Error())
	}
	if rerr := domain.ReindexRoot(root); rerr != nil {
		return rerr
	}
	message := call.Message
	if message == "" {
		if paths, derr := sync.Dirty(root, sync.Options{}); derr == nil && len(paths) > 0 {
			message = stopCommitMessage(paths) + stopCommitBody(paths)
		}
	}
	n, cerr := sync.CommitAll(root, message, sync.Options{})
	if cerr != nil {
		return cerr
	}
	if n > 0 {
		fmt.Fprintf(e.Stderr, "mem: hook stop — commit %d ไฟล์ (ยังไม่ push · รัน `mem sync` เมื่อพร้อม)\n", n)
	}
	return nil
}

// stopCommitMessage summarises the changed top-level groups the way the hook
// contract does: "mem: hook stop (agent) — lessons×3, rules×1".
func stopCommitMessage(paths []string) string {
	groups := map[string]int{}
	order := []string{}
	for _, p := range paths {
		// Separator-safe: git porcelain paths are slash-separated, but
		// callers can also hand us OS-native rels.
		head := strings.Split(filepath.ToSlash(p), "/")[0]
		if _, seen := groups[head]; !seen {
			order = append(order, head)
		}
		groups[head]++
	}
	pairs := make([]struct {
		g string
		n int
	}, 0, len(groups))
	for _, g := range order {
		pairs = append(pairs, struct {
			g string
			n int
		}{g, groups[g]})
	}
	// Count desc, then name (Python ranks the same way).
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].g < pairs[j].g
	})
	parts := make([]string, 0, 4)
	for i, p := range pairs {
		if i >= 4 {
			parts = append(parts, "…")
			break
		}
		if p.n == 1 {
			parts = append(parts, p.g)
		} else {
			parts = append(parts, fmt.Sprintf("%s×%d", p.g, p.n))
		}
	}
	msg := fmt.Sprintf("mem: hook stop (%s) — %s", agentEnv(), strings.Join(parts, ", "))
	return truncateRunes(msg, 110)
}

// stopCommitBody is the commit body the baseline writes under the subject:
// the changed paths, first 30 then a "…+N more" tail. It is prefixed with the
// blank line that makes git commit treat the summary as the subject.
func stopCommitBody(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	shown := paths
	if len(shown) > 30 {
		shown = shown[:30]
	}
	body := strings.Join(shown, "\n")
	if len(paths) > 30 {
		body += fmt.Sprintf("\n…+%d more", len(paths)-30)
	}
	return "\n\n" + body
}

func pickedRecords(picked []domain.ScoredRecord) []store.Record {
	out := make([]store.Record, 0, len(picked))
	for _, p := range picked {
		out = append(out, p.Rec)
	}
	return out
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// fnv1a32 hashes the prompt for --from-log pair proposals (Python fnv1a32).
func fnv1a32(s string) string {
	h := uint32(0x811c9dc5)
	for _, b := range []byte(s) {
		h ^= uint32(b)
		h *= 0x01000193
	}
	return fmt.Sprintf("fnv1a:%08x", h)
}
