package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/ext"
)

func cmdExt(e *Env, c command, argv []string) int {
	if len(argv) == 0 {
		printCommandHelp(e, c, e.Stderr)
		return ExitGuard
	}
	switch argv[0] {
	case "add":
		return e.extAdd(argv[1:])
	case "list":
		return e.extList(argv[1:])
	case "rm":
		return e.extRm(argv[1:])
	case "reindex":
		return e.extReindex(argv[1:])
	case "search":
		return e.extSearch(argv[1:])
	default:
		fmt.Fprintf(e.Stderr, "mem ext: ไม่รู้จัก %q (ใช้ add | list | rm | reindex | search)\n", argv[0])
		printCommandHelp(e, c, e.Stderr)
		return ExitGuard
	}
}

func (e *Env) extAdd(argv []string) int {
	if len(argv) != 2 {
		fmt.Fprintln(e.Stderr, "usage: mem ext add <name> <path>")
		return ExitGuard
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	if err := ext.AddVault(root, argv[0], argv[1]); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.Stdout, "ext: added %s (%s)\n", argv[0], argv[1])
	return ExitOK
}

func (e *Env) extList(argv []string) int {
	asJSON := false
	for _, a := range argv {
		if a == "--json" {
			asJSON = true
		} else {
			fmt.Fprintf(e.Stderr, "mem ext list: unknown arg %q\n", a)
			return ExitGuard
		}
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	cfg, err := ext.LoadConfig(root)
	if err != nil {
		return e.fail(err)
	}
	if asJSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cfg)
		return ExitOK
	}
	if len(cfg.Vaults) == 0 {
		fmt.Fprintln(e.Stdout, "ext: (no external vaults configured)")
		return ExitOK
	}
	for _, v := range cfg.Vaults {
		fmt.Fprintf(e.Stdout, "%s\t%s\n", v.Name, v.Path)
	}
	return ExitOK
}

func (e *Env) extRm(argv []string) int {
	if len(argv) != 1 {
		fmt.Fprintln(e.Stderr, "usage: mem ext rm <name>")
		return ExitGuard
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	ok, err := ext.RemoveVault(root, argv[0])
	if err != nil {
		return e.fail(err)
	}
	if !ok {
		fmt.Fprintf(e.Stderr, "ext: unknown vault %q\n", argv[0])
		return ExitError
	}
	fmt.Fprintf(e.Stdout, "ext: removed %s\n", argv[0])
	return ExitOK
}

func (e *Env) extReindex(argv []string) int {
	only := ""
	full := false
	for _, a := range argv {
		switch {
		case a == "--full":
			full = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(e.Stderr, "usage: mem ext reindex [<name>] [--full]")
			return ExitGuard
		case only == "":
			only = a
		default:
			fmt.Fprintln(e.Stderr, "usage: mem ext reindex [<name>] [--full]")
			return ExitGuard
		}
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	res, err := ext.ReindexWithOptions(root, only, full)
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.Stdout, "ext index: %d vaults · %d files (%d skipped, %d updated, %d deleted) · %d chunks\n",
		res.Vaults, res.Files, res.Skipped, res.Updated, res.Deleted, res.Chunks)
	return ExitOK
}

func (e *Env) extSearch(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(e.Stderr, "usage: mem ext search <query> [--vault V] [--limit N] [--json]")
		return ExitGuard
	}
	var query, vault string
	limit := 10
	asJSON := false
	for i := 0; i < len(argv); i++ { // classic loop on purpose
		a := argv[i]
		switch a {
		case "--vault":
			if i+1 >= len(argv) {
				fmt.Fprintln(e.Stderr, "mem ext search: --vault requires a value")
				return ExitGuard
			}
			i++
			vault = argv[i]
		case "--limit":
			if i+1 >= len(argv) {
				fmt.Fprintln(e.Stderr, "mem ext search: --limit requires a value")
				return ExitGuard
			}
			i++
			n, err := strconv.Atoi(argv[i])
			if err != nil || n <= 0 {
				fmt.Fprintf(e.Stderr, "mem ext search: invalid --limit %q\n", argv[i])
				return ExitGuard
			}
			limit = n
		case "--json":
			asJSON = true
		default:
			if query == "" {
				query = a
			} else {
				query += " " + a
			}
		}
	}
	if query == "" {
		fmt.Fprintln(e.Stderr, "mem ext search: query cannot be empty")
		return ExitGuard
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	hits, err := ext.Search(root, query, vault, limit)
	if err != nil {
		return e.fail(err)
	}
	if asJSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(hits)
		return ExitOK
	}
	if len(hits) == 0 {
		fmt.Fprintln(e.Stdout, "(no external matches)")
		return ExitOK
	}
	for _, h := range hits {
		fmt.Fprintf(e.Stdout, "[%s] %s (%s)\n  %s\n", h.Vault, h.RelPath, h.Heading, h.Snippet)
	}
	return ExitOK
}
