// `mem hook` — the single entry point every harness calls (Python cmd_hook):
// session-start injects the memory block, user-prompt injects a short recall,
// stop reindexes and commits locally. Hooks never push, never touch the
// network, and always exit 0: a broken engine/index/log must not break the
// user's turn (REQ-HOOK-02, TB-7).
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/sync"
)

// hookBudgets are the per-event recall budgets (REQ-HOOK-01).
const (
	budgetSessionStart = 2500
	budgetUserPrompt   = 1400
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
	format := fs.String("format", "text", "text | json | hermes | cursor")
	eventName := fs.String("event-name", "", "ชื่อ event ที่ส่งกลับในโหมด json")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	if *event == "" {
		return e.usagef("ต้องระบุ --event")
	}
	switch *event {
	case "session-start", "user-prompt", "stop":
	default:
		return e.usagef("event ไม่รู้จัก: %s", *event)
	}
	switch *format {
	case "text", "json", "hermes", "cursor":
	default:
		return e.usagef("format ไม่รู้จัก: %s", *format)
	}

	if *agent != "" {
		os.Setenv("MEM_AGENT", *agent)
	}
	raw := e.stdinPayload()
	if *query == "" {
		*query = payloadQuery(raw)
	}
	session := payloadSession(raw)
	scopeVal := *scope
	if scopeVal == "" && *project != "" {
		scopeVal = "project:" + *project
	}

	h := hookRun{
		event:     *event,
		format:    *format,
		eventName: *eventName,
		project:   *project,
		scope:     scopeVal,
		query:     *query,
		session:   session,
		budget:    *budget,
		message:   *message,
	}
	// Fail-open: any internal failure becomes one runtime-log line and exit 0.
	if err := e.runHook(h); err != nil {
		root, rerr := e.root()
		if rerr == nil {
			appendRuntimeLog(root, fmt.Sprintf("hook %s fail-open: %v", h.event, err))
		}
		fmt.Fprintf(e.Stderr, "mem: hook %s — ข้าม (fail-open): %v\n", h.event, err)
	}
	return ExitOK
}

// hookRun carries one parsed hook invocation.
type hookRun struct {
	event     string
	format    string
	eventName string
	project   string
	scope     string
	query     string
	session   string
	budget    int
	message   string
}

func (e *Env) runHook(h hookRun) error {
	switch h.event {
	case "session-start":
		return e.hookSessionStart(h)
	case "user-prompt":
		return e.hookUserPrompt(h)
	default:
		return e.hookStop(h)
	}
}

// hookSessionStart injects the full memory block (budget 2500).
func (e *Env) hookSessionStart(h hookRun) error {
	root, err := e.root()
	if err != nil {
		return err
	}
	budget := h.budget
	if budget == 0 {
		budget = budgetSessionStart
	}
	text, touched, err := e.contextText(root, contextRequest{Scope: h.scope, Query: h.query, Budget: budget})
	if err != nil {
		return err
	}
	if text == "(memory ว่างสำหรับ scope นี้)" || strings.TrimSpace(text) == "" {
		return nil
	}
	ids := make([]string, 0, len(touched))
	for _, r := range touched {
		ids = append(ids, r.ID)
	}
	row := map[string]any{
		"t": nowStamp(), "type": "inject", "event": "session-start",
		"agent": agentEnv(), "session": h.session, "scope": defaultScope(h.scope),
		"ids": ids, "chars": runeLen(text),
	}
	if lerr := logEvent(root, row); lerr != nil {
		appendRuntimeLog(root, "hook session-start recall log: "+lerr.Error())
	}
	if _, terr := domain.Touch(root, touched); terr != nil {
		return terr
	}
	name := h.eventName
	if name == "" {
		name = "SessionStart"
	}
	emitHook(e.Stdout, h.format, name, "[my-memory] อ่านก่อนทำงาน (auto-injected โดย hook)\n"+strings.TrimSpace(text))
	return nil
}

