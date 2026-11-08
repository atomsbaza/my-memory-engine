// `mem import` — pull memories from another system into this home (Python
// cmd_import). Two sources are supported: `kirocrew` (its `memory list` output)
// and `dir` (a folder of markdown files). Both default to dry-run; --apply
// writes lessons only, never raw foreign text without provenance.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// kirocrewRow is one parsed kirocrew memory entry.
type kirocrewRow struct {
	id    string // source id ("" when the source has none)
	text  string
	conf  string
	src   string
	scope string
	title string
}

var (
	kirocrewHead     = regexp.MustCompile(`^\s*(lesson\.[0-9a-z_]+):\s*(.*)$`)
	kirocrewMeta     = regexp.MustCompile(`\(confidence=([\d.]+), source=([\w:.-]+)\)`)
	kirocrewMetaTail = regexp.MustCompile(`\s*\(confidence=[\d.]+, source=[\w:.-]+\)\s*$`)
)

// cmdImport reads the foreign source, reports what it found, and writes only
// with --apply.
func cmdImport(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	path := fs.String("path", "", "โฟลเดอร์ต้นทาง (สำหรับ source=dir)")
	sourceLabel := fs.String("source-label", "claude", "ชื่อระบบต้นทาง (ใช้ใน source/evidence)")
	scope := fs.String("scope", "global", "scope ของ record ที่ import")
	limit := fs.Int("limit", 0, "จำกัดจำนวนรายการ (0 = ไม่จำกัด)")
	var match stringsFlag
	fs.Var(&match, "match", "กรองด้วย regex (ซ้ำได้) ทั้งข้อความและ id")
	ids := fs.String("ids", "", "เลือกเฉพาะ id ต้นทาง คั่นด้วย ,")
	verbose := fs.Int("verbose", 15, "แสดงตัวอย่างกี่รายการ")
	apply := fs.Bool("apply", false, "เขียนจริง (default = dry-run)")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "<kirocrew|dir>"); stop {
		return code
	}
	source := args[0]

	var rows []kirocrewRow
	var err error
	switch source {
	case "kirocrew":
		rows, err = readKirocrewRows()
	case "dir":
		if *path == "" {
			return e.usagef("source=dir ต้องระบุ --path")
		}
		rows, err = readDirRows(*path, *sourceLabel, *scope)
	default:
		return e.usagef("source ที่รองรับ: kirocrew | dir")
	}
	if err != nil {
		return e.fail(err)
	}

	if *limit > 0 && len(rows) > *limit {
		rows = rows[:*limit]
	}
	if len(match) > 0 {
		pats := make([]*regexp.Regexp, 0, len(match))
		for _, m := range match {
			re, cerr := regexp.Compile("(?i)" + m)
			if cerr != nil {
				return e.usagef("--match %q: %v", m, cerr)
			}
			pats = append(pats, re)
		}
		kept := rows[:0]
		for _, r := range rows {
			for _, re := range pats {
				if re.MatchString(r.text) || (r.id != "" && re.MatchString(r.id)) {
					kept = append(kept, r)
					break
				}
			}
		}
		rows = kept
	}
	if *ids != "" {
		want := map[string]bool{}
		for _, s := range strings.Split(*ids, ",") {
			if s = strings.TrimSpace(s); s != "" {
				want[s] = true
			}
		}
		kept := rows[:0]
		for _, r := range rows {
			if want[r.id] {
				kept = append(kept, r)
			}
		}
		rows = kept
	}

	if *apply {
		fmt.Fprintf(e.Stdout, "พบ %d รายการ\n", len(rows))
	} else {
		fmt.Fprintf(e.Stdout, "พบ %d รายการ  (dry-run — ใส่ --apply เพื่อเขียนจริง)\n", len(rows))
	}
	shown := 0
	for _, r := range rows {
		if shown >= *verbose {
			break
		}
		shown++
		fmt.Fprintf(e.Stdout, "  %-22s %s\n", orDash(r.id), truncateRunes(firstLine(r.text), 110))
	}
	if !*apply {
		return ExitOK
	}

	root, rerr := e.root()
	if rerr != nil {
		return e.fail(rerr)
	}
	existing, serr := e.scan(root)
	if serr != nil {
		return e.fail(serr)
	}
	seen := map[string]bool{}
	for _, r := range existing {
		seen[r.ID] = true
	}

	added := 0
	for _, r := range rows {
		text := strings.TrimSpace(r.text)
		if text == "" {
			continue
		}
		body := importBody(text, r.src, r.id)
		title := strings.TrimSpace(r.title)
		if title == "" {
			title = strings.TrimSpace(strings.SplitN(text, " — NOT: ", 2)[0])
		}
		title = truncateRunes(title, 80)
		recID := codec.MakeID("lesson", r.scope, title, body)
		if seen[recID] {
			continue
		}
		if gerr := store.Guard(body); gerr != nil {
			// Never write a foreign record that trips the guard: the caller has
			// to redact first (Python prints the same skip).
			fmt.Fprintf(e.Stdout, "  skip %s (secret/path/pii — ต้อง redact ก่อน import: %v)\n", orDash(r.id), gerr)
			continue
		}
		created := today()
		fm := codec.Frontmatter{
			"id":          recID,
			"title":       title,
			"layer":       "lesson",
			"scope":       r.scope,
			"tags":        []string{},
			"confidence":  r.conf,
			"source":      r.src,
			"evidence":    []string{"import:" + r.src + ":" + orDash(r.id)},
			"created":     created,
			"last_used":   created,
			"expires":     "none",
			"status":      "active",
			"sensitivity": "internal",
		}
		rel := "lessons/" + codec.Filename("lesson", created, title)
		target := filepath.Join(root, filepath.FromSlash(rel))
		if _, statErr := os.Stat(target); statErr == nil {
			rel = "lessons/" + codec.FilenameN("lesson", created, title, 2)
			target = filepath.Join(root, filepath.FromSlash(rel))
		}
		if merr := os.MkdirAll(filepath.Dir(target), 0o755); merr != nil {
			return e.fail(merr)
		}
		if werr := store.AtomicWrite(target, []byte(codec.DumpFront(fm)+"\n"+body)); werr != nil {
			return e.fail(werr)
		}
		seen[recID] = true
		added++
	}
	if err := domain.ReindexRoot(root); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.Stdout, "เพิ่ม %d records\n", added)
	return ExitOK
}

