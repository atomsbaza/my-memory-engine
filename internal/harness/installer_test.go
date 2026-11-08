package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fixture: temp home + fake engine binary + stubbed exec/lookpath/clock.
// Nothing here may touch the real $HOME — every path is under t.TempDir().
// ---------------------------------------------------------------------------

type recorder struct {
	calls   []string
	listOut map[string]string // bin -> stdout of `<bin> mcp list`
}

func (r *recorder) exec(name string, args ...string) (string, error) {
	r.calls = append(r.calls, quoteCmd(name, args))
	if len(args) >= 2 && args[0] == "mcp" && args[1] == "list" {
		return r.listOut[name], nil
	}
	// kiro answers its idempotency probe with the exit code alone.
	if len(args) >= 2 && args[0] == "mcp" && args[1] == "status" {
		if strings.Contains(r.listOut[name], "my-memory") {
			return "", nil
		}
		return "", errors.New("No MCP server named 'my-memory' found in any agent")
	}
	if len(args) >= 2 && args[0] == "mcp" && args[1] == "add" {
		if r.listOut == nil {
			r.listOut = map[string]string{}
		}
		r.listOut[name] = "my-memory: " + name + " server" // simulate registry state
	}
	return "", nil
}

type fixture struct {
	home     string
	repo     string
	memBin   string
	rec      *recorder
	look     map[string]bool
	settings string // ~/.claude/settings.json
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	memBin := filepath.Join(home, "bin", "mem")
	if err := os.MkdirAll(filepath.Dir(memBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{
		home:   home,
		repo:   repo,
		memBin: memBin,
		rec:    &recorder{},
		look:   map[string]bool{}, // no harness CLI installed by default
	}
}

func (f *fixture) opts() Options {
	return Options{
		Home:     f.home,
		Repo:     f.repo,
		MemBin:   f.memBin,
		Run:      f.rec.exec,
		LookPath: func(name string) (string, bool) { return "", f.look[name] },
		Now:      func() time.Time { return testNow },
	}
}

func (f *fixture) run(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := RunWithOptions(f.opts(), argv, &out, &errb)
	return code, out.String(), errb.String()
}

func (f *fixture) write(t *testing.T, rel, content string) {
	t.Helper()
	path := filepath.Join(f.home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// backups counts <file>.bak-* files under home.
func (f *fixture) backups(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.Walk(f.home, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(filepath.Base(path), ".bak-") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func (f *fixture) allCLIs() {
	for _, cli := range []string{
		"claude", "codex", "gemini", "opencode", "hermes", "cursor-agent", "omp",
		"kiro-cli", "qoder",
	} {
		f.look[cli] = true
	}
}

// decode parses a config file into a plain map for assertions.
func decode(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("config is not valid JSON %q: %v", doc, err)
	}
	return m
}

// ---------------------------------------------------------------------------
// Spec test 1: dry run exits 0, prints the plan, writes nothing.
// ---------------------------------------------------------------------------

func TestDryRunExitsZeroAndWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.allCLIs()
	f.write(t, ".claude/settings.json", `{"model":"sonnet","core":{"hooksPath":"/custom/hooks"}}`)

	code, out, errOut := f.run(t) // no flags → dry run

	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %q)", code, ExitOK, errOut)
	}
	for _, want := range []string{
		"== dry-run",
		"[dry-run] claude mcp add --scope user my-memory -e MEM_AGENT=claude -- " + f.memBin + " mcp",
		"cursor: เขียน " + filepath.Join(f.home, ".cursor", "mcp.json"),
		"omp: เขียน " + filepath.Join(f.home, ".omp", "agent", "mcp.json"),
		"claude -> " + filepath.Join(f.home, ".claude", "settings.json"),
		"hermes -> ~/.hermes/config.yaml",
		"[dry-run] ln -s " + filepath.Join(f.repo, "harness/omp/my-memory.ts"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q\n--- got ---\n%s", want, out)
		}
	}
	// Spec guard: core.hooksPath must be flagged in the plan too.
	if !strings.Contains(errOut, "core.hooksPath") {
		t.Errorf("dry run did not warn about core.hooksPath, stderr: %q", errOut)
	}
	// Nothing was executed beyond the read-only `mcp list` probe, and no
	// write happened anywhere under home.
	for _, call := range f.rec.calls {
		if strings.Contains(call, " mcp add") {
			t.Errorf("dry run executed a write command: %s", call)
		}
	}
	if got := f.read(t, ".claude/settings.json"); got != `{"model":"sonnet","core":{"hooksPath":"/custom/hooks"}}` {
		t.Errorf("dry run modified settings.json: %s", got)
	}
	if baks := f.backups(t); len(baks) != 0 {
		t.Errorf("dry run created backups: %v", baks)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".cursor")); !os.IsNotExist(err) {
		t.Error("dry run created the cursor config dir")
	}
}

// ---------------------------------------------------------------------------
// Spec test 2: apply mode without --yes is refused before anything runs.
// ---------------------------------------------------------------------------

func TestApplyWithoutYesRefused(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".claude/settings.json", `{"model":"sonnet"}`)

	for _, argv := range [][]string{
		{"--hooks"},
		{"--mcp"},
		{"--all"},
	} {
		code, _, errOut := f.run(t, argv...)
		if code != ExitUsage {
			t.Errorf("%v: exit = %d, want %d (usage error)", argv, code, ExitUsage)
		}
		if !strings.Contains(errOut, "--yes") {
			t.Errorf("%v: refusal must tell the user about --yes, stderr: %q", argv, errOut)
		}
	}
	if got := f.read(t, ".claude/settings.json"); got != `{"model":"sonnet"}` {
		t.Errorf("refused run modified settings.json: %s", got)
	}
	if len(f.rec.calls) != 0 {
		t.Errorf("refused run executed commands: %v", f.rec.calls)
	}
	if baks := f.backups(t); len(baks) != 0 {
		t.Errorf("refused run created backups: %v", baks)
	}
}

