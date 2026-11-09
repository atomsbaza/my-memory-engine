// Package harness wires harness CLIs (claude, codex, gemini, opencode,
// cursor, hermes, omp, kiro, qoder) to the engine: MCP server registration
// plus session hooks. kiro and qoder are MCP-only (no hook config the
// installer can merge). It is the P1.7 port of the Python repo's
// harness/install.sh, sections 1 (MCP) and 2 (hooks); the LaunchAgent
// (--backup, --promotion-cron) and git-hook (--git-hooks) sections stay in
// the Python repo until their engine counterparts exist.
//
// Contract shared with install.sh:
//
//   - No action flag        → dry run: print the plan, touch nothing, exit 0.
//   - --mcp / --hooks / --all without --yes → refuse (exit 2), touch nothing.
//   - Every file modified is first copied to <file>.bak-<timestamp>.
//   - Registration is idempotent: an existing entry is detected and skipped.
//   - hermes has no mergeable config path (YAML + consent flow) — the plan
//     prints the exact snippet to paste by hand, exactly like install.sh.
//
// Divergences from install.sh, on purpose:
//
//   - Hooks call the engine binary directly (`<mem> hook --agent … --event …`)
//     instead of routing through harness/mem-hook.sh: that wrapper existed to
//     locate python3, which a static Go binary does not need.
//   - A dry run prints the full per-harness plan; install.sh without flags
//     printed only a banner (its per-command [dry-run] branch was dead code).
//   - A failed apply step (mcp add error, unwritable file) makes the run exit
//     1 after finishing the remaining steps — install.sh exited 0 silently.
package harness

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Options configures an Installer. Zero values are filled in by New.
type Options struct {
	// Home is the harness config root (usually the user's home directory).
	// Default: os.UserHomeDir. Tests point this at a temp dir — nothing in
	// this package may fall back to the real home once Home is set.
	Home string

	// Repo is the engine repo root, the source of the opencode plugin and
	// omp extension symlinks. Empty disables both symlinks with a message.
	Repo string

	// MemBin is the engine binary registered with every harness. Default:
	// the "mem" sibling of the running executable.
	MemBin string

	// Out receives the plan/progress output; Err receives warnings.
	Out, Err io.Writer

	// Run executes a harness CLI (claude, codex, …). Default: exec.Command.
	Run func(name string, args ...string) (string, error)

	// LookPath reports whether a harness CLI is installed. Default: exec.LookPath.
	LookPath func(name string) (string, bool)

	// Now is the clock used for backup suffixes. Default: time.Now.
	Now func() time.Time
}

// Installer plans and applies the my-memory wiring for one user home.
type Installer struct {
	opt    Options
	errors []error
}

// New builds an Installer, applying defaults for any unset option.
func New(opt Options) *Installer {
	if opt.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			opt.Home = home
		}
	}
	if opt.MemBin == "" {
		if exe, err := os.Executable(); err == nil {
			opt.MemBin = filepath.Join(filepath.Dir(exe), "mem")
		}
	}
	if opt.Run == nil {
		opt.Run = func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).Output()
			return string(out), err
		}
	}
	if opt.LookPath == nil {
		opt.LookPath = func(name string) (string, bool) {
			path, err := exec.LookPath(name)
			return path, err == nil
		}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Out == nil {
		opt.Out = io.Discard
	}
	if opt.Err == nil {
		opt.Err = io.Discard
	}
	return &Installer{opt: opt}
}

func (i *Installer) say(format string, args ...any) {
	fmt.Fprintf(i.opt.Out, format+"\n", args...)
}

func (i *Installer) warn(format string, args ...any) {
	fmt.Fprintf(i.opt.Err, "  !! "+format+"\n", args...)
}

// fail records an apply-step failure; the run continues (partial failure must
// stay observable) and Apply returns the collected errors at the end.
func (i *Installer) fail(err error) {
	i.errors = append(i.errors, err)
	i.warn("%v", err)
}

