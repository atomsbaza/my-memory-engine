// Engine-only subcommands: init (scaffold a fresh home without touching an
// existing one), doctor (file ↔ index ↔ remote consistency) and reindex.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/embed"
	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/sync"
)

// taxonomyDoc is the taxonomy.json shape a scaffolded home gets. It is built
// from schema.Defaults(), so a fresh home and a schema-less home can never
// disagree about layers, budgets or scoring (AC-P2-07).
type taxonomyDoc struct {
	Version                int                        `json:"version"`
	Layers                 map[string]schema.LayerDef `json:"layers"`
	BudgetsChars           map[string]int             `json:"budgets_chars"`
	Scoring                schema.Scoring             `json:"scoring"`
	Tags                   []string                   `json:"tags"`
	SecretPatterns         []string                   `json:"secret_patterns"`
	ForbiddenPathFragments []string                   `json:"forbidden_path_fragments"`
}

// cmdInit scaffolds a usable memory home. It is additive only: files and
// directories that already exist are left byte- and mtime-identical, so
// running it against a live home is a no-op (AC-P2-07).
func cmdInit(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	root, err := e.initTarget(arg)
	if err != nil {
		return e.fail(err)
	}
	// An existing memory home is never touched, not even by creating a missing
	// directory: init is an additive scaffold for new homes, not a repair tool
	// (AC-P2-07 invariant).
	if _, serr := os.Stat(filepath.Join(root, "schema", "taxonomy.json")); serr == nil {
		fmt.Fprintf(e.Stdout, "%s\nhome นี้มีอยู่แล้ว — ไม่แตะ\n", root)
		return ExitOK
	}

	sch := schema.Defaults()
	dirs := []string{"schema", "eval", "_archive"}
	for _, def := range sch.Layers {
		dirs = append(dirs, def.Dir)
	}
	sort.Strings(dirs)

	var created, kept []string
	for _, dir := range dirs {
		path := filepath.Join(root, filepath.FromSlash(dir))
		if info, serr := os.Stat(path); serr == nil && info.IsDir() {
			kept = append(kept, dir+"/")
			continue
		}
		if merr := os.MkdirAll(path, 0o755); merr != nil {
			return e.fail(fmt.Errorf("สร้าง %s/: %w", dir, merr))
		}
		created = append(created, dir+"/")
	}

	taxonomy, err := json.MarshalIndent(taxonomyDoc{
		Version:                sch.TaxonomyVersion,
		Layers:                 sch.Layers,
		BudgetsChars:           sch.Budgets,
		Scoring:                sch.Scoring,
		Tags:                   sch.Tags,
		SecretPatterns:         sch.SecretPatterns,
		ForbiddenPathFragments: sch.ForbiddenPaths,
	}, "", "  ")
	if err != nil {
		return e.fail(err)
	}
	agents, err := json.MarshalIndent(map[string]any{
		"version": 2,
		"note":    "Adapter registry. เพิ่ม agent ใหม่ = เพิ่ม entry ที่นี่ (mode: render | pointer | tool | pull)",
		"agents":  []any{},
	}, "", "  ")
	if err != nil {
		return e.fail(err)
	}
	files := []struct {
		rel  string
		data []byte
	}{
		{"schema/taxonomy.json", append(taxonomy, '\n')},
		{"schema/agents.json", append(agents, '\n')},
		{".gitignore", []byte(".memory/\n*.sqlite\n")},
	}
	for _, f := range files {
		path := filepath.Join(root, filepath.FromSlash(f.rel))
		if _, serr := os.Stat(path); serr == nil {
			kept = append(kept, f.rel)
			continue
		}
		if werr := store.AtomicWrite(path, f.data); werr != nil {
			return e.fail(fmt.Errorf("เขียน %s: %w", f.rel, werr))
		}
		created = append(created, f.rel)
	}

	// Local-only git repo: `mem sync`/the stop hook need one, and `git init`
	// touches no network and configures no remote.
	notes := []string{}
	if _, serr := os.Stat(filepath.Join(root, ".git")); serr != nil {
		if out, gerr := runGitInit(root); gerr != nil {
			notes = append(notes, "git init ไม่สำเร็จ: "+string(out))
		} else {
			created = append(created, ".git/")
		}
	} else {
		kept = append(kept, ".git/")
	}

	fmt.Fprintf(e.Stdout, "%s\n", root)
	if len(created) > 0 {
		fmt.Fprintf(e.Stdout, "สร้าง: %s\n", strings.Join(created, ", "))
	}
	if len(kept) > 0 {
		fmt.Fprintf(e.Stdout, "มีอยู่แล้ว (ไม่แตะ): %s\n", strings.Join(kept, ", "))
	}
	for _, n := range notes {
		fmt.Fprintf(e.Stderr, "mem: %s\n", n)
	}
	fmt.Fprintf(e.Stdout, "ต่อ: รัน `mem --home %s verify` เพื่อยืนยันว่า home ใช้งานได้\n", root)
	return ExitOK
}

