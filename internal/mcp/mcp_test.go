package mcp

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixtureSrc is the synthetic home every test copies (records + taxonomy; never
// the real ~/my-memory).
const fixtureSrc = "../../testdata/domain/home"

// fixtureHome copies the synthetic home into a temp dir and — unless bare is
// set — makes it a git repo with one baseline commit, so the write path and
// memory_sync exercise their real behaviour.
func fixtureHome(t *testing.T, bare bool) string {
	t.Helper()
	dst := t.TempDir()
	if err := filepath.WalkDir(fixtureSrc, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(fixtureSrc, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	// The server resolves the home through store.ExpandPath (symlink-free), so
	// the fixture path must be resolved too or the instructions text differs
	// from what the test computes from its own root string.
	if resolved, rerr := filepath.EvalSymlinks(dst); rerr == nil {
		dst = resolved
	}
	if bare {
		return dst
	}
	git(t, dst, "init", "-q")
	git(t, dst, "config", "user.email", "test@example.invalid")
	git(t, dst, "config", "user.name", "test")
	git(t, dst, "add", "-A")
	git(t, dst, "commit", "-q", "-m", "fixture baseline")
	return dst
}

// git runs one git command against dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newSession starts the server against root plus a test client over in-memory
// transports, which performs the real initialize handshake.
func newSession(t *testing.T, root string) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()

	srv, err := NewServer(Options{Root: root, Agent: "test-harness"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "mcp-contract-test"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect/initialize: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callTool performs one tools/call and returns the first text content.
func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("tools/call %s: transport error: %v", name, err)
	}
	if len(res.Content) == 0 {
		return "", res.IsError
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("%s: content[0] is %T, want *TextContent", name, res.Content[0])
	}
	return text.Text, res.IsError
}

// TestInitializeHandshake: the handshake must present the baseline identity,
// a negotiated protocol version and the frozen instructions text.
func TestInitializeHandshake(t *testing.T) {
	root := fixtureHome(t, false)
	cs := newSession(t, root)

	ir := cs.InitializeResult()
	if ir == nil {
		t.Fatal("initialize result is nil — handshake did not complete")
	}
	if ir.ServerInfo.Name != ServerName {
		t.Errorf("server name = %q, want %q", ir.ServerInfo.Name, ServerName)
	}
	if ir.ServerInfo.Version != Version {
		t.Errorf("server version = %q, want %q", ir.ServerInfo.Version, Version)
	}
	if ir.ProtocolVersion == "" {
		t.Error("protocol version is empty — negotiation did not happen")
	}
	want := instructions(root, "test-harness")
	if ir.Instructions != want {
		t.Errorf("instructions mismatch\n got: %q\nwant: %q", ir.Instructions, want)
	}
	if !strings.Contains(ir.Instructions, "source ควรเป็น agent:test-harness") {
		t.Errorf("instructions do not name the harness source:\n%s", ir.Instructions)
	}
}

// TestInstructionsShape pins the parts of the frozen handshake text harnesses
// read: the repo pointer, the CLI path and both workflow reminders.
func TestInstructionsShape(t *testing.T) {
	got := instructions("/tmp/not-under-home", "")
	for _, want := range []string{
		"นี่คือ my-memory",
		"(private git repo `~/my-memory` · CLI `/tmp/not-under-home/bin/mem`)",
		"เรียก memory_context",
		"memory_add",
		"source ควรเป็น agent:mcp",
		"memory_trace",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("instructions lost the baseline's trailing newline")
	}
}

// TestToolsListMatchesFrozenSnapshot: tools/list must be the frozen document —
// same 9 names in registry order, same descriptions, same input schemas.
func TestToolsListMatchesFrozenSnapshot(t *testing.T) {
	cs := newSession(t, fixtureHome(t, false))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	frozen, err := frozenTools()
	if err != nil {
		t.Fatalf("frozen tools: %v", err)
	}
	if len(res.Tools) != len(frozen) || len(frozen) != 9 {
		names := make([]string, len(res.Tools))
		for i, tool := range res.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("tools/list returned %d tools (%v), want %d", len(res.Tools), names, len(frozen))
	}

	emitted := make(map[string]*mcpsdk.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		emitted[tool.Name] = tool
	}
	for _, want := range frozen {
		got, ok := emitted[want.Name]
		if !ok {
			t.Errorf("tool %q missing from tools/list", want.Name)
			continue
		}
		if got.Description != want.Description {
			t.Errorf("%s: description = %q, want %q", want.Name, got.Description, want.Description)
		}
		gotSchema, err := json.Marshal(got.InputSchema)
		if err != nil {
			t.Fatalf("%s: marshal emitted schema: %v", want.Name, err)
		}
		if !jsonEqual(t, gotSchema, want.InputSchema) {
			t.Errorf("%s: inputSchema drifted from the frozen snapshot", want.Name)
		}
	}
}

// TestFrozenRequiredAndProperties pins the frozen argument contract
// independently of the snapshot file: a renamed or dropped argument would
// break the six installed harnesses, so the expected surface is spelled out.
func TestFrozenRequiredAndProperties(t *testing.T) {
	want := map[string]struct {
		required []string
		props    []string
	}{
		"memory_context": {nil, []string{"budget", "no_touch", "project", "query", "scope"}},
		"memory_search":  {[]string{"query"}, []string{"include_inactive", "layer", "limit", "query", "scope", "scores"}},
		"memory_show":    {[]string{"id"}, []string{"id"}},
		"memory_add": {[]string{"text"}, []string{
			"confidence", "decay", "evidence", "expires", "force", "half_life",
			"layer", "scope", "sensitivity", "source", "supersede", "supersedes", "tags", "text", "title",
		}},
		"memory_list":   {nil, []string{"layer", "limit", "scope"}},
		"memory_verify": {nil, nil},
		"memory_stats":  {nil, nil},
		"memory_sync":   {nil, []string{"message", "no_push"}},
		"memory_trace":  {[]string{"query"}, []string{"limit", "query", "scope"}},
	}
	frozen, err := frozenTools()
	if err != nil {
		t.Fatalf("frozen tools: %v", err)
	}
	if len(frozen) != len(want) {
		t.Fatalf("frozen snapshot has %d tools, contract expects %d", len(frozen), len(want))
	}
	for _, tool := range frozen {
		exp, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool in frozen snapshot: %s", tool.Name)
			continue
		}
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: parse frozen schema: %v", tool.Name, err)
		}
		gotRequired := schema.Required
		if gotRequired == nil {
			gotRequired = []string{}
		}
		wantRequired := exp.required
		if wantRequired == nil {
			wantRequired = []string{}
		}
		if !slices.Equal(gotRequired, wantRequired) {
			t.Errorf("%s: required = %v, want %v", tool.Name, gotRequired, wantRequired)
		}
		gotProps := make([]string, 0, len(schema.Properties))
		for k := range schema.Properties {
			gotProps = append(gotProps, k)
		}
		slices.Sort(gotProps)
		wantProps := slices.Clone(exp.props)
		slices.Sort(wantProps)
		if !slices.Equal(gotProps, wantProps) {
			t.Errorf("%s: properties = %v, want %v", tool.Name, gotProps, wantProps)
		}
	}
}

// TestToolCallsWired proves every one of the 9 tools reaches the domain layer
// and returns the CLI-shaped text (not a stub).
func TestToolCallsWired(t *testing.T) {
	root := fixtureHome(t, false)
	cs := newSession(t, root)

	t.Run("memory_context", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_context", map[string]any{
			"query": "artifact", "no_touch": true,
		})
		if isErr {
			t.Fatalf("memory_context returned an error: %s", text)
		}
		for _, want := range []string{"[Memory — ความจำถาวรของผู้ใช้", "## Rules (MUST follow)", "[End of memory]"} {
			if !strings.Contains(text, want) {
				t.Errorf("context output missing %q\n%s", want, text)
			}
		}
		// The injection is logged like the CLI's session-start event.
		data, err := os.ReadFile(filepath.Join(root, "ops", "recall-log.jsonl"))
		if err != nil {
			t.Fatalf("read recall log: %v", err)
		}
		if !strings.Contains(string(data), `"type":"inject"`) {
			t.Errorf("recall log has no inject row:\n%s", data)
		}
	})

	t.Run("memory_search", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_search", map[string]any{
			"query": "artifact", "scores": true,
		})
		if isErr {
			t.Fatalf("memory_search returned an error: %s", text)
		}
		if !strings.Contains(text, "rule.aaaa0001") {
			t.Errorf("search for the fixture rule returned:\n%s", text)
		}
		if !strings.Contains(text, "rrf_score=") {
			t.Errorf("scores=true did not expose the RRF breakdown:\n%s", text)
		}
	})

	t.Run("memory_show", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_show", map[string]any{"id": "rule.aaaa0001"})
		if isErr {
			t.Fatalf("memory_show returned an error: %s", text)
		}
		if !strings.Contains(text, "id: rule.aaaa0001") || !strings.Contains(text, "sensitivity: internal") {
			t.Errorf("show did not return the record file:\n%s", text)
		}
	})

	t.Run("memory_add", func(t *testing.T) {
		body := "**Rule.** MCP adapter test record.\n**NOT.** ไม่เขียนซ้ำ\n**Evidence.** adapter test"
		text, isErr := callTool(t, cs, "memory_add", map[string]any{
			"text": body, "layer": "lesson", "scope": "global", "title": "mcp adapter test record",
		})
		if isErr {
			t.Fatalf("memory_add returned an error: %s", text)
		}
		fields := strings.Split(strings.TrimSpace(text), "\t")
		if len(fields) < 2 {
			t.Fatalf("add output = %q, want <id>\\t<rel>", text)
		}
		if !regexp.MustCompile(`^lesson\.[0-9a-f]{8,16}$`).MatchString(fields[0]) {
			t.Errorf("add id = %q, want lesson.<hex>", fields[0])
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fields[1])))
		if err != nil {
			t.Fatalf("record not on disk at %s: %v", fields[1], err)
		}
		for _, want := range []string{"source: agent:test-harness", "layer: lesson", "MCP adapter test record"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("written record missing %q:\n%s", want, data)
			}
		}
	})

	t.Run("memory_list", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_list", map[string]any{})
		if isErr {
			t.Fatalf("memory_list returned an error: %s", text)
		}
		if !strings.Contains(text, "rule.aaaa0001") || !strings.Contains(text, "[rule/global/active]") {
			t.Errorf("list output missing the fixture rule:\n%s", text)
		}
	})

	t.Run("memory_verify", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_verify", map[string]any{})
		if isErr {
			t.Fatalf("memory_verify reported problems on the clean fixture: %s", text)
		}
		if !strings.Contains(text, "0 problems") || !strings.Contains(text, "records · ") {
			t.Errorf("verify summary missing:\n%s", text)
		}
	})

	t.Run("memory_stats", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_stats", map[string]any{})
		if isErr {
			t.Fatalf("memory_stats returned an error: %s", text)
		}
		for _, want := range []string{"records ", "by layer:", "by source:", "harness coverage"} {
			if !strings.Contains(text, want) {
				t.Errorf("stats output missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("memory_sync", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_sync", map[string]any{"no_push": true, "message": "mcp test sync"})
		if isErr {
			t.Fatalf("memory_sync returned an error: %s", text)
		}
		if !strings.Contains(text, "committed") && !strings.Contains(text, "ไม่มีอะไรเปลี่ยน") {
			t.Errorf("sync output = %q, want a commit summary", text)
		}
	})

	t.Run("memory_trace", func(t *testing.T) {
		text, isErr := callTool(t, cs, "memory_trace", map[string]any{"query": "artifact", "limit": 5})
		if isErr {
			t.Fatalf("memory_trace returned an error: %s", text)
		}
		for _, want := range []string{
			"trace 'artifact' · limit 5",
			"tokens:",
			"RRF: K=60",
			"rrf_score=",
			"half_life=",
			"layer allocation",
			"  total     cap ",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("trace output missing %q\n%s", want, text)
			}
		}
	})
}

