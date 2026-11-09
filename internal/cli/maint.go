// Lifecycle subcommands (Python phase 2): archive, promote — including the
// mechanical --scan that feeds the /promote-lessons skill — and compact. The
// mutating operations live in internal/domain/maint.go (already shared with
// the MCP tools); this file owns the flags, the baseline's exact stdout and
// the scan's file side effects.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// --- archive ---------------------------------------------------------------

// cmdArchive moves low-retention records into _archive/<layer>/ (Python
// cmd_archive). The argparse default 0.15 lives here, not in the domain: an
// explicit --threshold 0 keeps meaning "nothing qualifies".
func cmdArchive(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	threshold := fs.Float64("threshold", 0.15, "เกณฑ์ S = decay·2^(−days/half_life)·(1+ln(1+uses))·urgency ที่ต่ำกว่า = ถูก archive")
	dryRun := fs.Bool("dry-run", false, "แสดง candidates อย่างเดียว ไม่ย้าย")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	res, err := domain.Archive(root, domain.ArchiveOptions{Threshold: *threshold, DryRun: *dryRun})
	// A run that fails after moving some files keeps its progress lines: the
	// caller sees which records already left the active tree (same contract as
	// the MCP tool).
	fmt.Fprint(e.Stdout, renderArchive(res))
	if err != nil {
		return e.fail(err)
	}
	return ExitOK
}

// --- promote ---------------------------------------------------------------

