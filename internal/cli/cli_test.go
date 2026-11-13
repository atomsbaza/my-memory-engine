// Router, help, manifest and exit-code contract tests (AC-P2-07, AC-P2-08,
// NFR-COMPAT-01).
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/version"
)

// TestMain isolates every CLI test from the developer's real memory home:
// HOME points at a scratch dir with no ~/my-memory, and the home overrides are
// cleared, so a missing-home test means what it says.
func TestMain(m *testing.M) {
	scratch, err := os.MkdirTemp("", "mem-cli-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", scratch)
	if runtime.GOOS == "windows" {
		// os.UserHomeDir reads USERPROFILE on Windows, not HOME.
		os.Setenv("USERPROFILE", scratch)
	}
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(scratch, ".config"))
	for _, key := range []string{"MEM_HOME", "MY_MEMORY_HOME", "MEM_AGENT"} {
		os.Unsetenv(key)
	}
	code := m.Run()
	os.RemoveAll(scratch)
	os.Exit(code)
}

// legacyNames is the frozen Python `add_parser` manifest (E4 snapshot shape).
var legacyNames = []string{
	"context", "search", "list", "show", "add", "touch", "cite",
	"review", "supersede", "verify", "reindex", "archive", "promote",
	"compact", "render", "sync", "import", "hook", "stats", "eval", "mcp",
}

func TestManifestCounts(t *testing.T) {
	if LegacyCommandCount != len(legacyNames) {
		t.Fatalf("LegacyCommandCount = %d, want %d", LegacyCommandCount, len(legacyNames))
	}
	manifest := Manifest()
	if len(manifest) != CommandCount {
		t.Fatalf("Manifest() = %d commands, want CommandCount %d", len(manifest), CommandCount)
	}
	if CommandCount != LegacyCommandCount+4 {
		t.Fatalf("CommandCount = %d, want 21 legacy + init + doctor + embed + links", CommandCount)
	}
	legacy := 0
	for _, entry := range manifest {
		if !entry.Legacy {
			continue
		}
		name := legacyNames[legacy]
		legacy++
		if entry.Name != name {
			t.Errorf("legacy command #%d = %q, want %q (registry order is frozen)", legacy-1, entry.Name, name)
		}
	}
	for _, extra := range []string{"init", "doctor", "embed", "links"} {
		entry, ok := manifestEntry(extra)
		if !ok {
			t.Fatalf("manifest is missing %q", extra)
		}
		if entry.Legacy {
			t.Errorf("%s: Legacy = true, want false (engine-only command)", extra)
		}
	}
	// Every command carries a help line and the names are unique.
	seen := map[string]bool{}
	for _, c := range manifest {
		if c.Help == "" {
			t.Errorf("%s: empty help", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("%s: duplicate registry entry", c.Name)
		}
		seen[c.Name] = true
	}
}

func manifestEntry(name string) (Entry, bool) {
	for _, c := range Manifest() {
		if c.Name == name {
			return c, true
		}
	}
	return Entry{}, false
}

// TestDocumentedManifestMatchesCode is the NFR-COMPAT-01 drift gate: the
// generated docs must list exactly the registered commands and the documented
// count must equal the code count.
func TestDocumentedManifestMatchesCode(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "cli-commands.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate the doc from Manifest())", path, err)
	}
	want := renderManifestDoc()
	if string(got) != want {
		t.Fatalf("%s is out of date — regenerate it:\n\n%s", path, want)
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, argv := range [][]string{{}, {"-h"}, {"--help"}} {
		code, out, stderr := runMain(t, "", argv...)
		if code != ExitOK {
			t.Errorf("%v: exit = %d, want %d (stderr: %q)", argv, code, ExitOK, stderr)
		}
		if !strings.Contains(out, "usage: mem") {
			t.Errorf("%v: stdout missing usage line, got %q", argv, out)
		}
		if stderr != "" {
			t.Errorf("%v: help wrote to stderr: %q", argv, stderr)
		}
	}
}

// TestVersionFlag: `mem --version` (before any subcommand) prints the single
// source-of-truth version and exits 0. After a subcommand it is that
// subcommand's problem (argparse parity: unknown flag → exit 2).
func TestVersionFlag(t *testing.T) {
	code, out, stderr := runMain(t, "", "--version")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %q)", code, ExitOK, stderr)
	}
	want := "mem version " + version.Version + "\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	// After the subcommand name the flag belongs to the subcommand: unknown
	// flag → usage error exit 2 (argparse parity, not a global override).
	code, _, _ = runMain(t, "", "verify", "--version")
	if code != ExitGuard {
		t.Fatalf("post-subcommand --version exit = %d, want %d", code, ExitGuard)
	}
}

func TestHelpListsAllCommands(t *testing.T) {
	if got := strings.Join(CommandNames(), ","); got != strings.Join(allNames(), ",") {
		t.Fatalf("CommandNames() = %s, want %s", got, strings.Join(allNames(), ","))
	}
	_, out, _ := runMain(t, "")
	for _, name := range allNames() {
		if !strings.Contains(out, name) {
			t.Errorf("root help does not mention %q", name)
		}
	}
	if !strings.Contains(out, "{context,search,list,show,links,add,touch,cite,review,supersede,verify,reindex,archive,promote,compact,render,sync,import,hook,stats,eval,mcp,init,doctor,embed}") {
		t.Errorf("usage line does not list the full surface:\n%s", out)
	}
}

