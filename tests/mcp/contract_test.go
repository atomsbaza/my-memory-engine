// Binary-level contract tests for `mem mcp` (plan P2.3): they build cmd/mem and
// drive the real process over stdio, so the acceptance evidence is the wire, not
// an in-process call — exactly what a harness sees.
//
// Covered here:
//   - every stdout byte parses as a JSON-RPC frame (no banner, no logging)
//   - initialize negotiates a protocol version and carries the instructions
//   - tools/list is the frozen 9-tool document (names, descriptions, schemas)
//   - each frozen tool answers a positive call and rejects malformed input
package mcp_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	memconf "github.com/atomsbaza/my-memory-engine/internal/mcp"
)

var (
	memBin  string
	scratch string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mem-mcp-e2e-")
	if err != nil {
		panic(err)
	}
	scratch = dir
	memBin = filepath.Join(dir, "mem")
	build := exec.Command("go", "build", "-o", memBin, "../../cmd/mem")
	if out, berr := build.CombinedOutput(); berr != nil {
		os.Stderr.WriteString("go build cmd/mem: " + berr.Error() + "\n" + string(out))
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixtureHome copies the synthetic home (never the real ~/my-memory).
func fixtureHome(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "home")
	if err := os.CopyFS(dst, os.DirFS("../../testdata/domain/home")); err != nil {
		t.Fatalf("copy fixture home: %v", err)
	}
	return dst
}

// env is the isolated environment every invocation gets: HOME points into the
// scratch dir, so the binary can never touch the developer's real memory repo.
func env() []string {
	return append(os.Environ(),
		"HOME="+filepath.Join(scratch, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(scratch, "home", ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
}

// frame is one JSON-RPC message as it appeared on stdout.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// session is one `mem mcp` exchange: every response frame, plus the raw
// stdout for the purity assertions.
type session struct {
	frames []frame
	raw    []string // every stdout line consumed, in order
	extra  []string // lines that arrived after the last expected frame
	stdout string
	stderr string
	code   int
}

// request builds one JSON-RPC request line.
func request(id int, method string, params any) string {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// call builds one tools/call request line.
func call(id int, name string, args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}
	return request(id, "tools/call", map[string]any{"name": name, "arguments": args})
}

// runMCP feeds the request lines to `mem --home <home> mcp`, reads the
// response frames while stdin is still open (the server flushes each reply as
// it handles the request, and a client — a harness — never closes stdin
// mid-session), then closes stdin and collects the exit code.
func runMCP(t *testing.T, home string, requests ...string) session {
	t.Helper()
	wantFrames := 0
	for _, req := range requests {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(req), &msg); err != nil {
			t.Fatalf("request is not JSON: %v\n%s", err, req)
		}
		if _, hasID := msg["id"]; hasID {
			wantFrames++
		}
	}

	cmd := exec.Command(memBin, "--home", home, "mcp")
	cmd.Env = env()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mem mcp: %v", err)
	}

	for _, req := range requests {
		if _, werr := fmt.Fprintln(stdin, req); werr != nil {
			t.Fatalf("write request: %v", werr)
		}
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	s := session{}
	deadline := time.After(30 * time.Second)
	for len(s.frames) < wantFrames {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("stdout closed after %d of %d frames\nstdout:\n%s\nstderr:\n%s",
					len(s.frames), wantFrames, strings.Join(s.raw, "\n"), errb.String())
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			s.raw = append(s.raw, line)
			var f frame
			if jerr := json.Unmarshal([]byte(line), &f); jerr != nil {
				t.Fatalf("stdout line %d is not JSON-RPC: %v\n%s", len(s.raw), jerr, line)
			}
			s.frames = append(s.frames, f)
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatalf("timed out after %d of %d frames\nstdout:\n%s\nstderr:\n%s",
				len(s.frames), wantFrames, strings.Join(s.raw, "\n"), errb.String())
		}
	}

	// Drain anything the server flushed after the last expected frame, then end
	// the session the way a harness does: close stdin and wait for exit.
	_ = stdin.Close()
	drained := make(chan []string)
	go func() {
		var extra []string
		for line := range lines {
			if strings.TrimSpace(line) != "" {
				extra = append(extra, line)
			}
		}
		drained <- extra
	}()
	s.extra = <-drained

	werr := cmd.Wait()
	s.stderr = errb.String()
	s.code = 0
	if exitErr, ok := werr.(*exec.ExitError); ok {
		s.code = exitErr.ExitCode()
	} else if werr != nil {
		t.Fatalf("wait mem mcp: %v", werr)
	}
	s.stdout = strings.Join(append(s.raw, s.extra...), "\n")
	return s
}

// initialize returns the standard handshake request lines.
func initialize() []string {
	return []string{
		request(1, "initialize", map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "contract-test", "version": "0"},
		}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}
}

// result decodes the result of the frame with id into v.
func (s session) result(t *testing.T, id int, v any) {
	t.Helper()
	for _, f := range s.frames {
		if strings.TrimSpace(string(f.ID)) != fmt.Sprint(id) {
			continue
		}
		if len(f.Error) > 0 && string(f.Error) != "null" {
			t.Fatalf("id %d returned a protocol error: %s", id, f.Error)
		}
		if err := json.Unmarshal(f.Result, v); err != nil {
			t.Fatalf("id %d: decode result: %v\n%s", id, err, f.Result)
		}
		return
	}
	t.Fatalf("no response frame for id %d (got %d frames)\nstdout:\n%s\nstderr:\n%s",
		id, len(s.frames), s.stdout, s.stderr)
}

// response returns the frame with id.
func (s session) response(t *testing.T, id int) frame {
	t.Helper()
	for _, f := range s.frames {
		if strings.TrimSpace(string(f.ID)) == fmt.Sprint(id) {
			return f
		}
	}
	t.Fatalf("no response frame for id %d\nstdout:\n%s", id, s.stdout)
	return frame{}
}

// toolResult is a decoded tools/call result.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// text returns the first text content of a tools/call result.
func (r toolResult) text() string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

// callTool runs one tools/call in its own session (handshake included).
func callTool(t *testing.T, home, name string, args map[string]any) toolResult {
	t.Helper()
	s := runMCP(t, home, append(initialize(), call(2, name, args))...)
	if s.code != 0 {
		t.Fatalf("mem mcp exited %d\nstderr:\n%s", s.code, s.stderr)
	}
	var res toolResult
	s.result(t, 2, &res)
	return res
}

// TestStdioPurity: every stdout byte is a JSON-RPC frame — no banner, no
// progress text, no Go log output — and the handshake negotiates a version and
// carries the instructions.
func TestStdioPurity(t *testing.T) {
	home := fixtureHome(t)
	s := runMCP(t, home, append(initialize(),
		request(2, "tools/list", nil),
		call(3, "memory_stats", nil),
		call(4, "memory_context", map[string]any{"no_touch": true}),
	)...)

	if s.code != 0 {
		t.Fatalf("mem mcp exited %d\nstderr:\n%s", s.code, s.stderr)
	}
	// Four request ids and one notification: exactly four response frames, and
	// nothing else on the stream (no unsolicited frame, no banner).
	if len(s.frames) != 4 {
		names := make([]string, len(s.frames))
		for i, f := range s.frames {
			names[i] = string(f.ID)
		}
		t.Fatalf("stdout carried %d frames (ids %v), want 4\nstdout:\n%s", len(s.frames), names, s.stdout)
	}
	if len(s.extra) != 0 {
		t.Errorf("stdout carried %d unsolicited line(s) after the last response:\n%s",
			len(s.extra), strings.Join(s.extra, "\n"))
	}
	for _, f := range s.frames {
		if f.JSONRPC != "2.0" {
			t.Errorf("frame without jsonrpc 2.0: %+v", f)
		}
		if len(f.ID) == 0 {
			t.Errorf("response frame without id: %+v", f)
		}
	}

	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	s.result(t, 1, &init)
	if init.ProtocolVersion == "" {
		t.Error("initialize did not negotiate a protocolVersion")
	}
	if init.ServerInfo.Name != memconf.ServerName || init.ServerInfo.Version != memconf.Version {
		t.Errorf("serverInfo = %+v, want %s/%s", init.ServerInfo, memconf.ServerName, memconf.Version)
	}
	for _, want := range []string{"นี่คือ my-memory", "memory_add", "memory_trace"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("instructions missing %q", want)
		}
	}

	// The calls actually did something: stats and the injected block are real.
	var stats toolResult
	s.result(t, 3, &stats)
	if stats.IsError || !strings.Contains(stats.text(), "records ") {
		t.Errorf("memory_stats over stdio = %q (isError=%v)", stats.text(), stats.IsError)
	}
	var ctx toolResult
	s.result(t, 4, &ctx)
	if ctx.IsError || !strings.Contains(ctx.text(), "[End of memory]") {
		t.Errorf("memory_context over stdio = %q (isError=%v)", ctx.text(), ctx.IsError)
	}
}

