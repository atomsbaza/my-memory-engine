// Hook command tests: the three events, the four output formats, per-harness
// payload extraction, silence on tiny prompts and the fail-open contract
// (AC-P2-10, AC-P2-11, REQ-HOOK-02).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// payload is a claude-style hook payload (query + session at the top level).
const claudePayload = `{"session_id":"sess-123","prompt":"alpha bravo charlie"}`

func TestHookSessionStartInjectsContext(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "hook", "--event", "session-start", "--project", "alpha-project")
	mustCode(t, ExitOK, code, out, stderr, "hook session-start")
	if !strings.Contains(out, "[my-memory]") || !strings.Contains(out, "Rules (MUST follow)") {
		t.Fatalf("session-start did not inject the block:\n%s", out)
	}
	if !strings.Contains(out, "[End of memory]") {
		t.Fatalf("session-start block has no end marker:\n%s", out)
	}
}

func TestHookUserPromptSilentForTinyPrompt(t *testing.T) {
	home := fixtureHome(t)
	for _, argv := range [][]string{
		{"hook", "--event", "user-prompt", "--query", "hi"},
		{"hook", "--event", "user-prompt", "--query", "ok thanks"},
	} {
		code, out, stderr := runHome(t, home, "", argv...)
		mustCode(t, ExitOK, code, out, stderr, argv[len(argv)-1])
		if out != "" {
			t.Fatalf("%v: injected for a tiny prompt: %q", argv, out)
		}
	}
}

// TestHookUserPromptInjectsRecall: a real request injects the matching record
// and logs one inject row for the noise metric.
func TestHookUserPromptInjectsRecall(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, claudePayload, "hook", "--event", "user-prompt")
	mustCode(t, ExitOK, code, out, stderr, "hook user-prompt")
	if !strings.Contains(out, "[my-memory recall") || !strings.Contains(out, "[end recall]") {
		t.Fatalf("user-prompt did not inject a recall:\n%s", out)
	}
	if !strings.Contains(out, "[id: lesson.bbbb0001]") {
		t.Fatalf("recall did not include the matching record:\n%s", out)
	}

	data, err := os.ReadFile(filepath.Join(home, "ops", "recall-log.jsonl"))
	if err != nil {
		t.Fatalf("recall log missing: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &row); err != nil {
		t.Fatalf("recall row: %v", err)
	}
	if row["event"] != "user-prompt" || row["session"] != "sess-123" {
		t.Fatalf("recall row = %v, want the user-prompt/session from the payload", row)
	}
	if head, _ := row["prompt_head"].(string); head != "alpha bravo charlie" {
		t.Fatalf("prompt_head = %q, want the extracted prompt", head)
	}
	if h, _ := row["prompt_hash"].(string); !strings.HasPrefix(h, "fnv1a:") {
		t.Fatalf("prompt_hash = %q, want fnv1a:…", h)
	}
}

// TestHookPayloadExtraction: the query is read from each harness's own keys,
// not one shared field (REQ-HOOK-01).
func TestHookPayloadExtraction(t *testing.T) {
	home := fixtureHome(t)
	cases := []string{
		`{"userPrompt":"alpha bravo charlie"}`,
		`{"data":{"prompt":"alpha bravo charlie"}}`,
		`{"input":{"message":"alpha bravo charlie"}}`,
		`{"extra":{"query":"alpha bravo charlie"}}`,
	}
	for _, payload := range cases {
		code, out, stderr := runHome(t, home, payload, "hook", "--event", "user-prompt")
		if code != ExitOK {
			t.Fatalf("%s: exit = %d (stderr %q)", payload, code, stderr)
		}
		if !strings.Contains(out, "[my-memory recall") {
			t.Fatalf("%s: query not extracted, stdout = %q", payload, out)
		}
	}
}