// cmdPromote turns proven lessons into rules (Python cmd_promote); --scan
// switches to the mechanical grouping pass that only writes the candidates
// file — judgment and approval stay in the /promote-lessons skill.
func cmdPromote(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	minUses := fs.Int("min-uses", 3, "จำนวน uses ขั้นต่ำ (default 3)")
	force := fs.Bool("force", false, "พิจารณาจาก uses อย่างเดียว (default: strict — successes>0 และ successes==uses)")
	dryRun := fs.Bool("dry-run", false, "แสดง candidates อย่างเดียว ไม่เขียน")
	scan := fs.Bool("scan", false, "จับกลุ่ม candidates สำหรับ /promote-lessons -> ops/promotions-candidates.md (ไม่ apply)")
	notify := fs.Bool("notify", false, "แจ้งเตือน macOS เมื่อผล scan เปลี่ยน (สำหรับ LaunchAgent)")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	if *scan {
		return promotionScan(e, *notify)
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	res, err := domain.Promote(root, domain.PromoteOptions{MinUses: *minUses, Force: *force, DryRun: *dryRun})
	fmt.Fprint(e.Stdout, renderPromote(res))
	if err != nil {
		return e.fail(err)
	}
	return ExitOK
}

// promoRow is one active lesson with its failure-line mention count.
type promoRow struct {
	rec   store.Record
	fails int
}

// promoGroup is one primary-tag group of rows in first-seen (scan) order.
type promoGroup struct {
	tag  string
	rows []promoRow
	maxF int // highest failure-mention count in the group
	sumU int // total uses across the group
}

// promotionScan mirrors Python _promotion_scan (bin/mem): group active lessons
// by primary tag with no LLM and no apply, write ops/promotions-candidates.md
// only when the content changed (the timestamp header is excluded from the
// digest, else every run "changes"), remember the digest in
// .memory/promo-scan.sha, and notify via osascript only on a real change.
func promotionScan(e *Env, notify bool) int {
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := store.Scan(root)
	if err != nil {
		return e.fail(fmt.Errorf("domain: promote --scan: %w", err))
	}
	var lessons []store.Record
	for _, r := range recs {
		if r.Layer == "lesson" && r.Status == "active" && r.Sensitivity != "quarantine" {
			lessons = append(lessons, r)
		}
	}
	if len(lessons) == 0 {
		fmt.Fprintln(e.Stdout, "promote --scan: ไม่มี lesson active — ไม่เขียนไฟล์")
		return ExitOK
	}

	// Every line of ops/failures/*.md contributes one token set a title can
	// cross-reference; unreadable files are skipped, never fatal.
	var failToks []map[string]bool
	fdir := filepath.Join(root, "ops", "failures")
	entries, derr := os.ReadDir(fdir)
	if derr == nil {
		for _, en := range entries { // ReadDir returns sorted names = glob order
			if en.IsDir() || !strings.HasSuffix(en.Name(), ".md") {
				continue
			}
			data, ferr := os.ReadFile(filepath.Join(fdir, en.Name()))
			if ferr != nil {
				continue
			}
			for _, ln := range strings.Split(string(data), "\n") {
				if ft := scanLongTokens(ln); len(ft) > 0 {
					failToks = append(failToks, ft)
				}
			}
		}
	}

	var groups []*promoGroup
	byTag := map[string]*promoGroup{}
	nFails := 0
	for _, r := range lessons {
		titleToks := scanLongTokens(r.TitleOrDerived())
		fails := 0
		for _, ft := range failToks {
			for t := range titleToks {
				if ft[t] {
					fails++
					break
				}
			}
		}
		if fails > 0 {
			nFails++
		}
		primary := "(no-tag)"
		if len(r.Tags) > 0 {
			primary = r.Tags[0]
		}
		g := byTag[primary]
		if g == nil {
			g = &promoGroup{tag: primary}
			byTag[primary] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, promoRow{rec: r, fails: fails})
		if fails > g.maxF {
			g.maxF = fails
		}
		g.sumU += r.Uses
	}
	// Ranking: cross-ref failures > total uses > group size, descending.
	// Python's sorted(..., reverse=True) is stable, so equal keys keep scan
	// order — SliceStable, not Slice.
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.maxF != b.maxF {
			return a.maxF > b.maxF
		}
		if a.sumU != b.sumU {
			return a.sumU > b.sumU
		}
		return len(a.rows) > len(b.rows)
	})

	now := time.Now().Format("2006-01-02T15:04:05")
	lines := []string{
		fmt.Sprintf("# promotion candidates — %s", now),
		"",
		"source: `mem promote --scan` (จับกลุ่มด้วยกลไก — ไม่มี LLM ไม่ apply)",
		"ขั้นถัดไป: skill `/promote-lessons` — lesson→rule · ซ้ำ→AGENTS.md/skill · ตรวจเครื่องได้→test",
		"",
		fmt.Sprintf("รวม **%d lessons · %d groups** · cross-ref failures: %d lessons", len(lessons), len(groups), nFails),
		"",
	}
	for _, g := range groups {
		gf, gu := 0, 0
		for _, row := range g.rows {
			if row.fails > 0 {
				gf++
			}
			gu += row.rec.Uses
		}
		lines = append(lines, fmt.Sprintf("## %s — %d lessons · failures-mentions %d · uses %d",
			g.tag, len(g.rows), gf, gu))
		rows := append([]promoRow(nil), g.rows...)
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].fails != rows[j].fails {
				return rows[i].fails > rows[j].fails
			}
			return rows[i].rec.Uses > rows[j].rec.Uses
		})
		for _, row := range rows {
			var marks []string
			if row.fails > 0 {
				marks = append(marks, fmt.Sprintf("failures=%d", row.fails))
			}
			if row.rec.Uses > 0 {
				marks = append(marks, fmt.Sprintf("uses=%d", row.rec.Uses))
			}
			suffix := ""
			if len(marks) > 0 {
				suffix = "  ·  " + strings.Join(marks, " · ")
			}
			lines = append(lines, fmt.Sprintf("- `%s`  %s%s", row.rec.ID, row.rec.TitleOrDerived(), suffix))
		}
		lines = append(lines, "")
	}
	content := strings.Join(lines, "\n") + "\n"

	// Digest skips the header line: it carries a per-second timestamp, so
	// hashing it whole would mark every run "changed" and notify for nothing.
	digestSrc := content
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		digestSrc = content[i+1:]
	}
	sum := sha256.Sum256([]byte(digestSrc))
	digest := hex.EncodeToString(sum[:])

	out := filepath.Join(root, "ops", "promotions-candidates.md")
	hashPath := filepath.Join(root, ".memory", "promo-scan.sha")
	prev := ""
	if data, herr := os.ReadFile(hashPath); herr == nil {
		prev = strings.TrimSpace(string(data))
	}
	for _, w := range []struct {
		path string
		data string
	}{
		{out, content},
		{hashPath, digest + "\n"},
	} {
		if merr := os.MkdirAll(filepath.Dir(w.path), 0o755); merr != nil {
			return e.fail(fmt.Errorf("เขียน %s ไม่ได้: %s", relPath(root, w.path), clipErr(merr)))
		}
		if werr := os.WriteFile(w.path, []byte(w.data), 0o644); werr != nil {
			return e.fail(fmt.Errorf("เขียน %s ไม่ได้: %s", relPath(root, w.path), clipErr(werr)))
		}
	}

	changed := prev != digest
	if changed && notify {
		notifyPromotionScan(len(lessons))
	}
	suffix := ""
	if !changed {
		suffix = "  (เนื้อหาไม่เปลี่ยนจากรอบก่อน — ไม่ notify)"
	}
	fmt.Fprintf(e.Stdout, "promote --scan: %d lessons · %d groups -> ops/promotions-candidates.md%s\n",
		len(lessons), len(groups), suffix)
	return ExitOK
}

// scanLongTokens is Python {t for t in tokens(s) if len(t) > 3}: distinct
// tokens longer than 3 code points (runes, not bytes — Thai tokens count
// characters, and byte-counting would admit 2-char Thai words).
func scanLongTokens(s string) map[string]bool {
	out := map[string]bool{}
	for t := range domain.Tokens(s) {
		if utf8.RuneCountInString(t) > 3 {
			out[t] = true
		}
	}
	return out
}