// validate checks that the installer can run at all.
func (i *Installer) validate() error {
	if i.opt.Home == "" {
		return errors.New("ไม่ทราบ home directory (ตั้ง --home)")
	}
	fi, err := os.Stat(i.opt.MemBin)
	if err != nil {
		return fmt.Errorf("ไม่พบ executable %s — build แล้วติดตั้ง binary ก่อน (หรือใช้ --mem)", i.opt.MemBin)
	}
	// Windows has no execute bit: a regular file is executable there
	// (PATHEXT governs invocation), so the 0111 test is unix-only.
	if fi.IsDir() || (fi.Mode()&0o111 == 0 && runtime.GOOS != "windows") {
		return fmt.Errorf("ไม่พบ executable %s — build แล้วติดตั้ง binary ก่อน (หรือใช้ --mem)", i.opt.MemBin)
	}
	return nil
}

// ---------------------------------------------------------------------------
// MCP registration (install.sh §1)
// ---------------------------------------------------------------------------

// mcpCLI is a harness whose MCP registry is owned by its own CLI.
type mcpCLI struct {
	name string // harness display name
	bin  string // CLI binary to call
	// addArgs builds the `mcp add` argument list for the engine binary.
	addArgs func(mem string) []string
	// probeArgs replaces the default `<bin> mcp list` idempotency probe; with
	// it the server counts as registered iff the command exits 0. kiro needs
	// this: `kiro-cli mcp list` writes its listing to stderr (so
	// exec.Command.Output() sees nothing) while `mcp status --name` answers
	// with the exit code alone.
	probeArgs []string
}

var mcpCLIs = []mcpCLI{
	{name: "claude", bin: "claude", addArgs: func(mem string) []string {
		return []string{"mcp", "add", "--scope", "user", "my-memory", "-e", "MEM_AGENT=claude", "--", mem, "mcp"}
	}},
	{name: "codex", bin: "codex", addArgs: func(mem string) []string {
		return []string{"mcp", "add", "my-memory", "--env", "MEM_AGENT=codex", "--", mem, "mcp"}
	}},
	{name: "gemini", bin: "gemini", addArgs: func(mem string) []string {
		return []string{"mcp", "add", "--scope", "user", "--env", "MEM_AGENT=gemini", "my-memory", mem, "mcp"}
	}},
	{name: "opencode", bin: "opencode", addArgs: func(mem string) []string {
		return []string{"mcp", "add", "--global", "--env", "MEM_AGENT=opencode", "my-memory", "--", mem, "mcp"}
	}},
	{name: "hermes", bin: "hermes", addArgs: func(mem string) []string {
		return []string{"mcp", "add", "my-memory", "--command", mem, "--args", "mcp"}
	}},
	// kiro-cli + qoder: MCP only — neither has a session-hook config the
	// installer can merge (kiro hooks live inside agent JSON, qoder's hook
	// support is unverified), so they appear in mcpCLIs only.
	{name: "kiro", bin: "kiro-cli", addArgs: func(mem string) []string {
		// Default scope is workspace, which only loads inside a directory
		// that has its own .kiro/; global is ~/.kiro/settings/mcp.json and is
		// inherited by the built-in kiro_default agent (includeMcpJson).
		return []string{"mcp", "add", "--name", "my-memory", "--scope", "global",
			"--command", mem, "--args", "mcp", "--env", "MEM_AGENT=kiro"}
	}, probeArgs: []string{"mcp", "status", "--name", "my-memory"}},
	{name: "qoder", bin: "qoder", addArgs: func(mem string) []string {
		// --env is variadic, so it must be the joined form: `--env K=V` would
		// swallow the server name/command positionals as further env values.
		return []string{"mcp", "add", "my-memory", mem, "mcp", "-s", "user", "--env=MEM_AGENT=qoder"}
	}},
}

// mcpFile is a harness with a Claude-Desktop-style mcpServers JSON config
// that install.sh writes directly (the CLI has no `mcp add`).
type mcpFile struct {
	name     string // harness display name
	bin      string // CLI whose presence gates the write
	rel      string // config path relative to Home
	envAgent string // MEM_AGENT value for this harness
}

var mcpFiles = []mcpFile{
	{"cursor", "cursor-agent", ".cursor/mcp.json", "cursor-agent"},
	{"omp", "omp", ".omp/agent/mcp.json", "oh-my-pi"},
}

