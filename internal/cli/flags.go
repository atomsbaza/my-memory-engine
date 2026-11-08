// Flag plumbing shared by every subcommand: stdlib flag with argparse-ish
// behaviour (usage errors → exit 2 on stderr, -h → command help on stdout,
// repeated flags accumulate) and the helpers that turn domain/sync errors into
// the frozen exit codes.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/sync"
)

// stringsFlag collects a repeatable flag (`--layer a --layer b`), matching
// argparse's action="append".
type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }

func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// flagSet builds a FlagSet whose usage goes to stderr: a usage error must
// never write to stdout (the --json purity contract, AC-P2-08).
func (e *Env) flagSet(c command) *flag.FlagSet {
	fs := flag.NewFlagSet("mem "+c.Name, flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	fs.Usage = func() { printCommandHelp(e, c, e.Stderr) }
	return fs
}

// parseArgs parses argv and returns the positional arguments, plus the exit
// code the caller must return when the parse itself decided the outcome.
//
// Flags and positionals may interleave (`mem review <id> --evidence X`), which
// the stdlib parser alone does not allow: it stops at the first positional.
// Re-parsing the remainder keeps every flag reachable without reordering argv
// by hand — argparse behaviour without a second flag implementation.
func (e *Env) parseArgs(c command, fs *flag.FlagSet, argv []string) (args []string, code int, stop bool) {
	for _, a := range argv {
		if a == "-h" || a == "--help" {
			// argparse prints help on stdout and exits 0 wherever -h sits.
			printCommandHelp(e, c, e.Stdout)
			return nil, ExitOK, true
		}
	}
	rest := argv
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, ExitOK, true
			}
			return nil, ExitGuard, true
		}
		remaining := fs.Args()
		if len(remaining) == 0 {
			return args, ExitOK, false
		}
		args = append(args, remaining[0])
		rest = remaining[1:]
		if len(rest) == 0 {
			return args, ExitOK, false
		}
	}
}

// usagef reports a flag-level usage error in the Python die() shape.
func (e *Env) usagef(format string, a ...any) int {
	fmt.Fprintf(e.Stderr, "mem: "+format+"\n", a...)
	return ExitGuard
}

// root resolves the memory home: --home first, then store.FindRoot's order.
func (e *Env) root() (string, error) {
	if e.Home != "" {
		return store.ExpandPath(e.Home)
	}
	return store.FindRoot()
}

// exitCode maps an error onto the frozen exit contract: domain refusals keep
// their code, sync conflicts are 3, everything else is a generic failure.
func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if code, ok := domain.AsError(err); ok {
		return code
	}
	var ce *sync.ConflictError
	if errors.As(err, &ce) {
		return ExitConflict
	}
	return ExitError
}

// needArgs reports a missing positional argument.
func (e *Env) needArgs(got, min int, synopsis string) (int, bool) {
	if got >= min {
		return ExitOK, false
	}
	fmt.Fprintf(e.Stderr, "mem: ต้องระบุ %s\n", synopsis)
	return ExitGuard, true
}

// stdinPayload reads all of stdin when it is not a terminal. The hook command
// uses it for the harness JSON payload; a TTY means "no payload" (never block).
func (e *Env) stdinPayload() string {
	if e.Stdin == nil {
		return ""
	}
	if f, ok := e.Stdin.(*os.File); ok {
		info, err := f.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return ""
		}
	}
	data, err := io.ReadAll(e.Stdin)
	if err != nil {
		return ""
	}
	return string(data)
}
