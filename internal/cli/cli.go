// Package cli implements the mem command surface (design §6 "cli" row):
// the 18 subcommands of the Python baseline plus `init` and `doctor`, their
// flags and their exit-code contract.
//
// Exit-code contract (Python baseline mem; design §7 write path):
//
//	0  success
//	1  generic failure (repo not found, record not found, failed check, eval
//	   regression) — and every git failure that is not a conflict
//	2  invalid input — guard violations (secret / absolute-path / PII scan)
//	   and, in argparse parity, usage errors such as an unknown flag or
//	   command
//	3  conflict — dedup block (jaccard >= 0.88), supersede misuse, or a
//	   sync rebase conflict
//
// The binary never prints to stdout from the mcp path: `mem mcp` hands the
// process to the MCP stdio server before any CLI output could pollute the
// JSON-RPC stream (stdout purity, design §6 "mcp" row).
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/mcp"
)

// Exit codes — the contract every subcommand returns through Main.
// Values are frozen by the Python baseline; changing one is a breaking
// change to every harness script that branches on them.
const (
	ExitOK       = 0 // success
	ExitError    = 1 // generic failure / failed check
	ExitGuard    = 2 // invalid input: guard violation or usage error
	ExitConflict = 3 // conflict: dedup block / supersede misuse / sync conflict
)

// Command counts. The manifest is generated from this file; docs/cli-commands.md
// is checked against it so a new command cannot ship undocumented
// (NFR-COMPAT-01, AC-P2-07).
const (
	// LegacyCommandCount is the Python baseline's add_parser count.
	LegacyCommandCount = 18
	// CommandCount is the full surface: 18 legacy families + init + doctor.
	CommandCount = LegacyCommandCount + 2
)

// usage is the top-level usage line; mirrors Python mem's argparse banner with
// the two engine-only commands appended.
const usage = "usage: mem [-h] [--home HOME] {context,search,list,show,add,touch,cite,review,supersede,verify,reindex,render,sync,import,hook,stats,eval,mcp,init,doctor} ..."

// banner is the one-line description printed under usage.
const banner = "portable agent memory (markdown + git)"

// Entry is one command-surface record. It is the single source of truth for
// the help table, the generated docs and the parity manifest.
type Entry struct {
	Name   string
	Args   string // argument synopsis without the leading command name
	Help   string
	Legacy bool // part of the frozen 18-command Python surface
}

// command is Entry plus the handler the router calls.
type command struct {
	Entry
	run func(*Env, command, []string) int
}