func (i *Installer) mcpSection(dry bool) {
	i.say("")
	i.say("== MCP server: my-memory ==")
	for _, h := range mcpCLIs {
		if _, ok := i.opt.LookPath(h.bin); !ok {
			i.say("  %s: ไม่พบ CLI — ข้าม", h.name)
			continue
		}
		// Read-only idempotency probe, also in dry run (install.sh parity).
		if i.mcpRegistered(h) {
			i.say("  %s: มีอยู่แล้ว ข้าม", h.name)
			continue
		}
		args := h.addArgs(i.opt.MemBin)
		if dry {
			i.say("  [dry-run] %s", quoteCmd(h.bin, args))
			continue
		}
		i.say("  $ %s", quoteCmd(h.bin, args))
		if _, err := i.opt.Run(h.bin, args...); err != nil {
			i.fail(fmt.Errorf("%s: mcp add: %w", h.name, err))
		}
	}
	for _, h := range mcpFiles {
		if _, ok := i.opt.LookPath(h.bin); !ok {
			i.say("  %s: ไม่พบ CLI — ข้าม", h.name)
			continue
		}
		i.mcpFile(h, dry)
	}
}

// mcpRegistered reports whether the CLI's registry already holds the
// my-memory server. Read-only: safe to run in dry-run mode.
func (i *Installer) mcpRegistered(h mcpCLI) bool {
	if len(h.probeArgs) > 0 {
		_, err := i.opt.Run(h.bin, h.probeArgs...)
		return err == nil
	}
	out, err := i.opt.Run(h.bin, "mcp", "list")
	return err == nil && strings.Contains(out, "my-memory")
}

// mcpFile merges {"mcpServers":{"my-memory":{command,args,env}}} into the
// harness config, preserving any other servers already registered.
func (i *Installer) mcpFile(h mcpFile, dry bool) {
	path := relToHome(i.opt.Home, h.rel)
	if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), `"my-memory"`) {
		i.say("  %s: มีอยู่แล้ว ข้าม", h.name)
		return
	}
	if dry {
		i.say("  %s: เขียน %s", h.name, path)
		return
	}
	v, err := loadJSONFile(path)
	if err != nil {
		i.fail(fmt.Errorf("%s: %s: %v — ข้าม (แก้ด้วยมือ)", h.name, path, err))
		return
	}
	root := asObj(v)
	if root == nil {
		i.fail(fmt.Errorf("%s: %s: ตัวไฟล์ไม่ใช่ JSON object — ข้าม (แก้ด้วยมือ)", h.name, path))
		return
	}
	servers := asObj(mapGet(root, "mcpServers"))
	if servers == nil {
		servers = newObj()
		root.Set("mcpServers", servers)
	}
	entry := newObj()
	entry.Set("command", i.opt.MemBin)
	entry.Set("args", []any{"mcp"})
	env := newObj()
	env.Set("MEM_AGENT", h.envAgent)
	entry.Set("env", env)
	servers.Set("my-memory", entry)
	if _, _, err := saveJSONFile(path, root, i.opt.Now()); err != nil {
		i.fail(fmt.Errorf("%s: %w", h.name, err))
		return
	}
	i.say("  %s: เขียน %s (สำรอง .bak ก่อนถ้าไฟล์เดิมมี)", h.name, path)
}

func mapGet(o *obj, key string) any {
	v, _ := o.Get(key)
	return v
}

// ---------------------------------------------------------------------------
// Hooks (install.sh §2)
// ---------------------------------------------------------------------------

// hookSpec maps one harness hook event to the engine hook invocation.
type hookSpec struct {
	event    string   // event key in the harness config
	memEvent string   // --event value passed to `mem hook`
	extra    []string // extra flags (--format / --event-name)
}

func (i *Installer) hookCommand(agent string, s hookSpec) string {
	args := append([]string{"hook", "--agent", agent, "--event", s.memEvent}, s.extra...)
	return quoteCmd(i.opt.MemBin, args)
}