// initTarget resolves where init writes: an explicit positional path, then
// --home, then the documented default ~/my-memory. $MEM_HOME is honoured only
// when neither was given, so a shell-wide override cannot silently redirect an
// explicit target.
func (e *Env) initTarget(arg string) (string, error) {
	switch {
	case arg != "":
		return store.ExpandPath(arg)
	case e.Home != "":
		return store.ExpandPath(e.Home)
	}
	for _, key := range []string{"MEM_HOME", "MY_MEMORY_HOME"} {
		if v := os.Getenv(key); v != "" {
			return store.ExpandPath(v)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("หา home directory ไม่ได้: %w", err)
	}
	return store.ExpandPath(filepath.Join(home, "my-memory"))
}

func runGitInit(root string) (string, error) {
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cmdReindex rebuilds the derived index (Python cmd_reindex) — mode-aware:
// with embeddings off it provably drops the emb table; with a provider
// configured it re-embeds every record chunk and prints the mode it used.
func cmdReindex(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	sch, err := e.schemaOf(root)
	if err != nil {
		return e.fail(err)
	}
	cfg := sch.Embedding.WithDefaults()
	if err := domain.ReindexRoot(root); err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}
	ix, err := index.Open(index.IndexPath(root))
	if err != nil {
		return e.fail(err)
	}
	defer ix.Close()
	if !ix.FTS5() {
		// Degraded mode must be reported, never presented as ranked search.
		fmt.Fprintf(e.Stderr, "mem: FTS5 ไม่พร้อม — index ใช้ substring fallback (AC-P1-09)\n")
	}
	provider := embed.NewFromConfig(cfg)
	switch p := provider.(type) {
	case nil:
		// Off mode: the index is provably emb-free.
		if err := ix.DropEmb(); err != nil {
			return e.fail(err)
		}
		fmt.Fprintf(e.Stdout, "index: %d records · embedding: off (ไม่มี emb table)\n", len(recs))
	case *embed.Ollama:
		started := time.Now()
		rows, berr := buildEmbRows(p, recs)
		if berr != nil {
			return e.fail(berr)
		}
		if len(rows) == 0 {
			if err := ix.DropEmb(); err != nil {
				return e.fail(err)
			}
			fmt.Fprintf(e.Stdout, "index: %d records · embedding: %s (ไม่มี chunk ให้ embed)\n", len(recs), cfg.Provider)
			return ExitOK
		}
		dim := len(rows[0].Vec) / 4
		if err := ix.SetEmb(rows, p.Model(), dim); err != nil {
			return e.fail(err)
		}
		fmt.Fprintf(e.Stdout, "index: %d records · embedding: %s/%s · %d chunks (%dd, %.1fs)\n",
			len(recs), p.Name(), p.Model(), len(rows), dim, time.Since(started).Seconds())
	default:
		// Unknown provider: loud at first use — reindex must not guess.
		if _, uerr := p.Embed(context.Background(), []string{"ping"}); uerr != nil {
			return e.fail(uerr)
		}
		return e.fail(fmt.Errorf("reindex: provider %T ไม่รองรับ", p))
	}
	return ExitOK
}

// doctorReport accumulates one consistency check per home surface.
type doctorReport struct {
	lines    []string
	problems []string
}

func (r *doctorReport) check(name string, ok bool, detail string) {
	status := "ok"
	if !ok {
		status = "FAIL"
		r.problems = append(r.problems, name+": "+detail)
	}
	r.lines = append(r.lines, fmt.Sprintf("  %-9s %-10s %s", name, status, detail))
}

// cmdDoctor reports file ↔ index ↔ remote consistency: exit 0 when the home is
// internally consistent, exit 1 when it is not (a failed check, REQ-CLI-02).
func cmdDoctor(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		// The home itself is the first thing doctor checks.
		fmt.Fprintf(e.Stderr, "mem: %v\n", err)
		return ExitError
	}

	rep := &doctorReport{}
	fmt.Fprintf(e.Stdout, "home: %s\n", root)

	sch, serr := e.schemaOf(root)
	rep.check("schema", serr == nil, describeErr(fmt.Sprintf("%d layers, %d budgets", len(sch.Layers), len(sch.Budgets)), serr))

	scanned, ferr := store.ScanWithReport(root)
	okFiles := ferr == nil && len(scanned.Broken) == 0
	detail := "0 records"
	if ferr == nil {
		detail = fmt.Sprintf("%d records", len(scanned.Records))
		if len(scanned.Broken) > 0 {
			names := make([]string, 0, len(scanned.Broken))
			for _, b := range scanned.Broken {
				names = append(names, b.Rel)
			}
			detail += " · front-matter เสีย: " + strings.Join(names, ", ")
		}
	}
	rep.check("files", okFiles, describeErr(detail, ferr))

	missing := missingLayerDirs(root, sch)
	rep.check("layers", len(missing) == 0, describeErr(missingHint(missing), nil))

	// Index: existence, count parity, schema generation. A home with no records
	// needs no index (nothing derived yet), so its absence is only a problem
	// once there is something to index.
	idxPath := index.IndexPath(root)
	var idxDetail string
	idxOK := true
	if _, statErr := os.Stat(idxPath); statErr != nil {
		if len(scanned.Records) == 0 {
			idxDetail = "ไม่มี index (home ว่าง ไม่มีอะไรให้ index)"
		} else {
			idxOK = false
			idxDetail = "ไม่มี index — รัน `mem reindex`"
		}
	} else if ix, oerr := index.Open(idxPath); oerr != nil {
		idxOK = false
		idxDetail = oerr.Error()
	} else {
		count, cerr := ix.Count()
		stale, verr := ix.Stale()
		version, _, _ := ix.Version()
		_ = ix.Close()
		switch {
		case cerr != nil:
			idxOK = false
			idxDetail = "อ่าน index ไม่ได้: " + cerr.Error()
		case verr != nil:
			idxOK = false
			idxDetail = "อ่าน schema_version ไม่ได้: " + verr.Error()
		case stale:
			idxOK = false
			idxDetail = fmt.Sprintf("index generation เก่า (schema_version=%d) — รัน `mem reindex`", version)
		case count != len(scanned.Records):
			idxOK = false
			idxDetail = fmt.Sprintf("index มี %d แต่พบ %d records — รัน `mem reindex`", count, len(scanned.Records))
		default:
			idxDetail = fmt.Sprintf("%d records · schema_version=%d", count, version)
		}
	}
	rep.check("index", idxOK, idxDetail)

	// Remote: readiness only — `git remote`/`rev-parse` read config, no network.
	o := sync.Options{}
	repo, rerr := sync.IsRepo(root, o)
	var remoteDetail string
	remoteOK := true
	switch {
	case rerr != nil:
		remoteOK = false
		remoteDetail = rerr.Error()
	case !repo:
		remoteOK = false
		remoteDetail = "ไม่ใช่ git repo — `git init` ก่อน (sync ต้องใช้)"
	default:
		remotes, gerr := sync.Remotes(root, o)
		upstream, hasUp, uerr := sync.Upstream(root, o)
		switch {
		case gerr != nil:
			remoteOK = false
			remoteDetail = gerr.Error()
		case uerr != nil:
			remoteOK = false
			remoteDetail = uerr.Error()
		case len(remotes) == 0:
			remoteDetail = "ไม่มี remote (local only) — sync จะไม่แตะ network"
		case !hasUp:
			remoteOK = false
			remoteDetail = fmt.Sprintf("remote %s แต่ HEAD ไม่มี upstream — `git branch -u <remote>/<branch>`", strings.Join(remotes, ","))
		case !hasRemotePrefix(remotes, upstream):
			remoteOK = false
			remoteDetail = fmt.Sprintf("upstream %s ไม่ตรงกับ remote ที่มี (%s)", upstream, strings.Join(remotes, ","))
		default:
			remoteDetail = "remote " + strings.Join(remotes, ",") + " · upstream " + upstream
		}
	}
	rep.check("remote", remoteOK, remoteDetail)

	for _, line := range rep.lines {
		fmt.Fprintln(e.Stdout, line)
	}
	fmt.Fprintf(e.Stdout, "\n%d checks · %d problems\n", len(rep.lines), len(rep.problems))
	if len(rep.problems) > 0 {
		for _, p := range rep.problems {
			fmt.Fprintf(e.Stdout, "FAIL: %s\n", p)
		}
		return ExitError
	}
	return ExitOK
}

func describeErr(detail string, err error) string {
	if err != nil {
		return err.Error()
	}
	return detail
}

func missingHint(missing []string) string {
	if len(missing) == 0 {
		return "ครบทุก layer dir"
	}
	return "ขาดโฟลเดอร์ " + strings.Join(missing, ", ")
}

func missingLayerDirs(root string, sch schema.Schema) []string {
	var missing []string
	dirs := make(map[string]bool, len(sch.Layers))
	for _, def := range sch.Layers {
		dirs[def.Dir] = true
	}
	names := make([]string, 0, len(dirs))
	for dir := range dirs {
		names = append(names, dir)
	}
	sort.Strings(names)
	for _, dir := range names {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); err != nil || !info.IsDir() {
			missing = append(missing, dir+"/")
		}
	}
	return missing
}

func hasRemotePrefix(remotes []string, upstream string) bool {
	for _, r := range remotes {
		if strings.HasPrefix(upstream, r+"/") {
			return true
		}
	}
	return false
}
