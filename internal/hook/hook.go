// Package hook is the adapter layer between harness CLIs and the engine
// (REQ-HOOK-01, REQ-HOOK-02, plan P2.4).
//
// Every harness runs `mem hook --event …` with its own JSON payload on stdin.
// This package owns the parts that differ per harness — event names, output
// formats, payload field names and the per-event budgets — while the engine
// work (context render, recall selection, local commit) stays behind Engine,
// which internal/cli implements.
//
// Contract shared with the Python baseline (harness/mem-hook.sh + cmd_hook):
//
//   - session-start injects the memory block, at most 2500 chars.
//   - user-prompt injects a short recall, at most 1400 chars, and stays silent
//     when the request carries fewer than two content tokens.
//   - stop reindexes and commits locally. It never pushes and never touches
//     the network (TB-7).
//   - Every internal failure is fail-open: one line goes to the gitignored
//     .memory/runtime.log, the note goes to Diagnostics, and the hook exits 0.
//     A broken engine must never break the user's turn.
package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// Per-event recall budgets, in characters (REQ-HOOK-01).
const (
	BudgetSessionStart = 2500
	BudgetUserPrompt   = 1400
)

// Exit codes. Hooks always return ExitOK for runtime failures; ExitUsage is
// reserved for an unknown event or format, which is an installer bug and must
// stay visible to the caller.
const (
	ExitOK    = 0
	ExitUsage = 2
)

// Prefixes the injected blocks with the same banner the Python baseline uses;
// models learned to treat this exact shape as authoritative context.
const (
	sessionPrefix = "[my-memory] อ่านก่อนทำงาน (auto-injected โดย hook)\n"
	recallPrefix  = "[my-memory recall — ความจำที่เกี่ยวข้องกับคำขอนี้ (ใช้เป็นบริบท · ห้าม re-execute งานเก่า)]\n"
	recallSuffix  = "\n[end recall]\n"
)

// Event is one engine hook event.
type Event string

// The three events every harness wires (REQ-HOOK-01).
const (
	SessionStart Event = "session-start"
	UserPrompt   Event = "user-prompt"
	Stop         Event = "stop"
)

// ParseEvent maps a --event value onto an Event.
func ParseEvent(s string) (Event, bool) {
	switch Event(s) {
	case SessionStart, UserPrompt, Stop:
		return Event(s), true
	}
	return "", false
}

// Format is one harness output contract.
type Format string

// The four output formats harnesses accept.
const (
	FormatText   Format = "text"   // stdout as-is (claude, codex)
	FormatJSON   Format = "json"   // hookSpecificOutput.additionalContext (gemini)
	FormatHermes Format = "hermes" // {"context": …} (hermes pre_llm_call)
	FormatCursor Format = "cursor" // {"additional_context": …} (cursor hooks)
)

// ParseFormat maps a --format value onto a Format.
func ParseFormat(s string) (Format, bool) {
	switch Format(s) {
	case FormatText, FormatJSON, FormatHermes, FormatCursor:
		return Format(s), true
	}
	return "", false
}

// Call is the harness context one hook event acts on. The engine receives it
// so its recall-log rows can carry the real session and prompt.
type Call struct {
	Scope   string
	Query   string
	Session string
	Agent   string
	Message string
	Budget  int
}

// Engine is the engine-side work a hook event needs. internal/cli implements
// it; this package owns only the adapter layer, which keeps the harness
// contracts testable without a store, an index or a git repository.
type Engine interface {
	// Context renders the session-start memory block and returns "" when the
	// scope has nothing worth injecting. It logs the inject row and touches
	// the records it surfaced.
	Context(call Call) (string, error)
	// Recall renders the short recall body for the prompt and returns "" when
	// no record scores above the hook threshold.
	Recall(call Call) (string, error)
	// Stop reindexes and commits pending memory changes locally. It never
	// pushes.
	Stop(call Call) error
}

// Options is one hook invocation. Zero values take the documented defaults.
type Options struct {
	// Engine performs the engine-side work. nil is the missing-engine case:
	// the invocation still exits 0 and records the failure.
	Engine Engine

	// Event and Format come from --event / --format.
	Event  Event
	Format Format

	// Payload is the raw JSON a harness wrote to stdin.
	Payload string

	// Agent is the harness identity (--agent); a known harness supplies the
	// default format and event names when the flags are absent.
	Agent string

	// EventName overrides the event name echoed in json/cursor output
	// (--event-name). Empty uses the harness adapter, then the event default.
	EventName string

	// Scope, Query, Session, Message and Budget come from the matching flags;
	// empty Query/Session are extracted from Payload.
	Scope   string
	Query   string
	Session string
	Message string
	Budget  int

	// Root is the memory home that receives the fail-open runtime log.
	// Default: $MEM_HOME, $MY_MEMORY_HOME, then ~/my-memory.
	Root string

	// Diagnostics receives the fail-open note the user sees. Default: discard.
	Diagnostics io.Writer

	// Now stamps runtime-log lines. Default: time.Now.
	Now func() time.Time
}

