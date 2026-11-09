// Hook adapter tests: the four output formats, the six harness entries,
// per-harness payload extraction, the per-event budgets, silence on tiny
// prompts and the fail-open contract (AC-P2-10, AC-P2-11).
package hook

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeEngine records what the adapter passed down and returns canned bodies.
// It is the whole point of the Engine seam: harness contracts are testable
// without a store, an index or a git repository.
type fakeEngine struct {
	context    string
	recall     string
	contextErr error
	recallErr  error
	stopErr    error

	contextCalls []Call
	recallCalls  []Call
	stopCalls    []Call
}

func (f *fakeEngine) Context(call Call) (string, error) {
	f.contextCalls = append(f.contextCalls, call)
	return f.context, f.contextErr
}

func (f *fakeEngine) Recall(call Call) (string, error) {
	f.recallCalls = append(f.recallCalls, call)
	return f.recall, f.recallErr
}

func (f *fakeEngine) Stop(call Call) error {
	f.stopCalls = append(f.stopCalls, call)
	return f.stopErr
}

// sessionJSON is the strict shape gemini requires on stdout.
type sessionJSON struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

type hermesJSON struct {
	Context string `json:"context"`
}

type cursorJSON struct {
	AdditionalContext string `json:"additional_context"`
	Continue          *bool  `json:"continue"`
}

// decodeStrict parses exactly one JSON document, rejects unknown fields and
// fails on trailing bytes — a plain json.Unmarshal would accept both.
func decodeStrict(t *testing.T, raw string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("strict JSON decode failed: %v\n%s", err, raw)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("trailing content after first JSON document: %v\n%s", err, raw)
	}
}

func TestHarnessRegistryHasSixEntries(t *testing.T) {
	want := map[string]Format{
		"claude":       FormatText,
		"codex":        FormatText,
		"gemini":       FormatJSON,
		"opencode":     FormatText,
		"cursor-agent": FormatCursor,
		"hermes":       FormatHermes,
	}
	got := Harnesses()
	if len(got) != len(want) {
		t.Fatalf("Harnesses() = %d entries, want %d", len(got), len(want))
	}
	for _, h := range got {
		format, ok := want[h.Name]
		if !ok {
			t.Fatalf("unexpected harness %q", h.Name)
		}
		if h.Format != format {
			t.Errorf("%s format = %q, want %q", h.Name, h.Format, format)
		}
		if h.EventName(UserPrompt) == "" {
			t.Errorf("%s has no user-prompt event name", h.Name)
		}
		if _, ok := ParseFormat(string(h.Format)); !ok {
			t.Errorf("%s declares unknown format %q", h.Name, h.Format)
		}
		if _, ok := HarnessByName(strings.ToUpper(h.Name)); !ok {
			t.Errorf("%s not found case-insensitively", h.Name)
		}
	}
	// The registry is a copy: mutating it must not change the package state.
	got[0].Name = "mutated"
	if Harnesses()[0].Name == "mutated" {
		t.Fatal("Harnesses() returned the package slice, not a copy")
	}
	if _, ok := HarnessByName("does-not-exist"); ok {
		t.Fatal("unknown harness resolved")
	}
}