// TestToolsListFrozenContract: the served tools/list is the frozen 9-tool
// document — same names, descriptions and input schemas as the baseline.
func TestToolsListFrozenContract(t *testing.T) {
	home := fixtureHome(t)
	s := runMCP(t, home, append(initialize(), request(2, "tools/list", nil))...)
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	s.result(t, 2, &res)

	var frozen []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(memconf.FrozenToolsJSON(), &frozen); err != nil {
		t.Fatalf("parse embedded frozen snapshot: %v", err)
	}
	if len(res.Tools) != 9 {
		t.Fatalf("tools/list returned %d tools, want 9", len(res.Tools))
	}
	if len(frozen) != len(res.Tools) {
		t.Fatalf("frozen snapshot has %d tools, served %d", len(frozen), len(res.Tools))
	}

	served := make(map[string]int, len(res.Tools))
	for i, tool := range res.Tools {
		served[tool.Name] = i
	}
	for _, want := range frozen {
		i, ok := served[want.Name]
		if !ok {
			t.Errorf("tool %q is missing from tools/list", want.Name)
			continue
		}
		got := res.Tools[i]
		if got.Description != want.Description {
			t.Errorf("%s: description drifted\n got: %q\nwant: %q", want.Name, got.Description, want.Description)
		}
		if !sameJSON(t, got.InputSchema, want.InputSchema) {
			t.Errorf("%s: inputSchema drifted from the frozen snapshot\n got: %s\nwant: %s",
				want.Name, got.InputSchema, want.InputSchema)
		}
	}
}

