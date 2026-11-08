package cli

import (
	"bytes"
	"strings"
	"testing"
)

// runMain executes Main against pipes and returns (exit, stdout, stderr).
func runMain(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(argv, &out, &errb)
	return code, out.String(), errb.String()
}

// TestHelpExitsZero proves the help contract: bare invocation and both
// help flags print usage on stdout and exit 0.
func TestHelpExitsZero(t *testing.T) {
	for _, argv := range [][]string{{}, {"-h"}, {"--help"}} {
		code, out, stderr := runMain(t, argv...)
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

// TestHelpListsAllCommands: the command table carries the 18 subcommands
// in Python mem's order.
func TestHelpListsAllCommands(t *testing.T) {
	want := []string{
		"context", "search", "list", "show", "add", "touch", "cite",
		"review", "supersede", "verify", "reindex", "render", "sync",
		"import", "hook", "stats", "eval", "mcp",
	}
	if got := CommandNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CommandNames() = %v, want %v", got, want)
	}
	_, out, _ := runMain(t)
	for _, name := range want {
		if !strings.Contains(out, name) {
			t.Errorf("root help does not mention %q", name)
		}
	}
}

// TestStubCommandsReturnZero: every non-mcp subcommand is a stub that
// prints its own help and exits 0 (P2.2 replaces the bodies).
func TestStubCommandsReturnZero(t *testing.T) {
	for _, name := range CommandNames() {
		if name == "mcp" {
			continue // live subcommand — runs the stdio server, not a stub
		}
		code, out, stderr := runMain(t, name)
		if code != ExitOK {
			t.Errorf("mem %s: exit = %d, want %d (stderr: %q)", name, code, ExitOK, stderr)
		}
		if !strings.Contains(out, "usage: mem "+name) {
			t.Errorf("mem %s: stdout missing own usage, got %q", name, out)
		}
		if !strings.Contains(out, "stub") {
			t.Errorf("mem %s: stub marker missing, got %q", name, out)
		}
	}
}

// TestStubCommandIgnoresExtraArgs: the skeleton accepts and ignores
// per-command flags the real implementations will parse.
func TestStubCommandIgnoresExtraArgs(t *testing.T) {
	code, out, _ := runMain(t, "add", "--layer", "lesson", "-h")
	if code != ExitOK {
		t.Fatalf("mem add --layer lesson -h: exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out, "usage: mem add") {
		t.Fatalf("mem add with flags: stdout = %q", out)
	}
}

// TestUnknownCommand: argparse parity — usage errors go to stderr and
// exit 2 (the invalid-input class, alongside guard violations).
func TestUnknownCommand(t *testing.T) {
	code, out, stderr := runMain(t, "frobnicate")
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

// TestHomeFlagAccepted: --home VALUE and --home=VALUE parse without
// disturbing subcommand routing (P2.2 resolves the value).
func TestHomeFlagAccepted(t *testing.T) {
	for _, argv := range [][]string{
		{"--home", "/tmp/memhome", "verify"},
		{"--home=/tmp/memhome", "verify"},
		{"verify", "--home", "/tmp/memhome"},
	} {
		code, out, stderr := runMain(t, argv...)
		if code != ExitOK {
			t.Errorf("%v: exit = %d, want %d (stderr: %q)", argv, code, ExitOK, stderr)
			continue
		}
		if !strings.Contains(out, "usage: mem verify") {
			t.Errorf("%v: routed away from verify, stdout = %q", argv, out)
		}
	}
}

// TestHomeFlagRequiresValue: a dangling --home is a usage error (exit 2).
func TestHomeFlagRequiresValue(t *testing.T) {
	code, _, stderr := runMain(t, "--home")
	if code != ExitGuard {
		t.Errorf("dangling --home: exit = %d, want %d", code, ExitGuard)
	}
	if stderr == "" {
		t.Error("dangling --home: nothing on stderr")
	}
}

// TestExitCodeConstants pins the contract values: 0/1/2/3.
func TestExitCodeConstants(t *testing.T) {
	cases := map[string]int{
		"ExitOK":       ExitOK,
		"ExitError":    ExitError,
		"ExitGuard":    ExitGuard,
		"ExitConflict": ExitConflict,
	}
	for name, want := range map[string]int{"ExitOK": 0, "ExitError": 1, "ExitGuard": 2, "ExitConflict": 3} {
		if got := cases[name]; got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}