func TestFormatContracts(t *testing.T) {
	eng := &fakeEngine{context: "Rules\nbody"}
	run := func(format Format) string {
		t.Helper()
		root := t.TempDir()
		out, code := RunOptions(Options{
			Engine: eng, Event: SessionStart, Format: format, Root: root,
		})
		if code != ExitOK {
			t.Fatalf("%s: exit = %d", format, code)
		}
		return out
	}

	text := run(FormatText)
	if !strings.HasPrefix(text, "[my-memory] อ่านก่อนทำงาน") || !strings.Contains(text, "body") {
		t.Fatalf("text format = %q", text)
	}

	var js sessionJSON
	decodeStrict(t, run(FormatJSON), &js)
	if js.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("json hookEventName = %q", js.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(js.HookSpecificOutput.AdditionalContext, "[my-memory]") {
		t.Fatalf("json additionalContext = %q", js.HookSpecificOutput.AdditionalContext)
	}

	var hm hermesJSON
	decodeStrict(t, run(FormatHermes), &hm)
	if !strings.Contains(hm.Context, "body") {
		t.Fatalf("hermes context = %q", hm.Context)
	}

	var cu cursorJSON
	decodeStrict(t, run(FormatCursor), &cu)
	if !strings.Contains(cu.AdditionalContext, "body") {
		t.Fatalf("cursor additional_context = %q", cu.AdditionalContext)
	}
	if cu.Continue != nil {
		t.Fatalf("cursor SessionStart must not carry continue: %q", run(FormatCursor))
	}
}

// TestGeminiJSONIsStrict proves gemini's output parses under a strict decoder
// and that its event name comes from the harness adapter, not a shared default.
func TestGeminiJSONIsStrict(t *testing.T) {
	eng := &fakeEngine{context: "block"}
	out, code := RunOptions(Options{
		Engine: eng, Event: SessionStart, Agent: "gemini", Root: t.TempDir(),
	})
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var doc sessionJSON
	decodeStrict(t, out, &doc)
	if doc.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("gemini event name = %q", doc.HookSpecificOutput.HookEventName)
	}

	eng.recall = "related memory"
	out, code = RunOptions(Options{
		Engine: eng, Event: UserPrompt, Agent: "gemini",
		Payload: `{"prompt":"alpha bravo charlie"}`, Root: t.TempDir(),
	})
	if code != ExitOK {
		t.Fatalf("user-prompt exit = %d", code)
	}
	decodeStrict(t, out, &doc)
	if doc.HookSpecificOutput.HookEventName != "BeforeAgent" {
		t.Fatalf("gemini prompt event name = %q, want BeforeAgent", doc.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(doc.HookSpecificOutput.AdditionalContext, "[end recall]") {
		t.Fatalf("gemini recall missing fences: %q", doc.HookSpecificOutput.AdditionalContext)
	}
}

// TestHarnessDrivesFormat: each entry's declaration, not a shared flag, picks
// the output contract (AC-P2-10 discriminating control).
func TestHarnessDrivesFormat(t *testing.T) {
	eng := &fakeEngine{context: "block"}
	for _, h := range Harnesses() {
		t.Run(h.Name, func(t *testing.T) {
			out, code := RunOptions(Options{
				Engine: eng, Event: SessionStart, Agent: h.Name, Root: t.TempDir(),
			})
			if code != ExitOK {
				t.Fatalf("exit = %d", code)
			}
			switch h.Format {
			case FormatJSON:
				var doc sessionJSON
				decodeStrict(t, out, &doc)
				if doc.HookSpecificOutput.AdditionalContext == "" {
					t.Fatalf("%s json missing context", h.Name)
				}
			case FormatHermes:
				var doc hermesJSON
				decodeStrict(t, out, &doc)
				if doc.Context == "" {
					t.Fatalf("%s hermes missing context", h.Name)
				}
			case FormatCursor:
				var doc cursorJSON
				decodeStrict(t, out, &doc)
				if doc.AdditionalContext == "" {
					t.Fatalf("%s cursor missing context", h.Name)
				}
			default:
				if !strings.HasPrefix(out, "[my-memory]") {
					t.Fatalf("%s text = %q", h.Name, out)
				}
			}
		})
	}
}

// TestCursorBeforeSubmitContinue: cursor's prompt hook must keep the turn
// alive even when there is nothing to inject.
func TestCursorBeforeSubmitContinue(t *testing.T) {
	var sb strings.Builder
	Emit(&sb, FormatCursor, "beforeSubmitPrompt", "")
	var doc cursorJSON
	decodeStrict(t, sb.String(), &doc)
	if doc.Continue == nil || !*doc.Continue {
		t.Fatalf("empty cursor beforeSubmitPrompt = %q, want continue:true", sb.String())
	}

	// Empty recall under cursor's prompt hook still answers continue.
	eng := &fakeEngine{}
	out, code := RunOptions(Options{
		Engine: eng, Event: UserPrompt, Agent: "cursor-agent",
		Payload: `{"prompt":"alpha bravo charlie"}`, Root: t.TempDir(),
	})
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	doc = cursorJSON{}
	decodeStrict(t, out, &doc)
	if doc.Continue == nil || !*doc.Continue {
		t.Fatalf("empty recall = %q, want continue:true", out)
	}
}

// TestPayloadExtractionPerHarness: the query/session come from each adapter's
// own keys, including the wrapper objects harnesses nest them in.
func TestPayloadExtractionPerHarness(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		query   string
		session string
	}{
		{"claude", `{"session_id":"s-claude","prompt":"alpha bravo charlie"}`, "alpha bravo charlie", "s-claude"},
		{"codex", `{"session_id":"s-codex","prompt":"bravo charlie delta"}`, "bravo charlie delta", "s-codex"},
		{"gemini", `{"session_id":"s-gemini","prompt":"charlie delta echo"}`, "charlie delta echo", "s-gemini"},
		{"opencode", `{"session_id":"s-open","prompt":"delta echo foxtrot"}`, "delta echo foxtrot", "s-open"},
		{"cursor-agent", `{"conversation_id":"s-cursor","user_message":"echo foxtrot golf"}`, "echo foxtrot golf", "s-cursor"},
		{"hermes", `{"session_id":"s-hermes","message":"foxtrot golf hotel"}`, "foxtrot golf hotel", "s-hermes"},
		// The adapter's own key wins over the shared list: hermes sends message,
		// and a stray prompt field must not shadow it.
		{"hermes", `{"prompt":"shared key","message":"adapter key wins"}`, "adapter key wins", "-"},
		// wrapper objects
		{"claude", `{"extra":{"prompt":"nested prompt here"}}`, "nested prompt here", "-"},
		{"hermes", `{"payload":{"message":"nested message here","session_id":"s-nested"}}`, "nested message here", "s-nested"},
		{"cursor-agent", `{"event":{"user_message":"event wrapper text","conversation_id":"s-ev"}}`, "event wrapper text", "s-ev"},
		// shared keys, unknown harness
		{"", `{"userPrompt":"shared key text"}`, "shared key text", "-"},
		{"", `{"session":{"id":"s-object"}}`, "", "s-object"},
		// non-JSON payload is the raw request, JSON string is the request
		{"", `alpha bravo charlie`, "alpha bravo charlie", "-"},
		{"", `"quoted bravo charlie"`, "quoted bravo charlie", "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.query, func(t *testing.T) {
			if got := ExtractPrompt(tc.payload, tc.name); got != tc.query {
				t.Errorf("ExtractPrompt(%s) = %q, want %q", tc.payload, got, tc.query)
			}
			if got := ExtractSession(tc.payload, tc.name); got != tc.session {
				t.Errorf("ExtractSession(%s) = %q, want %q", tc.payload, got, tc.session)
			}
		})
	}
}