// notifyPromotionScan posts the baseline's macOS notification: best effort and
// never fatal (Python fail-open on OSError/TimeoutExpired; on other platforms
// osascript simply does not exist).
func notifyPromotionScan(n int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", "-e",
		fmt.Sprintf(`display notification "%d candidates ใน ops/promotions-candidates.md" with title "my-memory promote"`, n))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}

// relPath renders p relative to root when possible (report paths match the
// baseline's root-relative form).
func relPath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

// clipErr shortens an error message the way Python str(exc)[:160] does.
func clipErr(err error) string {
	msg := err.Error()
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}

// --- compact ---------------------------------------------------------------

// cmdCompact folds old handoffs into one knowledge record per scope (Python
// cmd_compact).
func cmdCompact(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	days := fs.Int("days", 7, "handoff ที่ last_used/created เก่ากว่า N วัน (default 7)")
	minGroup := fs.Int("min-group", 3, "scope ที่มี handoff ตั้งแต่ N ตัวจึงรวบ (default 3)")
	dryRun := fs.Bool("dry-run", false, "แสดงกลุ่มอย่างเดียว ไม่เขียน")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	res, err := domain.Compact(root, domain.CompactOptions{Days: *days, MinGroup: *minGroup, DryRun: *dryRun})
	fmt.Fprint(e.Stdout, renderCompact(res))
	if err != nil {
		return e.fail(err)
	}
	return ExitOK
}

// --- shared rendering (byte-parity with the baseline's stdout) -------------

// renderArchive reproduces the baseline's `mem archive` stdout.
func renderArchive(res domain.ArchiveResult) string {
	var b strings.Builder
	if res.DryRun {
		for _, cand := range res.Candidates {
			fmt.Fprintf(&b, "DRY  %s  S=%.3f  [%s/%s] %s\n", cand.ID, cand.Score, cand.Layer, cand.Scope, cand.Rel)
		}
		fmt.Fprintf(&b, "dry-run: %d candidates (threshold %s) — ยังไม่ย้าย\n",
			len(res.Candidates), pyFloat(res.Threshold))
		return b.String()
	}
	for _, m := range res.Moved {
		fmt.Fprintf(&b, "archived %s (S=%.3f) -> %s\n", m.ID, m.Score, m.Dest)
	}
	fmt.Fprintf(&b, "archive: moved %d/%d candidates (of %d active, threshold %s)\n",
		len(res.Moved), len(res.Candidates), res.Active, pyFloat(res.Threshold))
	return b.String()
}

// renderPromote reproduces the baseline's `mem promote` stdout.
func renderPromote(res domain.PromoteResult) string {
	var b strings.Builder
	if res.DryRun {
		for _, cand := range res.Candidates {
			fmt.Fprintf(&b, "DRY  %s  uses=%d successes=%d  [%s] %s\n",
				cand.ID, cand.Uses, cand.Successes, cand.Scope, cand.Rel)
		}
		fmt.Fprintf(&b, "dry-run: %d candidates (min-uses %d, %s) — ยังไม่เขียน\n",
			len(res.Candidates), res.MinUses, res.Mode)
		return b.String()
	}
	for _, p := range res.Promoted {
		fmt.Fprintf(&b, "promoted %s -> %s (%s)\n", p.From, p.To, p.Rel)
	}
	fmt.Fprintf(&b, "promote: %d/%d lessons -> rules (min-uses %d, %s)\n",
		len(res.Promoted), len(res.Candidates), res.MinUses, res.Mode)
	return b.String()
}

// renderCompact reproduces the baseline's `mem compact` stdout.
func renderCompact(res domain.CompactResult) string {
	var b strings.Builder
	if res.DryRun {
		for _, g := range res.Groups {
			fmt.Fprintf(&b, "DRY  scope=%s: %d handoffs — %s\n", g.Scope, len(g.IDs), strings.Join(g.IDs, ", "))
		}
		fmt.Fprintf(&b, "dry-run: %d groups (>= %d handoffs, older than %dd) — ยังไม่เขียน\n",
			len(res.Groups), res.MinGroup, res.Days)
		return b.String()
	}
	handoffs := 0
	for _, w := range res.Written {
		fmt.Fprintf(&b, "compacted %d handoffs (%s) -> %s (%s)\n", len(w.IDs), w.Scope, w.ID, w.Rel)
		handoffs += len(w.IDs)
	}
	fmt.Fprintf(&b, "compact: %d groups / %d handoffs -> knowledge (min-group %d, older than %dd)\n",
		len(res.Written), handoffs, res.MinGroup, res.Days)
	return b.String()
}

// pyFloat renders a float the way Python's str() does for the values a
// threshold flag can carry (0.15 -> "0.15", 1 -> "1.0").
func pyFloat(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