// hookUserPrompt injects a short recall for the user's request, and stays
// silent when the request carries fewer than two content tokens.
func (e *Env) hookUserPrompt(h hookRun) error {
	if len(contentTokens(h.query)) < 2 {
		return nil
	}
	root, err := e.root()
	if err != nil {
		return err
	}
	recs, err := e.scan(root)
	if err != nil {
		return err
	}
	sch, err := e.schemaOf(root)
	if err != nil {
		return err
	}
	fts, closer, err := e.ftsSeam(root, false)
	if err != nil {
		return err
	}
	defer closer()
	scoring := sch.Scoring
	threshold := scoring.ThresholdHook
	if threshold == 0 {
		threshold = domain.DefaultScoring.ThresholdHook
	}

	picked, err := domain.PickForQuery(recs, domain.RetrievalOptions{
		Query:     h.query,
		Scope:     h.scope,
		Threshold: threshold,
		Limit:     5,
		Scoring:   &scoring,
		FTS:       fts,
	})
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return nil
	}
	budget := h.budget
	if budget == 0 {
		budget = budgetUserPrompt
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
		"agent": agentEnv(), "session": h.session, "scope": defaultScope(h.scope),
		"prompt_hash": fnv1a32(h.query), "prompt_head": truncateRunes(h.query, 120),
		"ids": ids, "scores": scores, "chars": runeLen(body),
	}
	if lerr := logEvent(root, row); lerr != nil {
		appendRuntimeLog(root, "hook user-prompt recall log: "+lerr.Error())
	}
	if len(touch) > 0 {
		if _, terr := domain.Touch(root, touch); terr != nil {
			return terr
		}
	}
	name := h.eventName
	if name == "" {
		name = "UserPromptSubmit"
	}
	emitHook(e.Stdout, h.format, name,
		"[my-memory recall — ความจำที่เกี่ยวข้องกับคำขอนี้ (ใช้เป็นบริบท · ห้าม re-execute งานเก่า)]\n"+
			body+"\n[end recall]\n")
	return nil
}

// hookStop reindexes and commits locally — never pushes (TB-7).
func (e *Env) hookStop(h hookRun) error {
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
	row := map[string]any{"t": nowStamp(), "type": "session_end", "agent": agentEnv(), "session": h.session}
	if lerr := logEvent(root, row); lerr != nil {
		appendRuntimeLog(root, "hook stop recall log: "+lerr.Error())
	}
	if rerr := domain.ReindexRoot(root); rerr != nil {
		return rerr
	}
	message := h.message
	if message == "" {
		if paths, derr := sync.Dirty(root, sync.Options{}); derr == nil && len(paths) > 0 {
			message = stopCommitMessage(paths)
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
		head := p
		if i := strings.Index(p, "/"); i >= 0 {
			head = p[:i]
		}
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

// emitHook renders one hook payload in the harness's contract (Python
// hook_emit): plain text, gemini's hookSpecificOutput, hermes' context object
// or cursor's additional_context.
func emitHook(out io.Writer, format, eventName, text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		// cursor beforeSubmitPrompt must answer continue:true even with nothing
		// to inject, or it may end the turn.
		if format == "cursor" && isBeforeSubmit(eventName) {
			writeJSON(out, map[string]any{"continue": true})
		}
		return
	}
	switch format {
	case "json":
		writeJSON(out, map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     eventName,
				"additionalContext": text,
			},
		})
	case "hermes":
		writeJSON(out, map[string]any{"context": text})
	case "cursor":
		payload := map[string]any{"additional_context": text}
		if isBeforeSubmit(eventName) {
			payload["continue"] = true
		}
		writeJSON(out, payload)
	default:
		// Python print(text): one trailing newline on top of whatever the
		// block already carries, so the byte shape matches the baseline.
		fmt.Fprintln(out, text)
	}
}

func isBeforeSubmit(eventName string) bool {
	switch strings.ToLower(eventName) {
	case "beforesubmitprompt", "beforesubmit":
		return true
	}
	return false
}