// TestMalformedInputRejected covers the negative side: schema violations the
// SDK rejects before the handler, and the baseline's own explicit refusals.
func TestMalformedInputRejected(t *testing.T) {
	cs := newSession(t, fixtureHome(t, false))

	cases := []struct {
		name string
		args map[string]any
	}{
		{"memory_search", map[string]any{}},
		{"memory_show", map[string]any{}},
		{"memory_add", map[string]any{}},
		{"memory_trace", map[string]any{}},
		{"memory_show", map[string]any{"id": ""}},
		{"memory_add", map[string]any{"text": "   "}},
		{"memory_trace", map[string]any{"query": "  "}},
		{"memory_search", map[string]any{"query": "artifact", "limit": "many"}},
	}
	for _, tc := range cases {
		text, isErr := callTool(t, cs, tc.name, tc.args)
		if !isErr {
			t.Errorf("%s%v was accepted, want a tool error (text: %q)", tc.name, tc.args, text)
		}
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s%v: empty error text — the caller cannot see why", tc.name, tc.args)
		}
	}

	// A tool outside the frozen set must not be served.
	if res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "memory_promote", Arguments: map[string]any{},
	}); err == nil && !res.IsError {
		t.Error("memory_promote (outside the frozen 9) was served, want a protocol error")
	}
}

// TestNotFoundRecordIsAnError: memory_show on a missing id must fail loudly.
func TestNotFoundRecordIsAnError(t *testing.T) {
	cs := newSession(t, fixtureHome(t, false))
	text, isErr := callTool(t, cs, "memory_show", map[string]any{"id": "lesson.deadbeef"})
	if !isErr {
		t.Fatalf("show of a missing record was accepted: %q", text)
	}
	if !strings.Contains(text, "ไม่พบ record") {
		t.Errorf("error text = %q, want the missing-record message", text)
	}
}

// TestFrozenSnapshotIsNineExactTools guards the snapshot file itself.
func TestFrozenSnapshotIsNineExactTools(t *testing.T) {
	frozen, err := frozenTools()
	if err != nil {
		t.Fatalf("frozen tools: %v", err)
	}
	got := make([]string, len(frozen))
	for i, tool := range frozen {
		got[i] = tool.Name
	}
	if !slices.Equal(got, ToolNames) {
		t.Errorf("frozen snapshot order = %v, want the registry order %v", got, ToolNames)
	}
}

// jsonEqual reports whether two JSON documents are semantically equal.
func jsonEqual(t *testing.T, a, b []byte) bool {
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