// ---------------------------------------------------------------------------
// Spec test 3: apply with --yes merges hooks and leaves a .bak-<stamp>
// behind; a second apply is a no-op.
// ---------------------------------------------------------------------------

func TestApplyHooksCreatesBackupAndMerges(t *testing.T) {
	f := newFixture(t)
	const origSettings = `{"model":"sonnet","env":{"FOO":"bar"}}`
	const origCodex = `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`
	f.write(t, ".claude/settings.json", origSettings)
	f.write(t, ".codex/hooks.json", origCodex)

	code, out, _ := f.run(t, "--hooks", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (out: %s)", code, ExitOK, out)
	}

	// Backup holds the pre-install bytes, next to the live file.
	settingsBak := filepath.Join(f.home, ".claude", "settings.json.bak-20261108150405")
	if b, err := os.ReadFile(settingsBak); err != nil || string(b) != origSettings {
		t.Errorf("backup missing or wrong content (err=%v): %s", err, b)
	}
	codexBak := filepath.Join(f.home, ".codex", "hooks.json.bak-20261108150405")
	if b, err := os.ReadFile(codexBak); err != nil || string(b) != origCodex {
		t.Errorf("codex backup missing or wrong content (err=%v): %s", err, b)
	}

	// settings.json: original keys intact and first (order preserved), hooks
	// added in the grouped schema with the engine binary as the command.
	doc := decode(t, f.read(t, ".claude/settings.json"))
	if doc["model"] != "sonnet" || doc["env"].(map[string]any)["FOO"] != "bar" {
		t.Errorf("original settings keys lost: %s", f.read(t, ".claude/settings.json"))
	}
	if strings.Index(f.read(t, ".claude/settings.json"), `"model"`) > strings.Index(f.read(t, ".claude/settings.json"), `"hooks"`) {
		t.Errorf("hooks was not appended after existing keys: %s", f.read(t, ".claude/settings.json"))
	}
	hooks := doc["hooks"].(map[string]any)
	wantCmd := f.memBin + " hook --agent claude --event session-start"
	first := hooks["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if first["command"] != wantCmd || first["type"] != "command" || first["timeout"].(float64) != 20 {
		t.Errorf("SessionStart entry = %v, want command=%s type=command timeout=20", first, wantCmd)
	}
	for _, ev := range []string{"UserPromptSubmit", "Stop"} {
		if _, ok := hooks[ev].([]any); !ok {
			t.Errorf("settings.json missing %s entry: %s", ev, f.read(t, ".claude/settings.json"))
		}
	}

	// codex: the unrelated user hook survives, the three events are added.
	codexHooks := decode(t, f.read(t, ".codex/hooks.json"))["hooks"].(map[string]any)
	ss := codexHooks["SessionStart"].([]any)
	if len(ss) != 2 {
		t.Fatalf("codex SessionStart groups = %d, want 2 (user + mem): %s", len(ss), f.read(t, ".codex/hooks.json"))
	}
	if ss[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != "echo hi" {
		t.Errorf("user hook lost: %s", f.read(t, ".codex/hooks.json"))
	}

	// gemini is not installed (no ~/.gemini): silently skipped, not created.
	if _, err := os.Stat(filepath.Join(f.home, ".gemini")); !os.IsNotExist(err) {
		t.Error("installer created .gemini for an uninstalled harness")
	}

	// Idempotent rerun: no duplicate entries, no new backups.
	code, out2, _ := f.run(t, "--hooks", "--yes")
	if code != ExitOK {
		t.Fatalf("second apply exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out2, "มีอยู่แล้ว ข้าม") {
		t.Errorf("second apply did not detect existing hooks: %s", out2)
	}
	if got := len(f.backups(t)); got != 2 {
		t.Errorf("second apply created new backups (total %d, want 2): %v", got, f.backups(t))
	}
	codexHooks2 := decode(t, f.read(t, ".codex/hooks.json"))["hooks"].(map[string]any)
	if got := len(codexHooks2["SessionStart"].([]any)); got != 2 {
		t.Errorf("second apply duplicated SessionStart groups: %d", got)
	}
}

// The core.hooksPath guard also fires during a real apply, as a warning —
// the merge still lands (claude may load hooks from either place).
func TestApplyWarnsCoreHooksPath(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".claude/settings.json", `{"core":{"hooksPath":"/custom/hooks"}}`)

	code, _, errOut := f.run(t, "--hooks", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(errOut, "core.hooksPath") || !strings.Contains(errOut, "/custom/hooks") {
		t.Errorf("missing core.hooksPath warning, stderr: %q", errOut)
	}
	if !strings.Contains(f.read(t, ".claude/settings.json"), "SessionStart") {
		t.Error("warning should not block the merge")
	}
}

// ---------------------------------------------------------------------------
// MCP: CLI-owned registries get `mcp add`; cursor/omp get merged JSON files.
// ---------------------------------------------------------------------------

func TestApplyMCPRegistersServers(t *testing.T) {
	f := newFixture(t)
	f.allCLIs()
	f.rec.listOut = map[string]string{"codex": "my-memory: already there"}
	f.write(t, ".cursor/mcp.json", `{"mcpServers":{"other":{"command":"x"}}}`)

	code, out, _ := f.run(t, "--mcp", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (out: %s)", code, ExitOK, out)
	}

	// claude was registered through its CLI; codex already had the server.
	addCalls := 0
	for _, c := range f.rec.calls {
		if strings.HasPrefix(c, "claude mcp add --scope user my-memory") {
			addCalls++
		}
		if strings.HasPrefix(c, "codex mcp add") {
			t.Errorf("codex already had my-memory but add ran anyway: %s", c)
		}
	}
	if addCalls != 1 {
		t.Errorf("claude mcp add ran %d times, want 1 (calls: %v)", addCalls, f.rec.calls)
	}
	if !strings.Contains(out, "codex: มีอยู่แล้ว ข้าม") {
		t.Errorf("codex skip not reported: %s", out)
	}

	// cursor: other server preserved, my-memory appended, original backed up.
	cursor := f.read(t, ".cursor/mcp.json")
	servers := decode(t, cursor)["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("existing cursor server lost: %s", cursor)
	}
	mm := servers["my-memory"].(map[string]any)
	if mm["command"] != f.memBin {
		t.Errorf("cursor my-memory.command = %v, want %s", mm["command"], f.memBin)
	}
	if args := mm["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Errorf("cursor my-memory.args = %v, want [mcp]", mm["args"])
	}
	if env := mm["env"].(map[string]any); env["MEM_AGENT"] != "cursor-agent" {
		t.Errorf("cursor MEM_AGENT = %v, want cursor-agent", env["MEM_AGENT"])
	}
	if b, err := os.ReadFile(filepath.Join(f.home, ".cursor", "mcp.json.bak-20261108150405")); err != nil || string(b) != `{"mcpServers":{"other":{"command":"x"}}}` {
		t.Errorf("cursor backup wrong (err=%v): %s", err, b)
	}

	// omp: file created fresh (no backup), oh-my-pi agent id.
	ompEnv := decode(t, f.read(t, ".omp/agent/mcp.json"))["mcpServers"].(map[string]any)["my-memory"].(map[string]any)["env"].(map[string]any)
	if ompEnv["MEM_AGENT"] != "oh-my-pi" {
		t.Errorf("omp MEM_AGENT = %v, want oh-my-pi", ompEnv["MEM_AGENT"])
	}
	if baks := f.backups(t); len(baks) != 1 {
		t.Errorf("backups = %v, want only the cursor one", baks)
	}

	// Idempotent rerun: files detected as installed, claude now listed.
	beforeRerun := len(f.rec.calls)
	_, out2, _ := f.run(t, "--mcp", "--yes")
	if !strings.Contains(out2, "cursor: มีอยู่แล้ว ข้าม") || !strings.Contains(out2, "omp: มีอยู่แล้ว ข้าม") {
		t.Errorf("second run did not skip installed file configs: %s", out2)
	}
	for _, c := range f.rec.calls[beforeRerun:] {
		if strings.HasPrefix(c, "claude mcp add") {
			t.Errorf("claude add repeated after registry listed my-memory: %s", c)
		}
	}
}

// kiro and qoder keep their own MCP registries, but neither can be probed
// with `mcp list` like claude/codex do: kiro writes that listing to stderr
// (its probe is `mcp status --name`, exit code only) and qoder's list spawns
// and connects to every configured server. Both are MCP-only — the hook
// section must never touch them.
func TestMCPKiroAndQoder(t *testing.T) {
	f := newFixture(t)
	f.look["kiro-cli"] = true
	f.look["qoder"] = true

	code, out, errOut := f.run(t, "--mcp", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitOK, errOut)
	}

	wantKiro := quoteCmd("kiro-cli", []string{"mcp", "add", "--name", "my-memory", "--scope", "global",
		"--command", f.memBin, "--args", "mcp", "--env", "MEM_AGENT=kiro"})
	// qoder's --env is variadic: the joined form is the only one that does
	// not swallow the positional name/command.
	wantQoder := quoteCmd("qoder", []string{"mcp", "add", "my-memory", f.memBin, "mcp", "-s", "user", "--env=MEM_AGENT=qoder"})
	kiroAdds, qoderAdds := 0, 0
	for _, c := range f.rec.calls {
		switch c {
		case wantKiro:
			kiroAdds++
		case wantQoder:
			qoderAdds++
		}
	}
	if kiroAdds != 1 || qoderAdds != 1 {
		t.Errorf("add calls: kiro=%d qoder=%d, want 1 each (calls: %v)", kiroAdds, qoderAdds, f.rec.calls)
	}

	wantProbe := quoteCmd("kiro-cli", []string{"mcp", "status", "--name", "my-memory"})
	probed, listed := false, false
	for _, c := range f.rec.calls {
		switch c {
		case wantProbe:
			probed = true
		case quoteCmd("kiro-cli", []string{"mcp", "list"}):
			listed = true
		}
	}
	if !probed {
		t.Errorf("kiro probe did not use `mcp status --name`: %v", f.rec.calls)
	}
	if listed {
		t.Errorf("kiro was probed with `mcp list`, whose listing goes to stderr: %v", f.rec.calls)
	}

	// Second apply: both registries report the server — no re-registration.
	before := len(f.rec.calls)
	_, out2, _ := f.run(t, "--mcp", "--yes")
	if !strings.Contains(out2, "kiro: มีอยู่แล้ว ข้าม") || !strings.Contains(out2, "qoder: มีอยู่แล้ว ข้าม") {
		t.Errorf("re-run did not skip kiro/qoder: %s", out2)
	}
	for _, c := range f.rec.calls[before:] {
		if strings.HasPrefix(c, "kiro-cli mcp add") || strings.HasPrefix(c, "qoder mcp add") {
			t.Errorf("re-run registered an existing server: %s", c)
		}
	}

	// MCP-only: a full apply must not create any kiro/qoder hook config.
	if code, _, _ := f.run(t, "--all", "--yes"); code != ExitOK {
		t.Fatalf("--all exit = %d, want %d", code, ExitOK)
	}
	err := filepath.Walk(f.home, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, "kiro") || strings.Contains(path, "qoder") {
			t.Errorf("hook/asset file written for an MCP-only harness: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kiro") || !strings.Contains(out, "qoder") {
		t.Errorf("plan did not report kiro/qoder: %s", out)
	}
}

// ---------------------------------------------------------------------------
// opencode plugin / omp extension symlinks.
// ---------------------------------------------------------------------------

func TestApplyHookSymlinks(t *testing.T) {
	f := newFixture(t)
	f.look["opencode"] = true
	f.look["omp"] = true
	f.write(t, ".claude/settings.json", `{"model":"x"}`) // claude dir exists → grouped section runs

	for _, rel := range []string{"harness/omp/my-memory.ts", "harness/opencode/my-memory.js"} {
		src := filepath.Join(f.repo, rel)
		if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(src, []byte("// asset"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, _, _ := f.run(t, "--hooks", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	dest := filepath.Join(f.home, ".omp/agent/extensions/my-memory.ts")
	want := filepath.Join(f.repo, "harness/omp/my-memory.ts")
	got, err := os.Readlink(dest)
	if err != nil || got != want {
		t.Fatalf("symlink = %q (err=%v), want %q", got, err, want)
	}
	// The opencode plugin link is verified in the same run: both symlinks
	// must exist when both assets ship in the repo.
	if got, err := os.Readlink(filepath.Join(f.home, ".config/opencode/plugins/my-memory.js")); err != nil || got != filepath.Join(f.repo, "harness/opencode/my-memory.js") {
		t.Errorf("opencode plugin symlink = %q (err=%v)", got, err)
	}
}

func TestApplyHookSymlinkMissingAssetFailsLoudly(t *testing.T) {
	f := newFixture(t)
	f.look["omp"] = true
	f.write(t, ".claude/settings.json", `{"model":"x"}`)

	code, _, errOut := f.run(t, "--hooks", "--yes")
	if code != ExitError {
		t.Fatalf("exit = %d, want %d (missing repo asset must fail the run)", code, ExitError)
	}
	if !strings.Contains(errOut, "asset ยังไม่ถูก port") {
		t.Errorf("missing-asset message not reported: %q", errOut)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".omp/agent/extensions/my-memory.ts")); !os.IsNotExist(err) {
		t.Error("symlink created although the source is missing")
	}
	// The failure of one harness must not stop the others: claude still merged.
	if !strings.Contains(f.read(t, ".claude/settings.json"), "SessionStart") {
		t.Error("claude merge skipped after an unrelated harness failure")
	}
}

func TestApplySymlinksSkippedWithoutRepo(t *testing.T) {
	f := newFixture(t)
	f.look["omp"] = true
	f.write(t, ".claude/settings.json", `{"model":"x"}`)
	f.repo = "" // no --repo

	code, out, _ := f.run(t, "--hooks", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (skip, not failure)", code, ExitOK)
	}
	if !strings.Contains(out, "ข้าม — ไม่ได้ระบุ --repo") {
		t.Errorf("missing --repo skip not explained: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Malformed configs: warn, skip that file, keep going — but report it.
// ---------------------------------------------------------------------------

func TestApplyInvalidJSONWarnsAndSkips(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".claude/settings.json", `{"model":"x"}`)
	f.write(t, ".codex/hooks.json", `{not json`)

	code, _, errOut := f.run(t, "--hooks", "--yes")
	if code != ExitError {
		t.Fatalf("exit = %d, want %d (a skipped harness is a failed step)", code, ExitError)
	}
	if !strings.Contains(errOut, "ข้าม (แก้ด้วยมือ)") {
		t.Errorf("skip not reported: %q", errOut)
	}
	if got := f.read(t, ".codex/hooks.json"); got != `{not json` {
		t.Errorf("malformed file was modified: %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".codex", "hooks.json.bak-20261108150405")); !os.IsNotExist(err) {
		t.Error("backup created for a file that was never written")
	}
	if !strings.Contains(f.read(t, ".claude/settings.json"), "SessionStart") {
		t.Error("claude merge skipped after codex config error")
	}
}

// ---------------------------------------------------------------------------
// Run-level flag/exit contract.
// ---------------------------------------------------------------------------

func TestRunUsageErrors(t *testing.T) {
	f := newFixture(t)
	for _, argv := range [][]string{
		{"--nope"},
		{"extra-arg"},
	} {
		if code, _, _ := f.run(t, argv...); code != ExitUsage {
			t.Errorf("%v: exit = %d, want %d", argv, code, ExitUsage)
		}
	}
	if code, _, errOut := f.run(t, "--help"); code != ExitOK || !strings.Contains(errOut, "usage: mem-install") {
		t.Errorf("--help: exit = %d, want %d", code, ExitOK)
	}
}

func TestRunMissingMemBinaryFails(t *testing.T) {
	f := newFixture(t)
	f.memBin = filepath.Join(f.home, "bin", "does-not-exist")
	code, _, errOut := f.run(t, "--all", "--yes")
	if code != ExitError {
		t.Fatalf("exit = %d, want %d", code, ExitError)
	}
	if !strings.Contains(errOut, "ไม่พบ executable") {
		t.Errorf("missing binary not reported: %q", errOut)
	}
}

// TestCursorHooksFlatSchema: cursor's own flat hook schema, reached when the
// CLI exists even though ~/.cursor does not.
func TestCursorHooksFlatSchema(t *testing.T) {
	f := newFixture(t)
	f.look["cursor-agent"] = true

	code, _, _ := f.run(t, "--hooks", "--yes")
	if code != ExitOK {
		t.Fatal("exit != 0")
	}
	doc := decode(t, f.read(t, ".cursor/hooks.json"))
	if v, ok := doc["version"].(float64); !ok || v != 1 {
		t.Errorf("cursor version = %v, want 1", doc["version"])
	}
	hooks := doc["hooks"].(map[string]any)
	first := hooks["sessionStart"].([]any)[0].(map[string]any)
	wantCmd := f.memBin + " hook --agent cursor-agent --event session-start --format cursor"
	if first["command"] != wantCmd || first["timeout"].(float64) != 20 {
		t.Errorf("cursor sessionStart = %v, want command=%s timeout=20", first, wantCmd)
	}
	if _, ok := hooks["beforeSubmitPrompt"].([]any); !ok {
		t.Errorf("cursor beforeSubmitPrompt missing: %s", f.read(t, ".cursor/hooks.json"))
	}
}