// Env carries process-level context into a subcommand. It exists so Main
// stays testable against io.Writer pipes and a scripted stdin instead of the
// real stdio (the hook command reads a JSON payload from stdin).
type Env struct {
	// Home is the --home override ("" = auto-detect via store.FindRoot).
	Home string
	// Stdin, Stdout and Stderr are the command's streams.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// commands is the 20-command registry: the 18 legacy families in Python mem's
// order, then the two engine-only commands.
var commands = []command{
	{Entry: Entry{Name: "context", Args: "[--project P] [--scope S] [--query Q] [--budget N] [--no-touch] [--quiet]", Help: "ดึงความจำที่เกี่ยวข้องกับงาน", Legacy: true}, run: cmdContext},
	{Entry: Entry{Name: "search", Args: "<query> [--scope S] [--layer L]... [--limit N] [--all] [--scores]", Help: "ค้น record", Legacy: true}, run: cmdSearch},
	{Entry: Entry{Name: "list", Args: "[--layer L] [--scope S] [--limit N] [--all] [--status ST]", Help: "ไล่รายการ record", Legacy: true}, run: cmdList},
	{Entry: Entry{Name: "show", Args: "[<id>] [--path P]", Help: "เปิด record เต็ม", Legacy: true}, run: cmdShow},
	{Entry: Entry{Name: "add", Args: "--layer L --scope S --file F [flags]", Help: "เพิ่ม record ใหม่", Legacy: true}, run: cmdAdd},
	{Entry: Entry{Name: "touch", Args: "<id>", Help: "อัปเดต last_used", Legacy: true}, run: cmdTouch},
	{Entry: Entry{Name: "cite", Args: "<id>", Help: "นับว่า record ถูกใช้จริง (uses +1)", Legacy: true}, run: cmdCite},
	{Entry: Entry{Name: "review", Args: "<id> --evidence E", Help: "ปลด quarantine: sensitivity -> internal (ต้องมี --evidence)", Legacy: true}, run: cmdReview},
	{Entry: Entry{Name: "supersede", Args: "<old> --with <new> [--no-commit]", Help: "แทนที่ record เก่าด้วยอันใหม่", Legacy: true}, run: cmdSupersede},
	{Entry: Entry{Name: "verify", Args: "", Help: "ตรวจ schema/secret/path/dup/index", Legacy: true}, run: cmdVerify},
	{Entry: Entry{Name: "reindex", Args: "", Help: "สร้าง index ใหม่ (derived)", Legacy: true}, run: cmdReindex},
	{Entry: Entry{Name: "render", Args: "[--agent A]... [--dry-run] [--print]", Help: "อัปเดต pointer block ในไฟล์ของแต่ละ agent", Legacy: true}, run: cmdRender},
	{Entry: Entry{Name: "sync", Args: "[-m MSG] [--no-push]", Help: "commit + pull --rebase + push", Legacy: true}, run: cmdSync},
	{Entry: Entry{Name: "import", Args: "<kirocrew|dir> [--path P] [--apply]", Help: "ดึงความจำจากระบบอื่น (เข้า repo นี้)", Legacy: true}, run: cmdImport},
	{Entry: Entry{Name: "hook", Args: "--event E [--agent A] [--query Q] [--format F]", Help: "entry point เดียวที่ harness เรียกเป็น hook", Legacy: true}, run: cmdHook},
	{Entry: Entry{Name: "stats", Args: "[--json]", Help: "สถิติ + ความครอบคลุมของแต่ละ harness", Legacy: true}, run: cmdStats},
	{Entry: Entry{Name: "eval", Args: "[--days N] [--json] [--save-baseline] [--from-log] [--weights W]", Help: "วัด recall@5/MRR/noise เทียบ eval/baseline.json (eval loop)", Legacy: true}, run: cmdEval},
	{Entry: Entry{Name: "mcp", Args: "[--agent A]", Help: "รันเป็น MCP server บน stdio (harness เรียกเป็น tool)", Legacy: true}, run: cmdMCP},
	{Entry: Entry{Name: "init", Args: "[PATH]", Help: "สร้าง memory home ใหม่ (ไม่แตะ home เดิม)"}, run: cmdInit},
	{Entry: Entry{Name: "doctor", Args: "", Help: "ตรวจความสอดคล้อง file ↔ index ↔ remote"}, run: cmdDoctor},
}

// Manifest returns the command surface in registry order. Exported for the
// help table, the generated docs and the parity/gate tests.
func Manifest() []Entry {
	out := make([]Entry, len(commands))
	for i, c := range commands {
		out[i] = c.Entry
	}
	return out
}

// CommandNames returns the registered subcommand names in registry order.
func CommandNames() []string {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.Name
	}
	return names
}

// Main routes argv (without the program name) and returns the process exit
// code. It never calls os.Exit — cmd/mem owns that.
func Main(argv []string, stdout, stderr io.Writer) int {
	return MainWithIO(argv, os.Stdin, stdout, stderr)
}

// MainWithIO is Main with an explicit stdin (the hook command reads the
// harness payload from it; tests inject a scripted reader).
func MainWithIO(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	env := &Env{Stdin: stdin, Stdout: stdout, Stderr: stderr}

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
		if c.Name == rest[0] {
			return c.run(env, c, rest[1:])
		}
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
		if len(c.Name) > w {
			w = len(c.Name)
		}
	}
	for _, c := range commands {
		fmt.Fprintf(env.Stdout, "  %-*s  %s\n", w, c.Name, c.Help)
	}
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, "options:")
	fmt.Fprintln(env.Stdout, "  -h, --help   show this help message and exit")
	fmt.Fprintln(env.Stdout, "  --home HOME  path ของ memory repo (default: auto-detect)")
}

// printCommandHelp writes one subcommand's usage to w.
func printCommandHelp(env *Env, c command, w io.Writer) {
	synopsis := "usage: mem " + c.Name
	if c.Args != "" {
		synopsis += " " + c.Args
	}
	fmt.Fprintln(w, synopsis)
	fmt.Fprintln(w)
	fmt.Fprintln(w, c.Help)
}

// cmdMCP hands the process to the MCP stdio server.
func cmdMCP(env *Env, c command, argv []string) int {
	// --agent names the harness this server speaks for; MEM_AGENT carries the
	// same value for the tool handlers (Python mcp parity).
	fs := env.flagSet(c)
	agent := fs.String("agent", "", "ชื่อ harness — เป็น source default ของ record ที่เขียนผ่าน MCP")
	if _, code, stop := env.parseArgs(c, fs, argv); stop {
		return code
	}
	if *agent != "" {
		os.Setenv("MEM_AGENT", *agent)
	}
	if err := mcp.RunStdio(context.Background()); err != nil {
		fmt.Fprintf(env.Stderr, "mem: mcp server: %v\n", err)
		return ExitError
	}
	return ExitOK
}

// fail prints err in the Python `die()` shape and returns its exit code:
// domain refusals keep their own code, everything else is a generic failure.
func (e *Env) fail(err error) int {
	if err == nil {
		return ExitOK
	}
	fmt.Fprintf(e.Stderr, "mem: %s\n", err)
	return exitCode(err)
}
