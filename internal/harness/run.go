// CLI surface of the installer: flag parsing, the --yes gate and the exit
// codes. It lives next to the installer (not in internal/cli) because the
// 18-subcommand registry of `mem` is frozen by the Python baseline — the
// installer stays its own binary, mem-install, exactly like install.sh.
package harness

import (
	"flag"
	"fmt"
	"io"
)

// Exit codes, mirroring internal/cli's contract and install.sh: 0 success
// (including dry run), 1 environment failure, 2 usage error.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

const installUsage = `usage: mem-install [--home HOME] [--repo REPO] [--mem BIN] [--mcp] [--hooks] [--all] [--yes]

ติดตั้ง "ทาง" ให้แต่ละ harness อ่าน/เขียน memory นี้
  --mcp     ลงทะเบียน MCP server my-memory กับ CLI ที่ติดตั้งในเครื่อง (idempotent)
  --hooks   ติดตั้ง hook ในไฟล์ config ของ harness (claude/codex/gemini/cursor)
            + plugin ของ opencode/omp (symlink) — hermes แก้ YAML ด้วยมือ (พิมพ์คำสั่งให้)
  --all     = --mcp + --hooks
  --yes     ยืนยันว่าให้แก้ไฟล์จริง (จำเป็นเสมอ — ไม่มี prompt แบบ TTY)
  (ไม่ใส่ flag = dry-run: พิมพ์ว่าจะทำอะไร ไม่แก้ไฟล์ ออกด้วย 0)

ทุกไฟล์ที่ถูกแก้จะถูกสำรองเป็น <file>.bak-<timestamp> ก่อน
`

// Run parses argv (without the program name), executes one installer run and
// returns the process exit code. It never calls os.Exit — cmd/mem-install
// owns that.
func Run(argv []string, stdout, stderr io.Writer) int {
	return RunWithOptions(Options{}, argv, stdout, stderr)
}

// RunWithOptions is Run with injectable Options (exec/lookpath/clock stubs
// for tests). CLI path flags (--home/--repo/--mem), when given, override the
// corresponding option fields.
func RunWithOptions(opt Options, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mem-install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, installUsage) }
	home := fs.String("home", "", "harness config root (default: user home dir)")
	repo := fs.String("repo", "", "engine repo root — symlink source of opencode plugin / omp extension")
	mem := fs.String("mem", "", "engine binary to register (default: mem next to this binary)")
	doMCP := fs.Bool("mcp", false, "register the my-memory MCP server")
	doHooks := fs.Bool("hooks", false, "install session hooks")
	all := fs.Bool("all", false, "--mcp + --hooks")
	yes := fs.Bool("yes", false, "confirm real config changes")
	if err := fs.Parse(argv); err != nil {
		if err == flag.ErrHelp {
			return ExitOK
		}
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "ไม่รู้จัก argument: %s\n\n%s", fs.Arg(0), installUsage)
		return ExitUsage
	}
	if *all {
		*doMCP, *doHooks = true, true
	}
	dry := !*doMCP && !*doHooks
	if !dry && !*yes {
		fmt.Fprintln(stderr, "ปฏิเสธ: โหมดแก้จริงต้องใส่ --yes (ตรวจก่อน: mem-install ไม่ใส่ flag = dry-run)")
		fmt.Fprintln(stderr, "  ตรวจก่อน:  mem-install            # dry-run")
		fmt.Fprintln(stderr, "  ทำจริง:    mem-install --all --yes")
		return ExitUsage
	}
	if *home != "" {
		opt.Home = *home
	}
	if *repo != "" {
		opt.Repo = *repo
	}
	if *mem != "" {
		opt.MemBin = *mem
	}
	opt.Out, opt.Err = stdout, stderr
	inst := New(opt) // fills exec/lookpath/clock defaults for any stub left nil
	if err := inst.validate(); err != nil {
		fmt.Fprintln(stderr, "mem-install: "+err.Error())
		return ExitError
	}
	var err error
	if dry {
		err = inst.DryRun()
	} else {
		err = inst.Apply(*doMCP, *doHooks)
	}
	if err != nil {
		fmt.Fprintln(stderr, "mem-install: "+err.Error())
		return ExitError
	}
	return ExitOK
}