func allNames() []string {
	// Registry order: links sits right after show, inside the legacy block.
	names := append([]string{}, legacyNames[:4]...)
	names = append(names, "links")
	return append(append(names, legacyNames[4:]...), "init", "doctor", "embed")
}

// TestUnknownCommand: argparse parity — usage errors go to stderr, exit 2, and
// never pollute stdout.
func TestUnknownCommand(t *testing.T) {
	code, out, stderr := runMain(t, "", "frobnicate")
	if code != ExitGuard {
		t.Errorf("unknown command: exit = %d, want %d (ExitGuard)", code, ExitGuard)
	}
	if stderr == "" {
		t.Error("unknown command: nothing on stderr")
	}
	if out != "" {
		t.Errorf("unknown command: wrote to stdout: %q", out)
	}
}

// TestUnknownFlagIsUsageError: an undefined flag exits 2 and keeps stdout
// empty (the --json purity contract depends on it).
func TestUnknownFlagIsUsageError(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runHome(t, home, "", "verify", "--bogus")
	if code != ExitGuard {
		t.Fatalf("verify --bogus: exit = %d, want %d\nstderr: %s", code, ExitGuard, stderr)
	}
	if out != "" {
		t.Errorf("verify --bogus wrote to stdout: %q", out)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("stderr does not name the flag: %q", stderr)
	}
}

// TestSubcommandHelp: -h anywhere prints that command's usage on stdout, exit 0.
func TestSubcommandHelp(t *testing.T) {
	for _, argv := range [][]string{
		{"add", "-h"},
		{"add", "--layer", "lesson", "-h"},
		{"verify", "--help"},
	} {
		code, out, stderr := runMain(t, "", argv...)
		if code != ExitOK {
			t.Errorf("%v: exit = %d, want %d (stderr: %q)", argv, code, ExitOK, stderr)
		}
		if !strings.Contains(out, "usage: mem "+argv[0]) {
			t.Errorf("%v: stdout = %q, want the command usage line", argv, out)
		}
		if stderr != "" {
			t.Errorf("%v: help wrote to stderr: %q", argv, stderr)
		}
	}
}

// TestHomeFlagForms: --home VALUE and --home=VALUE both resolve the home.
// The global flag must precede the subcommand, exactly like argparse.
func TestHomeFlagForms(t *testing.T) {
	home := fixtureHome(t)
	for _, argv := range [][]string{
		{"--home", home, "verify"},
		{"--home=" + home, "verify"},
	} {
		code, out, stderr := runMain(t, "", argv...)
		if code != ExitOK {
			t.Errorf("%v: exit = %d, want %d (stderr: %q)", argv, code, ExitOK, stderr)
			continue
		}
		if !strings.Contains(out, "problems") {
			t.Errorf("%v: routed away from verify, stdout = %q", argv, out)
		}
	}
}

// TestHomeFlagAfterCommandIsUsageError: argparse defines --home on the root
// parser only, so a trailing --home belongs to the subcommand and is rejected.
func TestHomeFlagAfterCommandIsUsageError(t *testing.T) {
	home := fixtureHome(t)
	code, out, stderr := runMain(t, "", "verify", "--home", home)
	if code != ExitGuard {
		t.Fatalf("verify --home: exit = %d, want %d (stdout %q)", code, ExitGuard, out)
	}
	if !strings.Contains(stderr, "home") {
		t.Errorf("stderr does not name the flag: %q", stderr)
	}
}

// TestHomeFlagRequiresValue: a dangling --home is a usage error (exit 2).
func TestHomeFlagRequiresValue(t *testing.T) {
	code, _, stderr := runMain(t, "", "--home")
	if code != ExitGuard {
		t.Errorf("dangling --home: exit = %d, want %d", code, ExitGuard)
	}
	if stderr == "" {
		t.Error("dangling --home: nothing on stderr")
	}
}

// TestMissingHomeFailsLoudly: with no resolvable home (no --home, no
// MEM_HOME, no repo above the cwd, no ~/my-memory), commands exit 1 and say so
// instead of pretending success.
func TestMissingHomeFailsLoudly(t *testing.T) {
	for _, argv := range [][]string{{"verify"}, {"list"}, {"stats"}, {"reindex"}} {
		code, out, stderr := runMain(t, "", argv...)
		if code != ExitError {
			t.Errorf("%v: exit = %d, want %d (stdout %q)", argv, code, ExitError, out)
		}
		if stderr == "" {
			t.Errorf("%v: nothing on stderr", argv)
		}
	}
}

// TestExitCodeConstants pins the contract values: 0/1/2/3.
func TestExitCodeConstants(t *testing.T) {
	cases := map[string]int{
		"ExitOK": ExitOK, "ExitError": ExitError, "ExitGuard": ExitGuard, "ExitConflict": ExitConflict,
	}
	for name, want := range map[string]int{"ExitOK": 0, "ExitError": 1, "ExitGuard": 2, "ExitConflict": 3} {
		if got := cases[name]; got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

// TestGenerateCLIDoc regenerates docs/cli-commands.md when UPDATE_CLI_DOC=1.
// Without it the test is a no-op; TestDocumentedManifestMatchesCode is the
// gate that actually fails on drift.
func TestGenerateCLIDoc(t *testing.T) {
	if os.Getenv("UPDATE_CLI_DOC") == "" {
		t.Skip("set UPDATE_CLI_DOC=1 to regenerate docs/cli-commands.md")
	}
	path := filepath.Join("..", "..", "docs", "cli-commands.md")
	if err := os.WriteFile(path, []byte(renderManifestDoc()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