// groupedHookFiles: claude/codex/gemini schema —
// {"hooks": {event: [{"hooks": [{"type":"command","command":…,"timeout":20}]}]}}
// hookFile is one harness's hook config file. Grouped files install only
// when the harness dir exists; flat files also install when their CLI is on
// PATH (install.sh: `command -v cursor-agent || [ -d ~/.cursor ]`).
type hookFile struct {
	name    string
	rel     string // config path relative to Home
	dir     string // harness dir whose presence gates install
	bin     string // optional CLI whose presence also gates install
	agent   string // --agent value
	entries []hookSpec
}

var groupedHookFiles = []hookFile{
	{"claude", ".claude/settings.json", ".claude", "", "claude", []hookSpec{
		{"SessionStart", "session-start", nil},
		{"UserPromptSubmit", "user-prompt", nil},
		{"Stop", "stop", nil},
	}},
	{"codex", ".codex/hooks.json", ".codex", "", "codex", []hookSpec{
		{"SessionStart", "session-start", nil},
		{"UserPromptSubmit", "user-prompt", nil},
		{"Stop", "stop", nil},
	}},
	{"gemini", ".gemini/settings.json", ".gemini", "", "gemini", []hookSpec{
		// gemini forces stdout to be JSON, hence --format json.
		{"SessionStart", "session-start", []string{"--format", "json"}},
		{"BeforeAgent", "user-prompt", []string{"--format", "json", "--event-name", "BeforeAgent"}},
	}},
}

// flatHookFiles: cursor's own schema —
// {"version":1,"hooks":{event:[{"command":…,"timeout":20}]}}
var flatHookFiles = []hookFile{
	{"cursor", ".cursor/hooks.json", ".cursor", "cursor-agent", "cursor-agent", []hookSpec{
		{"sessionStart", "session-start", []string{"--format", "cursor"}},
		{"beforeSubmitPrompt", "user-prompt", []string{"--format", "cursor", "--event-name", "beforeSubmitPrompt"}},
		{"stop", "stop", nil},
	}},
}

// linkAsset is a harness that takes hooks as a plugin/extension file,
// installed as a symlink from the user config into the engine repo.
type linkAsset struct {
	name string
	bin  string // CLI whose presence gates the install
	rel  string // symlink destination relative to Home
	src  string // symlink source relative to Repo
}

var linkAssets = []linkAsset{
	{"opencode", "opencode", ".config/opencode/plugins/my-memory.js", "harness/opencode/my-memory.js"},
	{"omp", "omp", ".omp/agent/extensions/my-memory.ts", "harness/omp/my-memory.ts"},
}

func (i *Installer) hooksSection(dry bool) {
	i.say("")
	i.say("== hooks (SessionStart = อ่าน memory, UserPromptSubmit = recall ตามคำขอ, Stop = commit) ==")
	for _, h := range groupedHookFiles {
		if _, err := os.Stat(relToHome(i.opt.Home, h.dir)); err != nil {
			continue // harness not installed on this machine — install.sh parity
		}
		path := relToHome(i.opt.Home, h.rel)
		i.say("  %s -> %s", h.name, path)
		if h.name == "claude" {
			i.warnHooksPath(path)
		}
		if dry {
			continue
		}
		for _, e := range h.entries {
			i.mergeGroupedHook(path, h.name, h.agent, e)
		}
	}
	for _, h := range flatHookFiles {
		cli := h.bin != "" && func() bool { _, ok := i.opt.LookPath(h.bin); return ok }()
		_, dirErr := os.Stat(relToHome(i.opt.Home, h.dir))
		if !cli && dirErr != nil {
			continue // no CLI and no config dir — harness not present
		}
		path := relToHome(i.opt.Home, h.rel)
		i.say("  %s -> %s  (schema cursor เอง: {\"version\":1,\"hooks\":{event:[{command}]}})", h.name, path)
		if dry {
			continue
		}
		for _, e := range h.entries {
			i.mergeFlatHook(path, h.name, h.agent, e)
		}
	}
	i.hermesManual()
	for _, a := range linkAssets {
		i.linkAsset(a, dry)
	}
}