// importBody builds the lesson body with the Rule/NOT/Evidence shape the
// taxonomy expects (Python cmd_import).
func importBody(text, src, sourceID string) string {
	if rule, notPart, ok := strings.Cut(text, " — NOT: "); ok {
		return fmt.Sprintf("**Rule.** %s\n\n**NOT.** %s\n\n**Evidence.** import จาก %s (%s)\n",
			strings.TrimSpace(rule), strings.TrimSpace(notPart), src, orDash(sourceID))
	}
	return fmt.Sprintf("**Rule.** %s\n\n**Evidence.** import จาก %s (%s)\n", text, src, orDash(sourceID))
}

// readKirocrewRows parses `kirocrew memory list` output.
func readKirocrewRows() ([]kirocrewRow, error) {
	exe, err := exec.LookPath("kirocrew")
	if err != nil {
		return nil, fmt.Errorf("ไม่พบคำสั่ง kirocrew")
	}
	out, err := exec.Command(exe, "memory", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("kirocrew memory list: %w", err)
	}
	var rows []kirocrewRow
	var cur *kirocrewRow
	flush := func() {
		if cur != nil {
			cur.text = strings.TrimSpace(cur.text)
			rows = append(rows, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if m := kirocrewHead.FindStringSubmatch(line); m != nil {
			flush()
			cur = &kirocrewRow{id: m[1], text: m[2], conf: "0.9", src: "import", scope: "global"}
			continue
		}
		if cur == nil {
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(line, "(") {
			cur.text += "\n" + trimmed
		}
		if m := kirocrewMeta.FindStringSubmatch(line); m != nil {
			cur.conf = m[1]
			cur.src = m[2]
			cur.text = strings.TrimSpace(kirocrewMetaTail.ReplaceAllString(cur.text, ""))
		}
	}
	flush()
	return rows, nil
}

// readDirRows reads every non-MEMORY/_ markdown file in dir.
func readDirRows(dir, label, scope string) ([]kirocrewRow, error) {
	base, err := store.ExpandPath(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("ไม่ใช่โฟลเดอร์: %s", base)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if strings.HasPrefix(entry.Name(), "MEMORY") || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	rows := make([]kirocrewRow, 0, len(names))
	for _, name := range names {
		data, rerr := os.ReadFile(filepath.Join(base, name))
		if rerr != nil {
			return nil, rerr
		}
		fm, body, _ := codec.ParseFront(string(data))
		title := stringField(fm, "title")
		if title == "" {
			title = truncateRunes(strings.ReplaceAll(strings.TrimSuffix(name, ".md"), "-", " "), 80)
		}
		text := strings.TrimSpace(body)
		if text == "" {
			text = strings.TrimSpace(string(data))
		}
		rows = append(rows, kirocrewRow{
			text:  text,
			conf:  "0.6",
			src:   "import:" + label,
			scope: scope,
			title: title,
		})
	}
	return rows, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