// TestFrozenArgumentContract pins the arguments the six installed harnesses
// already depend on (OQ5 freeze): a rename or a new required field is a
// breaking change and must fail this test loudly.
func TestFrozenArgumentContract(t *testing.T) {
	want := map[string][]string{
		"memory_search": {"query"},
		"memory_show":   {"id"},
		"memory_add":    {"text"},
		"memory_trace":  {"query"},
	}
	var frozen []struct {
		Name        string `json:"name"`
		InputSchema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(memconf.FrozenToolsJSON(), &frozen); err != nil {
		t.Fatalf("parse embedded frozen snapshot: %v", err)
	}
	if len(frozen) != 9 {
		t.Fatalf("frozen snapshot has %d tools, want the frozen 9", len(frozen))
	}
	seen := make(map[string]bool, len(frozen))
	for _, tool := range frozen {
		seen[tool.Name] = true
		got := tool.InputSchema.Required
		if got == nil {
			got = []string{}
		}
		wantRequired, pinned := want[tool.Name]
		if !pinned {
			wantRequired = []string{}
		}
		if !reflect.DeepEqual(got, wantRequired) {
			t.Errorf("%s: required = %v, want %v", tool.Name, got, wantRequired)
		}
	}
	// memory_add's optional surface is what the Python baseline accepts; the
	// write guards depend on every one of these names.
	addProps := map[string]bool{"text": true, "layer": true, "scope": true, "title": true, "tags": true,
		"confidence": true, "decay": true, "half_life": true, "evidence": true, "expires": true,
		"supersedes": true, "supersede": true, "source": true, "sensitivity": true, "force": true}
	for _, tool := range frozen {
		if tool.Name != "memory_add" {
			continue
		}
		for _, prop := range []string{"text", "layer", "scope", "title", "tags", "confidence", "decay",
			"half_life", "evidence", "expires", "supersedes", "supersede", "source", "sensitivity", "force"} {
			if _, ok := tool.InputSchema.Properties[prop]; !ok {
				t.Errorf("memory_add lost argument %q", prop)
			}
			delete(addProps, prop)
		}
	}
	if len(addProps) != 0 {
		t.Errorf("memory_add contract lists unknown arguments: %v", addProps)
	}
	if !seen["memory_context"] || !seen["memory_sync"] {
		t.Errorf("frozen snapshot lost a tool: %v", seen)
	}
}

// TestPositiveCalls: each frozen tool answers a real call against a real home
// (the write side is proved by the file landing on disk).
func TestPositiveCalls(t *testing.T) {
	home := fixtureHome(t)

	t.Run("memory_add", func(t *testing.T) {
		res := callTool(t, home, "memory_add", map[string]any{
			"text":  "**Rule.** stdio contract test record.\n**NOT.** ไม่เขียนซ้ำ\n**Evidence.** contract test",
			"layer": "lesson", "scope": "global", "title": "stdio contract test record",
		})
		if res.IsError {
			t.Fatalf("memory_add failed: %s", res.text())
		}
		fields := strings.Split(strings.TrimSpace(res.text()), "\t")
		if len(fields) < 2 {
			t.Fatalf("add output = %q, want <id>\\t<rel>", res.text())
		}
		if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(fields[1]))); err != nil {
			t.Fatalf("record %s not on disk: %v", fields[1], err)
		}
	})

	t.Run("memory_search", func(t *testing.T) {
		res := callTool(t, home, "memory_search", map[string]any{"query": "artifact", "scores": true})
		if res.IsError {
			t.Fatalf("memory_search failed: %s", res.text())
		}
		if !strings.Contains(res.text(), "rule.aaaa0001") || !strings.Contains(res.text(), "rrf_score=") {
			t.Errorf("search output = %q", res.text())
		}
	})

	t.Run("memory_show", func(t *testing.T) {
		res := callTool(t, home, "memory_show", map[string]any{"id": "rule.aaaa0001"})
		if res.IsError {
			t.Fatalf("memory_show failed: %s", res.text())
		}
		if !strings.Contains(res.text(), "id: rule.aaaa0001") {
			t.Errorf("show output = %q", res.text())
		}
	})

	t.Run("memory_list", func(t *testing.T) {
		res := callTool(t, home, "memory_list", map[string]any{"limit": 5})
		if res.IsError {
			t.Fatalf("memory_list failed: %s", res.text())
		}
		if !strings.Contains(res.text(), "[rule/global/active]") {
			t.Errorf("list output = %q", res.text())
		}
	})

	t.Run("memory_verify", func(t *testing.T) {
		res := callTool(t, home, "memory_verify", nil)
		if res.IsError {
			t.Fatalf("memory_verify failed on the clean fixture: %s", res.text())
		}
		if !strings.Contains(res.text(), "records · ") {
			t.Errorf("verify output = %q", res.text())
		}
	})

	t.Run("memory_stats", func(t *testing.T) {
		res := callTool(t, home, "memory_stats", nil)
		if res.IsError {
			t.Fatalf("memory_stats failed: %s", res.text())
		}
		for _, want := range []string{"records ", "by layer:", "harness coverage"} {
			if !strings.Contains(res.text(), want) {
				t.Errorf("stats output missing %q: %s", want, res.text())
			}
		}
	})

	t.Run("memory_sync", func(t *testing.T) {
		// The fixture home is not a git repo: the tool must report that as an
		// error result, not hang or leak to stdout. (The in-process suite
		// covers the committed/no-change paths against a git fixture.)
		res := callTool(t, home, "memory_sync", map[string]any{"no_push": true})
		if !res.IsError || !strings.Contains(res.text(), "git init") {
			t.Errorf("sync on a non-repo home = %q (isError=%v), want the git-init refusal", res.text(), res.IsError)
		}
	})

	t.Run("memory_trace", func(t *testing.T) {
		res := callTool(t, home, "memory_trace", map[string]any{"query": "artifact", "limit": 5})
		if res.IsError {
			t.Fatalf("memory_trace failed: %s", res.text())
		}
		for _, want := range []string{"trace 'artifact' · limit 5", "RRF: K=60", "layer allocation"} {
			if !strings.Contains(res.text(), want) {
				t.Errorf("trace output missing %q: %s", want, res.text())
			}
		}
	})
}