// warnHooksPath implements the P1.7 spec guard: when claude's settings carry
// core.hooksPath, hooks are loaded from that path instead of settings.json,
// so a merged entry here can silently do nothing.
func (i *Installer) warnHooksPath(path string) {
	v, err := loadJSONFile(path)
	if err != nil {
		return // the merge will report the broken file
	}
	root := asObj(v)
	if root == nil {
		return
	}
	core := asObj(mapGet(root, "core"))
	if core == nil {
		return
	}
	if hp, ok := mapGet(core, "hooksPath").(string); ok && hp != "" {
		i.warn("%s มี core.hooksPath = %q — claude โหลด hooks จาก path นั้น ไม่ใช่จาก settings.json; entry ที่ merge ไว้ที่นี่อาจไม่ถูกใช้", path, hp)
	}
}

// mergeGroupedHook adds one event entry in the claude/codex/gemini schema.
func (i *Installer) mergeGroupedHook(path, name, agent string, s hookSpec) {
	v, err := loadJSONFile(path)
	if err != nil {
		i.fail(fmt.Errorf("%s: %s: %v — ข้าม (แก้ด้วยมือ)", name, path, err))
		return
	}
	root := asObj(v)
	if root == nil {
		i.fail(fmt.Errorf("%s: %s: ตัวไฟล์ไม่ใช่ JSON object — ข้าม (แก้ด้วยมือ)", name, path))
		return
	}
	hooks := asObj(mapGet(root, "hooks"))
	if hooks == nil {
		hooks = newObj()
		root.Set("hooks", hooks)
	}
	cmd := i.hookCommand(agent, s)
	list, _ := mapGet(hooks, s.event).([]any)
	for _, g := range list {
		group := asObj(g)
		if group == nil {
			continue
		}
		inner, _ := mapGet(group, "hooks").([]any)
		for _, h := range inner {
			if e := asObj(h); e != nil {
				if c, _ := mapGet(e, "command").(string); c == cmd {
					i.say("  %s: %s มีอยู่แล้ว ข้าม", filepath.Base(path), s.event)
					return
				}
			}
		}
	}
	entry := newObj()
	entry.Set("type", "command")
	entry.Set("command", cmd)
	entry.Set("timeout", jsonNumber(20))
	group := newObj()
	group.Set("hooks", []any{entry})
	hooks.Set(s.event, append(list, group))
	if _, _, err := saveJSONFile(path, root, i.opt.Now()); err != nil {
		i.fail(fmt.Errorf("%s: %w", name, err))
		return
	}
	i.say("  %s: เพิ่ม %s", filepath.Base(path), s.event)
}

// mergeFlatHook adds one event entry in cursor's flat schema.
func (i *Installer) mergeFlatHook(path, name, agent string, s hookSpec) {
	v, err := loadJSONFile(path)
	if err != nil {
		i.fail(fmt.Errorf("%s: %s: %v — ข้าม (แก้ด้วยมือ)", name, path, err))
		return
	}
	root := asObj(v)
	if root == nil {
		i.fail(fmt.Errorf("%s: %s: ตัวไฟล์ไม่ใช่ JSON object — ข้าม (แก้ด้วยมือ)", name, path))
		return
	}
	if !root.Has("version") {
		root.Set("version", jsonNumber(1))
	}
	hooks := asObj(mapGet(root, "hooks"))
	if hooks == nil {
		hooks = newObj()
		root.Set("hooks", hooks)
	}
	cmd := i.hookCommand(agent, s)
	list, _ := mapGet(hooks, s.event).([]any)
	for _, h := range list {
		if e := asObj(h); e != nil {
			if c, _ := mapGet(e, "command").(string); c == cmd {
				i.say("  %s: %s มีอยู่แล้ว ข้าม", filepath.Base(path), s.event)
				return
			}
		}
	}
	entry := newObj()
	entry.Set("command", cmd)
	entry.Set("timeout", jsonNumber(20))
	hooks.Set(s.event, append(list, entry))
	if _, _, err := saveJSONFile(path, root, i.opt.Now()); err != nil {
		i.fail(fmt.Errorf("%s: %w", name, err))
		return
	}
	i.say("  %s: เพิ่ม %s", filepath.Base(path), s.event)
}

