// Package cli implements the mem command router (design §6 "cli" row):
// the 18 subcommands of the Python baseline plus its exit-code contract.
//
// This is the P1 skeleton: the router, the command registry, the exit-code
// contract and the mcp dispatch are final; the per-command bodies are stubs
// that print help and exit 0. Domain wiring (guards, dedup, lifecycle,
// render, sync) lands in P2.2 (plan.md P2.2 "CLI 18 commands").
//
// Exit-code contract (Python baseline mem; design §7 write path):
//
//	0  success
//	1  generic failure (repo not found, record not found, bad state)
//	2  invalid input — guard violations (secret / absolute-path / PII scan)
//	   and, in argparse parity, usage errors such as an unknown command
//	3  conflict — dedup block (jaccard >= 0.88) or supersede misuse
//
// The binary never prints to stdout from the mcp path: `mem mcp` hands the
// process to the MCP stdio server before any CLI output could pollute the
// JSON-RPC stream (stdout purity, design §6 "mcp" row).
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/mcp"
)

// Exit codes — the contract every subcommand returns through Main.
// Values are frozen by the Python baseline; changing one is a breaking
// change to every harness script that branches on them.
const (
	ExitOK       = 0 // success
	ExitError    = 1 // generic failure
	ExitGuard    = 2 // invalid input: guard violation or usage error
	ExitConflict = 3 // conflict: dedup block / supersede misuse
)

// usage is the top-level usage line; mirrors Python mem's argparse banner.
const usage = "usage: mem [-h] [--home HOME] {context,search,list,show,add,touch,cite,review,supersede,verify,reindex,render,sync,import,hook,stats,eval,mcp} ..."

// banner is the one-line description printed under usage.
const banner = "portable agent memory (markdown + git)"

// command is one subcommand registry entry. help mirrors the Python
// add_parser help string (Thai, as in the baseline).
type command struct {
	name string
	help string
	// mcp marks the one live subcommand: it hands the process to the MCP
	// stdio server instead of printing stub help. Every other command is
	// a stub until P2.2 wires it to internal/domain.
	mcp bool
}

// Env carries process-level context into a subcommand. It exists so Main
// stays testable against io.Writer pipes instead of the real stdio.
type Env struct {
	// Home is the --home override ("" = auto-detect). P2.2 resolves it
	// through store.FindRoot / MEM_HOME.
	Home string
	// Stdout and Stderr are the command's output streams.
	Stdout io.Writer
	Stderr io.Writer
}

// commands is the 18-subcommand registry, in Python mem's order.
var commands = []command{
	{name: "context", help: "ดึงความจำที่เกี่ยวข้องกับงาน"},
	{name: "search", help: "ค้น record"},
	{name: "list", help: "ไล่รายการ record"},
	{name: "show", help: "เปิด record เต็ม"},
	{name: "add", help: "เพิ่ม record ใหม่"},
	{name: "touch", help: "อัปเดต last_used"},
	{name: "cite", help: "นับว่า record ถูกใช้จริง (uses +1)"},
	{name: "review", help: "ปลด quarantine: sensitivity -> internal (ต้องมี --evidence)"},
	{name: "supersede", help: "แทนที่ record เก่าด้วยอันใหม่"},
	{name: "verify", help: "ตรวจ schema/secret/path/dup/index"},
	{name: "reindex", help: "สร้าง index ใหม่ (derived)"},
	{name: "render", help: "อัปเดต pointer block ในไฟล์ของแต่ละ agent"},
	{name: "sync", help: "commit + pull --rebase + push"},
	{name: "import", help: "ดึงความจำจากระบบอื่น (เข้า repo นี้)"},
	{name: "hook", help: "entry point เดียวที่ harness เรียกเป็น hook"},
	{name: "stats", help: "สถิติ + ความครอบคลุมของแต่ละ harness"},
	{name: "eval", help: "วัด recall@5/MRR/noise เทียบ eval/baseline.json (eval loop)"},
	{name: "mcp", help: "รันเป็น MCP server บน stdio (harness เรียกเป็น tool)", mcp: true},
}

// CommandNames returns the registered subcommand names in registry order.
// Exported for tests and for future shell-completion support.
func CommandNames() []string {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.name
	}
	return names
}

// Main routes argv (without the program name) and returns the process exit
// code. It never calls os.Exit — cmd/mem owns that.
func Main(argv []string, stdout, stderr io.Writer) int {
	env := &Env{Stdout: stdout, Stderr: stderr}

	// Global flags before the subcommand: --home PATH, --home=PATH, -h/--help.
	// Once the subcommand name is seen, the rest of argv belongs to it.
	// Classic loop on purpose: the --home arm advances i to consume the
	// value, which a range-over-int loop variable cannot do.
	var rest []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if len(rest) > 0 {
			rest = append(rest, a)
			continue
		}
		switch {
		case a == "-h" || a == "--help":
			printHelp(env)
			return ExitOK
		case a == "--home":
			if i+1 >= len(argv) {
				fmt.Fprintln(env.Stderr, "mem: --home requires a value")
				return ExitGuard
			}
			env.Home = argv[i+1]
			i++ // consume the value
		case strings.HasPrefix(a, "--home="):
			env.Home = strings.TrimPrefix(a, "--home=")
		default:
			rest = append(rest, a)
		}
	}

	if len(rest) == 0 {
		printHelp(env)
		return ExitOK
	}

	for _, c := range commands {
		if c.name != rest[0] {
			continue
		}
		if c.mcp {
			// The MCP server owns os.Stdin/os.Stdout as a JSON-RPC stream —
			// which is why cmd/mem also short-circuits `mem mcp` before any
			// CLI output could touch stdout.
			if err := mcp.RunStdio(context.Background()); err != nil {
				fmt.Fprintf(env.Stderr, "mem: mcp server: %v\n", err)
				return ExitError
			}
			return ExitOK
		}
		printCommandHelp(env, c)
		return ExitOK
	}

	fmt.Fprintf(env.Stderr, "mem: unknown command %q (try --help)\n", rest[0])
	return ExitGuard
}

// printHelp writes the root help (usage + banner + command table) to stdout.
func printHelp(env *Env) {
	fmt.Fprintln(env.Stdout, usage)
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, banner)
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, "commands:")
	w := 0
	for _, c := range commands {
		if len(c.name) > w {
			w = len(c.name)
		}
	}
	for _, c := range commands {
		fmt.Fprintf(env.Stdout, "  %-*s  %s\n", w, c.name, c.help)
	}
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, "options:")
	fmt.Fprintln(env.Stdout, "  -h, --help   show this help message and exit")
	fmt.Fprintln(env.Stdout, "  --home HOME  path ของ memory repo (default: auto-detect)")
}

// printCommandHelp writes one subcommand's help — the stub body's output
// until P2.2 replaces it with the real command implementation.
func printCommandHelp(env *Env, c command) {
	fmt.Fprintf(env.Stdout, "usage: mem %s\n\n%s\n\n(stub — wired to internal/domain in P2.2)\n", c.name, c.help)
}
