// Write-side subcommands: add, touch, cite, review, supersede. All guards,
// dedup bands, quarantine rules and commit-failure reporting live in
// internal/domain; these handlers only parse the Python flag surface and print
// its result contract.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// cmdAdd writes one record (Python cmd_add).
func cmdAdd(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	layer := fs.String("layer", "", "layer ของ record (เช่น lesson)")
	scope := fs.String("scope", "", "global | project:<Name> | repo:<owner/name> | agent:<name>")
	file := fs.String("file", "", "path ของเนื้อหา หรือ - สำหรับ stdin")
	title := fs.String("title", "", "title (default: บรรทัดแรกของเนื้อหา)")
	var tags stringsFlag
	fs.Var(&tags, "tag", "tag (ซ้ำได้)")
	confidence := fs.String("confidence", "0.9", "confidence 0..1")
	decay := fs.String("decay", "1.0", "decay 0..1")
	halfLife := fs.String("half-life", "", "half life (วัน)")
	source := fs.String("source", "agent:unknown", "source (agent:<name> | import:... | external:...)")
	session := fs.String("session", "", "session id")
	var evidence stringsFlag
	fs.Var(&evidence, "evidence", "หลักฐาน/ที่มา (ซ้ำได้)")
	supersedes := fs.String("supersedes", "", "id ของ record เดิมที่ถูกแทนที่")
	supersede := fs.Bool("supersede", false, "แทนที่ record ที่ซ้ำโดยอัตโนมัติ")
	id := fs.String("id", "", "id ที่ต้องการ (default: คำนวณจากเนื้อหา)")
	created := fs.String("created", "", "วันที่สร้าง (ISO)")
	expires := fs.String("expires", "none", "none | YYYY-MM-DD | +21d | +2w | +3m")
	sensitivity := fs.String("sensitivity", "internal", "public | internal | private")
	force := fs.Bool("force", false, "ข้าม dedup/id ซ้ำ (ของเดิมไม่ถูกเขียนทับ — ใช้ supersede)")
	noCommit := fs.Bool("no-commit", false, "เขียนไฟล์แต่ไม่ commit")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	if *layer == "" {
		return e.usagef("ต้องระบุ --layer")
	}
	if *scope == "" {
		return e.usagef("ต้องระบุ --scope")
	}
	if *file == "" {
		return e.usagef("ต้องระบุ --file (ใช้ - สำหรับ stdin)")
	}

	body, err := e.readBody(*file)
	if err != nil {
		return e.fail(err)
	}

	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	res, err := domain.Add(root, store.Record{Layer: *layer, Scope: *scope, Body: body}, domain.AddOptions{
		ID:          *id,
		Title:       *title,
		Tags:        tags,
		Confidence:  *confidence,
		Decay:       *decay,
		HalfLife:    *halfLife,
		Source:      *source,
		Session:     *session,
		Evidence:    evidence,
		Supersedes:  *supersedes,
		Supersede:   *supersede,
		Created:     *created,
		Expires:     *expires,
		Sensitivity: *sensitivity,
		Force:       *force,
		NoCommit:    *noCommit,
	})
	if err != nil {
		return e.fail(err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(e.Stderr, "mem: %s\n", w)
	}
	if !res.Committed && res.CommitReason != "" {
		// The record is on disk; the commit failed. Observable, non-fatal
		// (REQ-STORE-03) — `mem sync` will pick the file up later.
		fmt.Fprintf(e.Stderr, "mem: git commit ล้มเหลว: %s\n", firstLine(res.CommitReason))
	}
	fmt.Fprintf(e.Stdout, "%s\t%s", res.ID, res.Rel)
	if res.Superseded != "" {
		fmt.Fprintf(e.Stdout, "\tsuperseded=%s", res.Superseded)
	}
	fmt.Fprintln(e.Stdout)
	return ExitOK
}

// readBody reads the --file payload ("-" = stdin) and trims it like Python.
func (e *Env) readBody(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		if e.Stdin == nil {
			return "", fmt.Errorf("เนื้อหาว่าง — ส่งทาง stdin ด้วย --file -")
		}
		data, err = io.ReadAll(e.Stdin)
	} else {
		data, err = os.ReadFile(expandUser(path))
	}
	if err != nil {
		return "", fmt.Errorf("อ่าน %s: %w", path, err)
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return "", fmt.Errorf("เนื้อหาว่าง — ส่งทาง stdin ด้วย --file -")
	}
	return body, nil
}

// cmdTouch bumps last_used (Python cmd_touch).
func cmdTouch(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "<id>"); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}
	id := args[0]
	for _, r := range recs {
		if r.ID != id && !strings.HasPrefix(r.ID, id) {
			continue
		}
		if lastOf(r) == today() {
			fmt.Fprintf(e.Stdout, "%s แตะแล้ววันนี้\n", r.ID)
			return ExitOK
		}
		if _, terr := domain.Touch(root, []store.Record{r}); terr != nil {
			return e.fail(terr)
		}
		fmt.Fprintf(e.Stdout, "touched %s\n", r.ID)
		return ExitOK
	}
	fmt.Fprintf(e.Stderr, "mem: ไม่พบ record: %s\n", id)
	return ExitError
}

// cmdCite records one real use (uses + 1) under the store lock.
func cmdCite(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "<id>"); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	full, err := domain.Cite(root, args[0])
	if err != nil {
		return e.fail(err)
	}
	row := map[string]any{"t": nowStamp(), "type": "cite", "id": full, "agent": agentEnv(), "session": "-"}
	if lerr := logEvent(root, row); lerr != nil {
		fmt.Fprintf(e.Stderr, "mem: %v\n", lerr)
	}
	fmt.Fprintln(e.Stdout, full)
	return ExitOK
}

// cmdReview is the only quarantine exit — it requires screening evidence.
func cmdReview(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	var evidence stringsFlag
	fs.Var(&evidence, "evidence", `เช่น "reviewed: อ่านแล้ว ปลอดภัย"`)
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "<id>"); stop {
		return code
	}
	if len(evidence) == 0 {
		return e.usagef(`review ต้องมี --evidence "reviewed: <หมายเหตุ>"`)
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	id := args[0]
	// Resolve the exact id first so the report names the full id (Python r.id).
	full := id
	recs, serr := e.scan(root)
	if serr != nil {
		return e.fail(serr)
	}
	var matches []store.Record
	for _, r := range recs {
		if strings.HasPrefix(r.ID, id) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 1 {
		full = matches[0].ID
	}
	if err := domain.Review(root, id, evidence); err != nil {
		return e.fail(err)
	}
	row := map[string]any{"t": nowStamp(), "type": "review", "id": full, "agent": agentEnv(), "session": "-"}
	if lerr := logEvent(root, row); lerr != nil {
		fmt.Fprintf(e.Stderr, "mem: %v\n", lerr)
	}
	fmt.Fprintf(e.Stdout, "%s\treviewed\n", full)
	return ExitOK
}

// cmdSupersede marks <old> superseded by <new> (Python cmd_supersede).
func cmdSupersede(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	with := fs.String("with", "", "id ของ record ใหม่")
	fs.Bool("no-commit", false, "ยอมรับเพื่อ compatibility (supersede ไม่ commit เอง — `mem sync`/stop hook เก็บให้)")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "<old> --with <new>"); stop {
		return code
	}
	if *with == "" {
		return e.usagef("ต้องระบุ --with <new>")
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	old := args[0]
	if err := domain.Supersede(root, old, *with); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.Stdout, "%s -> superseded by %s\n", old, *with)
	return ExitOK
}