// hermesManual prints the YAML snippet hermes needs by hand (install.sh
// parity: hermes config is YAML behind a consent flow — no auto-merge).
func (i *Installer) hermesManual() {
	i.say("")
	i.say("  hermes -> ~/.hermes/config.yaml (แก้ด้วยมือ — YAML ไม่ merge อัตโนมัติ):")
	i.say("    hooks:")
	i.say("      pre_llm_call:")
	i.say("        - command: %q   # จุดเดียวที่ inject context ได้", i.hookCommand("hermes", hookSpec{memEvent: "user-prompt", extra: []string{"--format", "hermes"}}))
	i.say("          timeout: 20")
	i.say("      on_session_end:")
	i.say("        - command: %q                          # commit งานที่ค้าง", i.hookCommand("hermes", hookSpec{memEvent: "stop"}))
	i.say("          timeout: 20")
	i.say("    แล้วรัน: hermes hooks doctor && hermes hooks list")
	i.say("    hook ใหม่ต้อง consent ครั้งแรก (TTY prompt) — สำหรับ gateway/cron ใช้ --accept-hooks หรือ HERMES_ACCEPT_HOOKS=1")
}

// linkAsset symlinks a plugin/extension from the user config into the repo.
func (i *Installer) linkAsset(a linkAsset, dry bool) {
	if _, ok := i.opt.LookPath(a.bin); !ok {
		i.say("  %s: ไม่พบ CLI — ข้าม", a.name)
		return
	}
	if i.opt.Repo == "" {
		i.say("  %s: ข้าม — ไม่ได้ระบุ --repo (ไม่ทราบตำแหน่ง %s)", a.name, a.src)
		return
	}
	src := filepath.Join(i.opt.Repo, filepath.FromSlash(a.src))
	dest := relToHome(i.opt.Home, a.rel)
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if cur, _ := os.Readlink(dest); cur == src {
			i.say("  %s: plugin มีอยู่แล้ว ข้าม", a.name)
			return
		}
	}
	if dry {
		i.say("  [dry-run] ln -s %s %s", src, dest)
		return
	}
	if _, err := os.Stat(src); err != nil {
		i.fail(fmt.Errorf("%s: ไม่พบ %s — ข้าม (asset ยังไม่ถูก port มา engine repo)", a.name, src))
		return
	}
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		backup := dest + ".bak-" + i.opt.Now().Format(backupTimeFormat)
		if err := copyFile(dest, backup); err != nil {
			i.fail(fmt.Errorf("%s: %w", a.name, err))
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		i.fail(fmt.Errorf("%s: %w", a.name, err))
		return
	}
	_ = os.Remove(dest)
	if err := os.Symlink(src, dest); err != nil {
		i.fail(fmt.Errorf("%s: symlink: %w", a.name, err))
		return
	}
	i.say("  %s: symlink %s -> %s", a.name, dest, src)
}

// ---------------------------------------------------------------------------
// Entry points
// ---------------------------------------------------------------------------

// DryRun prints the full installation plan (MCP + hooks for every harness
// found) without modifying anything. Read-only probes (CLI presence, existing
// registrations) are allowed so the plan can say "มีอยู่แล้ว ข้าม".
func (i *Installer) DryRun() error {
	i.say("== dry-run (ไม่แก้อะไร · ใส่ --mcp / --hooks / --all --yes เพื่อทำจริง) ==")
	i.install(true)
	return nil
}

// Apply writes the selected configurations, backing up every file first.
// Partial failure keeps going; the joined error reports every failed step.
func (i *Installer) Apply(mcp, hooks bool) error {
	i.errors = nil // an Installer is one run; never leak a previous run's failures
	if mcp {
		i.mcpSection(false)
	}
	if hooks {
		i.hooksSection(false)
	}
	i.say("")
	i.say("ตรวจว่าทุก harness ทำงานจริง:")
	i.say("  %s stats            # ใครเคยเขียน record บ้าง (writes = 0 คือยังไม่ต่อจริง)", i.opt.MemBin)
	return errors.Join(i.errors...)
}

// install drives both sections for a dry run.
func (i *Installer) install(dry bool) {
	i.mcpSection(dry)
	i.hooksSection(dry)
}