// TestRunEntryPoint pins the minimal harness-facing signature and rejects an
// unknown event or format as a usage error (not a fail-open).
func TestRunEntryPoint(t *testing.T) {
	eng := &fakeEngine{context: "block"}
	out, code := Run(eng, "session-start", "text", "")
	if code != ExitOK || !strings.HasPrefix(out, "[my-memory]") {
		t.Fatalf("Run() = (%q, %d)", out, code)
	}
	if _, code := Run(eng, "nope", "text", ""); code != ExitUsage {
		t.Fatalf("unknown event exit = %d, want %d", code, ExitUsage)
	}
	if _, code := Run(eng, "session-start", "nope", ""); code != ExitUsage {
		t.Fatalf("unknown format exit = %d, want %d", code, ExitUsage)
	}
}

func TestBudgetsPerEvent(t *testing.T) {
	eng := &fakeEngine{context: "block", recall: "related"}
	root := t.TempDir()
	if _, code := RunOptions(Options{Engine: eng, Event: SessionStart, Root: root}); code != ExitOK {
		t.Fatalf("session-start exit = %d", code)
	}
	if got := eng.contextCalls[0].Budget; got != BudgetSessionStart {
		t.Fatalf("session-start budget = %d, want %d", got, BudgetSessionStart)
	}
	if _, code := RunOptions(Options{Engine: eng, Event: SessionStart, Budget: 100, Root: root}); code != ExitOK {
		t.Fatalf("session-start exit = %d", code)
	}
	if got := eng.contextCalls[1].Budget; got != 100 {
		t.Fatalf("explicit budget = %d, want 100", got)
	}
	if _, code := RunOptions(Options{
		Engine: eng, Event: UserPrompt, Payload: `{"prompt":"alpha bravo charlie"}`, Root: root,
	}); code != ExitOK {
		t.Fatalf("user-prompt exit = %d", code)
	}
	if got := eng.recallCalls[0].Budget; got != BudgetUserPrompt {
		t.Fatalf("user-prompt budget = %d, want %d", got, BudgetUserPrompt)
	}
}