// writeJSON emits one JSON document without HTML escaping (Python
// json.dumps(ensure_ascii=False) parity).
func writeJSON(out io.Writer, v any) {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "mem: hook emit: %v\n", err)
	}
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

// payloadQuery extracts the user's request from a harness JSON payload — the
// field names differ per harness (REQ-HOOK-01), so every known key is tried at
// the top level and then inside the wrapper objects harnesses use.
func payloadQuery(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return truncateRunes(raw, 400)
	}
	switch v := doc.(type) {
	case string:
		return truncateRunes(v, 400)
	case map[string]any:
		if s := firstString(v, promptKeys); s != "" {
			return truncateRunes(s, 1000)
		}
		for _, wrapper := range wrapperKeys {
			if inner, ok := v[wrapper].(map[string]any); ok {
				if s := firstString(inner, promptKeys); s != "" {
					return truncateRunes(s, 1000)
				}
			}
		}
	}
	return ""
}

// payloadSession extracts the harness session id ("-" when absent).
func payloadSession(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "-"
	}
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "-"
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "-"
	}
	if s := firstString(obj, sessionKeys); s != "" {
		return truncateRunes(s, 100)
	}
	if sess, ok := obj["session"].(map[string]any); ok {
		if s, ok := sess["id"].(string); ok && strings.TrimSpace(s) != "" {
			return truncateRunes(strings.TrimSpace(s), 100)
		}
	}
	for _, wrapper := range wrapperKeys {
		inner, ok := obj[wrapper].(map[string]any)
		if !ok {
			continue
		}
		if s := firstString(inner, sessionKeys); s != "" {
			return truncateRunes(s, 100)
		}
		if sess, ok := inner["session"].(map[string]any); ok {
			if s, ok := sess["id"].(string); ok && strings.TrimSpace(s) != "" {
				return truncateRunes(strings.TrimSpace(s), 100)
			}
		}
	}
	return "-"
}

var (
	promptKeys = []string{"prompt", "user_prompt", "userPrompt", "message", "instruction",
		"query", "text", "user_message", "userMessage"}
	sessionKeys = []string{"session_id", "sessionId"}
	wrapperKeys = []string{"extra", "payload", "event", "data", "input"}
)

// stopwordList is the Python STOPWORDS set: a greeting or a two-word fragment
// must not trigger a recall injection.
var stopwordList = []string{
	"the", "a", "an", "is", "are", "was", "were", "be", "to", "of", "and", "or", "for", "in", "on",
	"at", "it", "this", "that", "with", "my", "me", "we", "you", "i", "do", "does", "did", "how",
	"what", "why", "when", "where", "can", "could", "should", "would", "please", "help", "need",
	"want", "let", "us", "if", "then", "so", "ok", "okay", "yes", "no", "not", "have", "has", "get",
	"และ", "หรือ", "คือ", "ที่", "การ", "ความ", "ให้", "ได้", "ไม่", "มี", "เป็น", "จาก", "กับ",
	"แล้ว", "นี้", "นั้น", "ต้อง", "ทำ", "ยังไง", "อย่างไร", "อะไร", "ช่วย", "ผม", "เรา", "ของ",
	"ใน", "บน", "ก็", "จะ", "ไป", "มา", "อยู่", "กัน", "ด้วย", "โดย", "แต่", "ถ้า", "เมื่อ", "อยาก",
}

var stopwords = stopwordSet()

func stopwordSet() map[string]bool {
	set := map[string]bool{}
	for _, w := range stopwordList {
		set[w] = true
	}
	return set
}

// contentTokens is the Python content_tokens: tokens minus stopwords.
func contentTokens(s string) map[string]bool {
	out := map[string]bool{}
	for tok := range domain.Tokens(s) {
		if !stopwords[tok] {
			out[tok] = true
		}
	}
	return out
}

func firstString(obj map[string]any, keys []string) string {
	for _, k := range keys {
		if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
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