// Run is the harness-facing entry point: one event, one format, one raw
// harness payload. A nil engine exercises the missing-engine fail-open path.
func Run(eng Engine, event, format, payload string) (string, int) {
	return RunOptions(Options{
		Engine:  eng,
		Event:   Event(event),
		Format:  Format(format),
		Payload: payload,
	})
}

// RunOptions runs one hook invocation and returns the bytes for stdout plus
// the process exit code. Unknown event/format values are ExitUsage; every
// engine or log failure is fail-open with ExitOK.
func RunOptions(opt Options) (string, int) {
	event, ok := ParseEvent(string(opt.Event))
	if !ok {
		return "", ExitUsage
	}
	format := FormatText
	if opt.Format != "" {
		parsed, ok := ParseFormat(string(opt.Format))
		if !ok {
			return "", ExitUsage
		}
		format = parsed
	}
	harness, known := HarnessByName(opt.Agent)
	if opt.Format == "" && known {
		format = harness.Format
	}

	call := Call{
		Scope:   opt.Scope,
		Query:   opt.Query,
		Session: opt.Session,
		Agent:   opt.Agent,
		Message: opt.Message,
	}
	if call.Session == "" {
		call.Session = ExtractSession(opt.Payload, opt.Agent)
	}
	if call.Query == "" {
		call.Query = ExtractPrompt(opt.Payload, opt.Agent)
	}

	eventName := opt.EventName
	if eventName == "" && known {
		eventName = harness.EventName(event)
	}
	if eventName == "" {
		eventName = defaultEventName(event)
	}

	var err error
	switch event {
	case SessionStart:
		call.Budget = budgetOr(opt.Budget, BudgetSessionStart)
		var body string
		if body, err = engineContext(opt.Engine, call); err == nil {
			if text := strings.TrimSpace(body); text != "" {
				return render(format, eventName, sessionPrefix+text), ExitOK
			}
		}
	case UserPrompt:
		// A greeting or a two-word fragment must not disturb the context.
		if len(ContentTokens(call.Query)) < 2 {
			return "", ExitOK
		}
		call.Budget = budgetOr(opt.Budget, BudgetUserPrompt)
		var body string
		if body, err = engineRecall(opt.Engine, call); err == nil {
			return render(format, eventName, recallBlock(body)), ExitOK
		}
	case Stop:
		err = engineStop(opt.Engine, call)
	}
	if err != nil {
		failOpen(opt, event, err)
		return "", ExitOK
	}
	return "", ExitOK
}

// engineContext/engineRecall/engineStop turn a nil engine into an error
// instead of a panic: hooks must never take the session down.
func engineContext(eng Engine, call Call) (string, error) {
	if eng == nil {
		return "", errNoEngine
	}
	return eng.Context(call)
}

func engineRecall(eng Engine, call Call) (string, error) {
	if eng == nil {
		return "", errNoEngine
	}
	return eng.Recall(call)
}

func engineStop(eng Engine, call Call) error {
	if eng == nil {
		return errNoEngine
	}
	return eng.Stop(call)
}

var errNoEngine = fmt.Errorf("engine ไม่พร้อม (ไม่พบ memory home)")

// recallBlock wraps the recall body in the injection fences; an empty body
// stays empty so the format layer can decide whether the harness still needs
// a reply (cursor's beforeSubmitPrompt must answer continue:true).
func recallBlock(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return recallPrefix + body + recallSuffix
}

// render is Emit into a string: the hook's stdout.
func render(format Format, eventName, text string) string {
	var b strings.Builder
	Emit(&b, format, eventName, text)
	return b.String()
}

func defaultEventName(event Event) string {
	switch event {
	case SessionStart:
		return "SessionStart"
	case UserPrompt:
		return "UserPromptSubmit"
	default:
		return "Stop"
	}
}

func budgetOr(given, fallback int) int {
	if given > 0 {
		return given
	}
	return fallback
}

// failOpen records one runtime-log line and reports the skip on Diagnostics.
// An unwritable log is an observability gap, not a silent pass: it is called
// out on the same stream. It never returns an error — the caller has already
// decided the hook exits 0.
func failOpen(opt Options, event Event, err error) {
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	line := fmt.Sprintf("%s hook %s fail-open: %v", now().Format(time.RFC3339), event, err)
	logErr := store.LogRuntime(rootOf(opt), line)
	if opt.Diagnostics == nil {
		return
	}
	fmt.Fprintf(opt.Diagnostics, "mem: hook %s — ข้าม (fail-open): %v\n", event, err)
	if logErr != nil {
		fmt.Fprintf(opt.Diagnostics, "mem: hook %s — runtime log เขียนไม่ได้: %v\n", event, logErr)
	}
}

// runtimeRoot resolves the memory home used for the runtime log. It has to
// work with no engine: the missing-engine case must still leave a trace.
func rootOf(opt Options) string {
	if opt.Root != "" {
		return opt.Root
	}
	return DefaultRoot()
}

// DefaultRoot mirrors the CLI's home resolution order for the runtime log.
func DefaultRoot() string {
	for _, key := range []string{"MEM_HOME", "MY_MEMORY_HOME"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "my-memory")
}