func TestTinyPromptStaysSilent(t *testing.T) {
	for _, payload := range []string{"", `{"prompt":"hi"}`, `{"prompt":"ok thanks"}`, `{"prompt":"please help"}`} {
		eng := &fakeEngine{recall: "should never be used"}
		out, code := RunOptions(Options{
			Engine: eng, Event: UserPrompt, Payload: payload, Root: t.TempDir(),
		})
		if code != ExitOK {
			t.Fatalf("%q: exit = %d", payload, code)
		}
		if out != "" {
			t.Fatalf("%q: injected for a tiny prompt: %q", payload, out)
		}
		if len(eng.recallCalls) != 0 {
			t.Fatalf("%q: engine touched for a tiny prompt", payload)
		}
	}
}

// TestFailOpenMissingEngine: no engine must still exit 0 and leave a trace.
func TestFailOpenMissingEngine(t *testing.T) {
	for _, tc := range []struct {
		event   Event
		payload string
	}{
		{SessionStart, ""},
		{UserPrompt, `{"prompt":"alpha bravo charlie"}`},
		{Stop, ""},
	} {
		t.Run(string(tc.event), func(t *testing.T) {
			root := t.TempDir()
			var diag strings.Builder
			out, code := RunOptions(Options{
				Event: tc.event, Payload: tc.payload, Root: root,
				Diagnostics: &diag, Now: func() time.Time { return time.Unix(0, 0) },
			})
			if code != ExitOK {
				t.Fatalf("exit = %d, want 0", code)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want empty", out)
			}
			lines := logLines(t, root)
			if len(lines) != 1 || !strings.Contains(lines[0], "fail-open") || !strings.Contains(lines[0], string(tc.event)) {
				t.Fatalf("runtime log = %q, want one fail-open line", lines)
			}
			if !strings.Contains(diag.String(), "fail-open") {
				t.Fatalf("diagnostics = %q, want the fail-open note", diag.String())
			}
		})
	}
}

func TestFailOpenEngineErrors(t *testing.T) {
	boom := os.ErrPermission
	for _, tc := range []struct {
		event Event
		eng   *fakeEngine
	}{
		{SessionStart, &fakeEngine{contextErr: boom}},
		{UserPrompt, &fakeEngine{recallErr: boom}},
		{Stop, &fakeEngine{stopErr: boom}},
	} {
		t.Run(string(tc.event), func(t *testing.T) {
			root := t.TempDir()
			out, code := RunOptions(Options{
				Engine: tc.eng, Event: tc.event, Payload: `{"prompt":"alpha bravo charlie"}`,
				Root: root, Now: func() time.Time { return time.Unix(0, 0) },
			})
			if code != ExitOK {
				t.Fatalf("exit = %d, want 0 (fail-open)", code)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want empty", out)
			}
			if lines := logLines(t, root); len(lines) != 1 || !strings.Contains(lines[0], boom.Error()) {
				t.Fatalf("runtime log = %q, want one line carrying the cause", lines)
			}
		})
	}
}

// TestFailOpenUnwritableLog: an unwritable log is recorded as an observability
// gap on stderr, never a silent pass — and still exit 0.
func TestFailOpenUnwritableLog(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var diag strings.Builder
	out, code := RunOptions(Options{
		Event: Stop, Root: file, Diagnostics: &diag,
	})
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if out != "" {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(diag.String(), "fail-open") || !strings.Contains(diag.String(), "runtime log เขียนไม่ได้") {
		t.Fatalf("diagnostics = %q, want both the fail-open note and the log gap", diag.String())
	}
}

// TestNoProcessSpawned: the adapter layer must never run git (or anything
// else), so a fake git on PATH is never invoked. The end-to-end push check
// lives in internal/cli, where the engine's local commit really runs.
func TestNoProcessSpawned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the git sentinel shim is a POSIX /bin/sh script — no Windows shell-script shim")
	}
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "called")
	shim := "#!/bin/sh\nprintf 'called\\n' >> " + sentinel + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	eng := &fakeEngine{context: "block", recall: "related"}
	for _, event := range []Event{SessionStart, UserPrompt, Stop} {
		if _, code := RunOptions(Options{
			Engine: eng, Event: event, Payload: `{"prompt":"alpha bravo charlie"}`, Root: t.TempDir(),
		}); code != ExitOK {
			t.Fatalf("%s exit = %d", event, code)
		}
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("hook adapter spawned a process")
	}
}

// logLines returns the non-empty runtime-log lines under root.
func logLines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".memory", "runtime.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read runtime log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