// TestHookFormats covers the four output contracts.
func TestHookFormats(t *testing.T) {
	home := fixtureHome(t)
	cases := []struct {
		format string
		check  func(t *testing.T, out string)
	}{
		{"text", func(t *testing.T, out string) {
			if !strings.HasPrefix(out, "[my-memory]") {
				t.Fatalf("text format = %q", out)
			}
		}},
		{"json", func(t *testing.T, out string) {
			var doc struct {
				HookSpecificOutput struct {
					EventName         string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("json format is not strict JSON: %v\n%s", err, out)
			}
			if doc.HookSpecificOutput.EventName != "SessionStart" || doc.HookSpecificOutput.AdditionalContext == "" {
				t.Fatalf("json payload = %+v", doc.HookSpecificOutput)
			}
		}},
		{"hermes", func(t *testing.T, out string) {
			var doc struct {
				Context string `json:"context"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Context == "" {
				t.Fatalf("hermes payload invalid: %v\n%s", err, out)
			}
		}},
		{"cursor", func(t *testing.T, out string) {
			var doc struct {
				AdditionalContext string `json:"additional_context"`
				Continue          bool   `json:"continue"`
			}
			// cursor's beforeSubmitPrompt carries continue; SessionStart does not.
			if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.AdditionalContext == "" {
				t.Fatalf("cursor payload invalid: %v\n%s", err, out)
			}
		}},
	}
	for _, tc := range cases {
		code, out, stderr := runHome(t, home, "", "hook", "--event", "session-start", "--project", "alpha-project", "--format", tc.format)
		if code != ExitOK {
			t.Fatalf("%s: exit = %d (stderr %q)", tc.format, code, stderr)
		}
		tc.check(t, out)
	}

	// A tiny prompt injects nothing at all — not even the cursor continue flag
	// (Python cmd_hook returns before hook_emit for <2 content tokens).
	code, out, stderr := runHome(t, home, "", "hook", "--event", "user-prompt", "--query", "hi", "--format", "cursor", "--event-name", "beforeSubmitPrompt")
	mustCode(t, ExitOK, code, out, stderr, "cursor beforeSubmitPrompt")
	if out != "" {
		t.Fatalf("tiny prompt injected under cursor: %q", out)
	}
}

// TestEmitHookEmptyCursorContinue covers the defensive branch: when a caller
// does emit empty text under cursor's beforeSubmitPrompt, the answer must keep
// the turn alive instead of printing nothing.
func TestEmitHookEmptyCursorContinue(t *testing.T) {
	var sb strings.Builder
	emitHook(&sb, "cursor", "beforeSubmitPrompt", "")
	var doc struct {
		Continue bool `json:"continue"`
	}
	if err := json.Unmarshal([]byte(sb.String()), &doc); err != nil || !doc.Continue {
		t.Fatalf("empty cursor beforeSubmitPrompt = %q, want continue:true", sb.String())
	}
	sb.Reset()
	emitHook(&sb, "cursor", "SessionStart", "")
	if sb.String() != "" {
		t.Fatalf("empty SessionStart emitted %q, want nothing", sb.String())
	}
}

// TestHookStopCommitsLocally: the stop hook commits but never pushes.
func TestHookStopCommitsLocally(t *testing.T) {
	home := fixtureHome(t)
	// The fixture ships uncommitted files, so a commit must appear.
	writeFile(t, home, "lessons/hook-stop-lesson.eeee0001.md", `---
id: lesson.eeee0001
title: hook stop writes a lesson
layer: lesson
scope: global
tags: []
confidence: 0.9
source: agent:test
created: 2026-01-01
last_used: 2026-01-01
expires: none
status: active
sensitivity: internal
---

**Rule.** stop hook commits locally.
`)
	code, out, stderr := runHome(t, home, "", "hook", "--event", "stop", "--agent", "claude")
	mustCode(t, ExitOK, code, out, stderr, "hook stop")
	if out != "" {
		t.Fatalf("stop hook wrote to stdout: %q", out)
	}
	if !strings.Contains(stderr, "hook stop — commit") {
		t.Fatalf("stop hook did not report the commit: %q", stderr)
	}
	log := git(t, home, "log", "--oneline", "-1")
	if !strings.Contains(log, "mem: hook stop") {
		t.Fatalf("stop hook commit missing: %s", log)
	}
	if strings.Contains(log, "push") {
		t.Fatalf("stop hook commit message mentions push: %s", log)
	}
}

// TestHookFailOpen: a broken home or repo never makes a hook fail the turn.
func TestHookFailOpen(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-home")
	for _, argv := range [][]string{
		{"hook", "--event", "session-start"},
		{"hook", "--event", "user-prompt", "--query", "alpha bravo charlie"},
		{"hook", "--event", "stop"},
	} {
		code, _, _ := runHome(t, missing, claudePayload, argv...)
		if code != ExitOK {
			t.Errorf("%v on a missing home: exit = %d, want 0 (fail-open)", argv, code)
		}
	}
}

func TestHookUsageErrors(t *testing.T) {
	for _, argv := range [][]string{
		{"hook"},
		{"hook", "--event", "nope"},
		{"hook", "--event", "session-start", "--format", "nope"},
	} {
		code, out, stderr := runMain(t, "", argv...)
		if code != ExitGuard {
			t.Errorf("%v: exit = %d, want %d (stdout %q, stderr %q)", argv, code, ExitGuard, out, stderr)
		}
	}
}