// TestMalformedInput: schema violations are rejected by the server (validation
// error result), and the baseline's explicit refusals stay loud.
func TestMalformedInput(t *testing.T) {
	home := fixtureHome(t)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"memory_search", map[string]any{}},
		{"memory_search", map[string]any{"query": "artifact", "limit": "many"}},
		{"memory_show", map[string]any{}},
		{"memory_show", map[string]any{"id": ""}},
		{"memory_add", map[string]any{}},
		{"memory_add", map[string]any{"text": ""}},
		{"memory_trace", map[string]any{}},
	}
	for _, tc := range cases {
		res := callTool(t, home, tc.name, tc.args)
		if !res.IsError {
			t.Errorf("%s%v was accepted over stdio, want an error result (%q)", tc.name, tc.args, res.text())
			continue
		}
		if strings.TrimSpace(res.text()) == "" {
			t.Errorf("%s%v: empty error text", tc.name, tc.args)
		}
	}

	// A tool outside the frozen set is not served: protocol error or error result.
	s := runMCP(t, home, append(initialize(), call(2, "memory_promote", nil))...)
	f := s.response(t, 2)
	if len(f.Error) == 0 || string(f.Error) == "null" {
		var res toolResult
		if err := json.Unmarshal(f.Result, &res); err != nil || !res.IsError {
			t.Errorf("memory_promote was served: error=%s result=%s", f.Error, f.Result)
		}
	}
}

// sameJSON compares two JSON documents semantically.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}
