package index

// P1.5 derived-index tests (AC-P1-08, AC-P1-09; REQ-IDX-01, REQ-IDX-02).
//
// All fixtures are synthetic (provenance: test-generated); no real corpus
// is used (NFR-PRIV-02). Tests are not parallel: several open the same
// fixture tree via copied writable homes.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

var fixtureHome = filepath.Join("..", "..", "testdata", "index", "home")

// Fixture record ids (see testdata/index/home).
const (
	denseID  = "knowledge.0a0a0a01"
	sparseID = "knowledge.0a0a0a02"
	thaiID   = "knowledge.0a0a0a03"
	lessonID = "lesson.0a0a0a04"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// copyTree copies a directory tree into dst (used to get a writable fixture
// home inside t.TempDir()).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyTree %s -> %s: %v", src, dst, err)
	}
}

// fixtureIndex copies the fixture home into a temp dir, scans it, opens the
// engine index beside it and rebuilds — the standard Given of most tests.
func fixtureIndex(t *testing.T) (*Index, []store.Record, string) {
	t.Helper()
	home := t.TempDir()
	copyTree(t, fixtureHome, home)
	records, err := store.Scan(home)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("scan: got %d records, want 4", len(records))
	}
	ix, err := Open(IndexPath(home))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	if err := ix.Rebuild(records); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return ix, records, home
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustSearch(t *testing.T, ix *Index, query string, limit int) []string {
	t.Helper()
	ids, err := ix.Search(query, limit)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return ids
}

// synthesize builds a minimal record for direct Rebuild input.
func synthesize(id, title, body string, tags ...string) store.Record {
	return store.Record{
		ID:    id,
		Rel:   id + ".md",
		Layer: "knowledge",
		Scope: "project:demo",
		Front: codec.Frontmatter{"title": title},
		Tags:  tags,
		Body:  body,
	}
}

// ---------------------------------------------------------------------------
// REQ-IDX-01 — engine-isolated filename, Python index never touched
// ---------------------------------------------------------------------------

// TestIndexPathIsEngineSpecific pins the isolation contract: the engine's
// index lives at .memory/index-<EngineName>.sqlite, distinct from the
// Python engine's index.sqlite and from any other engine's file.
func TestIndexPathIsEngineSpecific(t *testing.T) {
	p := IndexPath("/tmp/home-x")
	if got := filepath.Base(p); got != FileName {
		t.Fatalf("IndexPath base = %q, want %q", got, FileName)
	}
	if want := filepath.Join("/tmp/home-x", ".memory", "index-"+EngineName+".sqlite"); p != want {
		t.Fatalf("IndexPath = %q, want %q", p, want)
	}
	if FileName == "index.sqlite" {
		t.Fatalf("engine index filename must not be the Python engine's index.sqlite")
	}
	if EngineName == "" || FileName != "index-"+EngineName+".sqlite" {
		t.Fatalf("FileName %q must be index-<EngineName>.sqlite (EngineName=%q)", FileName, EngineName)
	}
	if _, err := Open(filepath.Join(t.TempDir(), ".memory", "index.sqlite")); err == nil {
		t.Fatalf("Open on the Python index.sqlite must refuse (REQ-IDX-01 negative control)")
	}
}

// TestPythonIndexUntouched proves the AC-P1-08 isolation gate: with a
// Python index.sqlite present, a full rebuild + query leaves it
// byte-identical and writes only the engine's own file.
func TestPythonIndexUntouched(t *testing.T) {
	home := t.TempDir()
	copyTree(t, fixtureHome, home)
	pyIdx := filepath.Join(home, ".memory", "index.sqlite")
	sentinel := []byte("python engine index sentinel — synthetic, not a real database")
	if err := os.MkdirAll(filepath.Dir(pyIdx), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pyIdx, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	before := sha256File(t, pyIdx)

	records, err := store.Scan(home)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	ix, err := Open(IndexPath(home))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()
	if err := ix.Rebuild(records); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if ids := mustSearch(t, ix, "quicksort", 10); len(ids) != 2 {
		t.Fatalf("search quicksort: got %v", ids)
	}

	if after := sha256File(t, pyIdx); after != before {
		t.Fatalf("Python index.sqlite changed: sha before %s after %s", before, after)
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".memory", "index*.sqlite*"))
	sort.Strings(matches)
	want := []string{pyIdx, IndexPath(home)}
	sort.Strings(want)
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf(".memory index files = %v, want exactly the untouched Python file and the engine file %v", matches, want)
	}
}

// ---------------------------------------------------------------------------
// REQ-IDX-01 — FTS5 search semantics (Python fts_ids parity)
// ---------------------------------------------------------------------------

// TestSearchFTSSemantics exercises MATCH with the unicode61 tokenizer,
// prefix terms, the tags column, OR across query tokens, bm25 ordering,
// the limit, and the no-usable-token contract.
func TestSearchFTSSemantics(t *testing.T) {
	ix, _, _ := fixtureIndex(t)

	if got, err := ix.Count(); err != nil || got != 4 {
		t.Fatalf("Count = %d, %v; want 4, nil", got, err)
	}
	if !ix.FTS5() {
		t.Fatalf("FTS5() = false after a normal rebuild; fallback would be silent degradation")
	}

	// bm25 ordering: the dense mention ranks above the sparse one.
	ids := mustSearch(t, ix, "quicksort", 10)
	if !reflect.DeepEqual(ids, []string{denseID, sparseID}) {
		t.Fatalf("search quicksort = %v, want [%s %s] (bm25 dense-first)", ids, denseID, sparseID)
	}
	// limit is respected and the best match survives it.
	if ids := mustSearch(t, ix, "quicksort", 1); !reflect.DeepEqual(ids, []string{denseID}) {
		t.Fatalf("search quicksort limit 1 = %v, want [%s]", ids, denseID)
	}
	// unicode61 Thai: whole-token words are prefix-matchable.
	if ids := mustSearch(t, ix, "ความจำ", 10); !reflect.DeepEqual(ids, []string{thaiID}) {
		t.Fatalf("search ความจำ = %v, want [%s]", ids, thaiID)
	}
	if ids := mustSearch(t, ix, "ระบบ", 10); !reflect.DeepEqual(ids, []string{thaiID}) {
		t.Fatalf("search ระบบ = %v, want [%s]", ids, thaiID)
	}
	// Prefix term: "rendez" matches "rendezvous".
	if ids := mustSearch(t, ix, "rendez", 10); !reflect.DeepEqual(ids, []string{lessonID}) {
		t.Fatalf("search rendez = %v, want [%s]", ids, lessonID)
	}
	// Tags column is indexed.
	if ids := mustSearch(t, ix, "tagprobe", 10); !reflect.DeepEqual(ids, []string{lessonID}) {
		t.Fatalf("search tagprobe = %v, want [%s]", ids, lessonID)
	}
	// Multi-token queries OR their prefix terms.
	ids = mustSearch(t, ix, "quicksort rendezvous", 10)
	if len(ids) != 3 {
		t.Fatalf("search quicksort rendezvous = %v, want 3 ids", ids)
	}
	// Queries with no usable token (>1 rune) match nothing, without error.
	for _, q := range []string{"", " ", "x", "!!!", "-"} {
		if ids, err := ix.Search(q, 10); err != nil || ids != nil {
			t.Fatalf("search %q = %v, %v; want nil, nil", q, ids, err)
		}
	}
}

// sameSet compares two token slices as sets — Python tokens() returns a
// set, so only membership is parity-relevant, not order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, t := range a {
		seen[t]++
	}
	for _, t := range b {
		seen[t]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// TestTokensParity pins the query tokenizer to Python tokens() /
// fts_ids semantics: distinct lowercased [0-9A-Za-z_ก-๙]+ runs, >1 rune.
func TestTokensParity(t *testing.T) {
	if got := tokens("Hello, HELLO world"); !reflect.DeepEqual(got, []string{"hello", "world"}) {
		t.Fatalf("tokens = %v", got)
	}
	if got := tokens("ค้น ความจำ quick-sort_2"); !sameSet(got, []string{"ค้น", "ความจำ", "quick", "sort_2"}) {
		t.Fatalf("tokens = %v", got)
	}
	if got := queryTokens("a bb ค้น"); !reflect.DeepEqual(got, []string{"bb", "ค้น"}) {
		t.Fatalf("queryTokens = %v (1-rune tokens dropped)", got)
	}
	if got := matchExpr([]string{"bb", "ค้น"}); got != "bb* OR ค้น*" {
		t.Fatalf("matchExpr = %q", got)
	}
}

// ---------------------------------------------------------------------------
// AC-P1-08 — schema_version stamp
// ---------------------------------------------------------------------------

// TestSchemaVersionStamp verifies the stamp in the file, its persistence
// across handles, and that a fresh unbuilt file reports no stamp.
func TestSchemaVersionStamp(t *testing.T) {
	ix, _, home := fixtureIndex(t)

	v, ok, err := ix.Version()
	if err != nil || !ok || v != SchemaVersion {
		t.Fatalf("Version = %d, %v, %v; want %d, true, nil", v, ok, err, SchemaVersion)
	}
	var raw string
	if err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&raw); err != nil {
		t.Fatalf("meta.schema_version row: %v", err)
	}
	if raw != "1" {
		t.Fatalf("meta.schema_version = %q, want %q", raw, "1")
	}
	var ftsFlag string
	if err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'fts5'`).Scan(&ftsFlag); err != nil {
		t.Fatalf("meta.fts5 row: %v", err)
	}
	if ftsFlag != "1" {
		t.Fatalf("meta.fts5 = %q, want 1", ftsFlag)
	}

	// The stamp persists across handles.
	ix.Close()
	ix2, err := Open(IndexPath(home))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ix2.Close()
	if v, ok, err := ix2.Version(); err != nil || !ok || v != SchemaVersion {
		t.Fatalf("reopen Version = %d, %v, %v", v, ok, err)
	}
	if stale, err := ix2.Stale(); err != nil || stale {
		t.Fatalf("Stale after own rebuild = %v, %v; want false, nil", stale, err)
	}
	if !ix2.FTS5() {
		t.Fatalf("FTS5() = false on reopen of an FTS index")
	}
}

// ---------------------------------------------------------------------------
// AC-P1-09 — idempotent rebuild, delete/corrupt/version-mismatch recovery
// ---------------------------------------------------------------------------

// TestRebuildIdempotent proves rebuilding over an existing index yields
// the same content — the P1 MVP answer to staleness is always full rebuild.
func TestRebuildIdempotent(t *testing.T) {
	ix, records, _ := fixtureIndex(t)

	queries := []string{"quicksort", "ความจำ", "rendez", "tagprobe"}
	var firstCount int
	var firstResults [][]string
	firstCount, _ = ix.Count()
	for _, q := range queries {
		firstResults = append(firstResults, mustSearch(t, ix, q, 10))
	}

	if err := ix.Rebuild(records); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if got, _ := ix.Count(); got != firstCount {
		t.Fatalf("Count after re-rebuild = %d, want %d", got, firstCount)
	}
	for i, q := range queries {
		if got := mustSearch(t, ix, q, 10); !reflect.DeepEqual(got, firstResults[i]) {
			t.Fatalf("search %q after re-rebuild = %v, want %v", q, got, firstResults[i])
		}
	}
	if v, ok, err := ix.Version(); err != nil || !ok || v != SchemaVersion {
		t.Fatalf("Version after re-rebuild = %d, %v, %v", v, ok, err)
	}
}

// TestRebuildAfterDelete proves the AC-P1-09 delete path: the index file is
// derived data, so deleting it and rebuilding from markdown fully restores
// search. No record file is touched (the fixture tree is read-only input).
func TestRebuildAfterDelete(t *testing.T) {
	ix, records, home := fixtureIndex(t)
	path := IndexPath(home)

	if err := ix.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("delete index: %v", err)
	}

	ix2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after delete: %v", err)
	}
	defer ix2.Close()
	if _, _, err := ix2.Version(); err != nil {
		t.Fatalf("Version on fresh file: %v", err)
	}
	if _, err := ix2.Count(); err == nil {
		t.Fatalf("Count on unbuilt file must fail loudly, not return 0 silently")
	}
	if err := ix2.Rebuild(records); err != nil {
		t.Fatalf("rebuild after delete: %v", err)
	}
	if got, _ := ix2.Count(); got != len(records) {
		t.Fatalf("Count after recovery rebuild = %d, want %d", got, len(records))
	}
	if ids := mustSearch(t, ix2, "quicksort", 10); !reflect.DeepEqual(ids, []string{denseID, sparseID}) {
		t.Fatalf("search after recovery rebuild = %v", ids)
	}
}

// TestVersionMismatchHealsByRebuild proves the AC-P1-09 version path: a
// foreign schema_version marks the file stale, and a rebuild from markdown
// replaces it rather than reusing or merging it.
func TestVersionMismatchHealsByRebuild(t *testing.T) {
	ix, records, home := fixtureIndex(t)

	// Simulate a file written by a different index-schema generation.
	if _, err := ix.db.Exec(`UPDATE meta SET value = '999' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if v, ok, err := ix.Version(); err != nil || !ok || v != 999 {
		t.Fatalf("tampered Version = %d, %v, %v", v, ok, err)
	}
	if stale, err := ix.Stale(); err != nil || !stale {
		t.Fatalf("Stale on mismatched stamp = %v, %v; want true, nil", stale, err)
	}

	ix.Close()
	ix2, err := Open(IndexPath(home))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ix2.Close()
	if stale, err := ix2.Stale(); err != nil || !stale {
		t.Fatalf("Stale after reopen = %v, %v; want true, nil", stale, err)
	}
	if err := ix2.Rebuild(records); err != nil {
		t.Fatalf("healing rebuild: %v", err)
	}
	if v, ok, err := ix2.Version(); err != nil || !ok || v != SchemaVersion {
		t.Fatalf("Version after healing rebuild = %d, %v, %v", v, ok, err)
	}
	if stale, err := ix2.Stale(); err != nil || stale {
		t.Fatalf("Stale after healing rebuild = %v, %v; want false, nil", stale, err)
	}
	if ids := mustSearch(t, ix2, "ความจำ", 10); !reflect.DeepEqual(ids, []string{thaiID}) {
		t.Fatalf("search after healing rebuild = %v", ids)
	}
}

// TestOpenCorruptFailsLoudly: a corrupt index file is refused at Open with
// an explicit error — recovery (delete + rebuild) stays a caller decision,
// never a silent fallback.
func TestOpenCorruptFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".memory", FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatalf("Open on a corrupt file must fail loudly")
	}
}

// TestSearchBeforeRebuildFailsLoudly: querying an opened-but-never-rebuilt
// index is an explicit error, not an empty success that would look like
// "no results".
func TestSearchBeforeRebuildFailsLoudly(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), ".memory", FileName))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer ix.Close()
	if ids, err := ix.Search("quicksort", 10); err == nil {
		t.Fatalf("Search before rebuild = %v, nil; want an explicit error", ids)
	}
}

// TestRebuildDropsStaleTerms proves rebuild really replaces the index: a
// term removed from (or added to) the records disappears from (or appears
// in) the search results after the next rebuild.
func TestRebuildDropsStaleTerms(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), ".memory", FileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()

	v1 := []store.Record{
		synthesize("knowledge.aa000001", "alpha holder", "alpha bravo text", "probe"),
		synthesize("knowledge.aa000002", "other", "charlie text", "probe"),
	}
	if err := ix.Rebuild(v1); err != nil {
		t.Fatalf("rebuild v1: %v", err)
	}
	if ids := mustSearch(t, ix, "alpha", 10); !reflect.DeepEqual(ids, []string{"knowledge.aa000001"}) {
		t.Fatalf("v1 search alpha = %v", ids)
	}

	v2 := []store.Record{
		synthesize("knowledge.aa000001", "rewritten", "delta text only now", "probe"),
		synthesize("knowledge.aa000002", "other", "charlie text", "probe"),
	}
	if err := ix.Rebuild(v2); err != nil {
		t.Fatalf("rebuild v2: %v", err)
	}
	if ids := mustSearch(t, ix, "alpha", 10); ids != nil {
		t.Fatalf("stale term alpha survived rebuild: %v", ids)
	}
	if ids := mustSearch(t, ix, "delta", 10); !reflect.DeepEqual(ids, []string{"knowledge.aa000001"}) {
		t.Fatalf("new term delta missing after rebuild: %v", ids)
	}
	if ids := mustSearch(t, ix, "charlie", 10); !reflect.DeepEqual(ids, []string{"knowledge.aa000002"}) {
		t.Fatalf("surviving term charlie = %v", ids)
	}

	// Empty homes rebuild to an empty index, not an error.
	if err := ix.Rebuild(nil); err != nil {
		t.Fatalf("rebuild nil: %v", err)
	}
	if n, _ := ix.Count(); n != 0 {
		t.Fatalf("Count after empty rebuild = %d, want 0", n)
	}
	if ids := mustSearch(t, ix, "delta", 10); ids != nil {
		t.Fatalf("search on empty index = %v, want nil", ids)
	}
}

// ---------------------------------------------------------------------------
// REQ-IDX-02 — substring fallback when FTS5 is unavailable
// ---------------------------------------------------------------------------

// TestSubstringFallback exercises the fallback path required by AC-P1-09.
// modernc.org/sqlite always ships FTS5, so the FTS5-less condition is
// forced white-box: the vtable is dropped and the flag flipped, exactly
// the state Rebuild leaves behind on an FTS5-less build. The fallback
// must answer, stay observable (FTS5() == false), and keep the
// no-usable-token contract.
func TestSubstringFallback(t *testing.T) {
	ix, _, _ := fixtureIndex(t)

	if _, err := ix.db.Exec(`DROP TABLE fts`); err != nil {
		t.Fatalf("drop fts: %v", err)
	}
	ix.fts = false // simulate the FTS5-unavailable build state

	if ix.FTS5() {
		t.Fatalf("FTS5() must be false in fallback mode (the report callers need)")
	}
	if ids := mustSearch(t, ix, "quicksort", 10); !reflect.DeepEqual(ids, []string{denseID, sparseID}) {
		t.Fatalf("fallback search quicksort = %v", ids)
	}
	if ids := mustSearch(t, ix, "ความจำ", 10); !reflect.DeepEqual(ids, []string{thaiID}) {
		t.Fatalf("fallback search ความจำ = %v", ids)
	}
	if ids := mustSearch(t, ix, "rendezvous", 10); !reflect.DeepEqual(ids, []string{lessonID}) {
		t.Fatalf("fallback search rendezvous = %v", ids)
	}
	if ids := mustSearch(t, ix, "quicksort", 1); len(ids) != 1 {
		t.Fatalf("fallback limit ignored: %v", ids)
	}
	if ids, err := ix.Search("x", 10); err != nil || ids != nil {
		t.Fatalf("fallback no-token search = %v, %v; want nil, nil", ids, err)
	}
	// Tag text stays reachable in fallback: tags are a rec-table column.
	if ids := mustSearch(t, ix, "tagprobe", 10); !reflect.DeepEqual(ids, []string{lessonID}) {
		t.Fatalf("fallback search over tags = %v", ids)
	}
}

// TestRebuildWithoutFTS5StampsFallback proves the degraded rebuild itself:
// when the vtable cannot be created, rec + stamps still land, the fts5
// meta flag records the fallback, and nothing fails silently. The
// createFTS seam stands in for an FTS5-unavailable driver build (the
// AC-P1-09 discriminating control).
func TestRebuildWithoutFTS5StampsFallback(t *testing.T) {
	ix, err := Open(filepath.Join(t.TempDir(), ".memory", FileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()

	orig := createFTS
	createFTS = func(*sql.Tx) error {
		return errors.New("simulated FTS5-unavailable build")
	}
	defer func() { createFTS = orig }()

	recs := []store.Record{synthesize("knowledge.bb000001", "fallback probe", "omega text", "probe")}
	if err := ix.Rebuild(recs); err != nil {
		t.Fatalf("rebuild without fts5: %v", err)
	}
	if ix.FTS5() {
		t.Fatalf("FTS5() = true; the failed vtable must leave fallback mode")
	}
	var flag string
	if err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'fts5'`).Scan(&flag); err != nil {
		t.Fatalf("meta.fts5 row: %v", err)
	}
	if flag != "0" {
		t.Fatalf("meta.fts5 = %q, want 0 (observable degraded mode)", flag)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("Count = %d, want 1 (rec rows must survive without FTS5)", n)
	}
	if ids := mustSearch(t, ix, "omega", 10); !reflect.DeepEqual(ids, []string{"knowledge.bb000001"}) {
		t.Fatalf("fallback search after fts5-less rebuild = %v", ids)
	}
	if v, ok, err := ix.Version(); err != nil || !ok || v != SchemaVersion {
		t.Fatalf("Version after fts5-less rebuild = %d, %v, %v", v, ok, err)
	}
}

// ---------------------------------------------------------------------------
// Regression probes — discriminating controls (memory rule: a test must
// fail on the pre-fix behavior, not just pass on the correct one)
// ---------------------------------------------------------------------------

// TestSearchIsNotSubstringOnFTS pins that the FTS path is real FTS5, not a
// substring scan: a mid-word substring that unicode61 never tokenizes as a
// prefix-rooted term must not match, while the true prefix term does.
func TestSearchIsNotSubstringOnFTS(t *testing.T) {
	ix, _, _ := fixtureIndex(t)

	// "endezvous" is a mid-word substring of "rendezvous": the substring
	// fallback would match it; FTS5 with prefix terms must not.
	if ids := mustSearch(t, ix, "endezvous", 10); ids != nil {
		t.Fatalf("FTS search matched mid-word substring: %v — the FTS path degraded to substring", ids)
	}
	// FTS matches the token itself and its prefixes only.
	if ids := mustSearch(t, ix, "rendezvous", 10); !reflect.DeepEqual(ids, []string{lessonID}) {
		t.Fatalf("search rendezvous = %v, want [%s]", ids, lessonID)
	}
}

// TestRecColumnsMirrorPython pins the rec-table column mapping to the
// Python reindex baseline (S3-relevant: same table shape, same values).
func TestRecColumnsMirrorPython(t *testing.T) {
	ix, _, _ := fixtureIndex(t)

	var id, path, layer, scope, status, title, tags, created, lastUsed, body string
	err := ix.db.QueryRow(`SELECT id, path, layer, scope, status, title, tags, created, last_used, body
		FROM rec WHERE id = ?`, thaiID).
		Scan(&id, &path, &layer, &scope, &status, &title, &tags, &created, &lastUsed, &body)
	if err != nil {
		t.Fatalf("rec row: %v", err)
	}
	if id != thaiID || path != filepath.Join("knowledge", "thai-memory-knowledge.0a0a0a03.md") ||
		layer != "knowledge" || scope != "project:demo" || status != "active" ||
		title != "ความจำถาวร" || tags != "thai" || created != "2026-01-04" || lastUsed != "" {
		t.Fatalf("rec columns = id=%q path=%q layer=%q scope=%q status=%q title=%q tags=%q created=%q last_used=%q",
			id, path, layer, scope, status, title, tags, created, lastUsed)
	}
	if !strings.Contains(body, "ความจำถาวร ระบบ") {
		t.Fatalf("rec body does not carry the record body verbatim")
	}

	// Untitled front-matter → empty title column (Python meta.get, not the
	// derived title).
	if err := ix.Rebuild([]store.Record{{
		ID: "knowledge.cc000001", Rel: "knowledge/x-knowledge.cc000001.md",
		Layer: "knowledge", Scope: "project:demo", Front: codec.Frontmatter{},
		Body: "body only",
	}}); err != nil {
		t.Fatal(err)
	}
	var t2 string
	if err := ix.db.QueryRow(`SELECT title FROM rec WHERE id = ?`, "knowledge.cc000001").Scan(&t2); err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("record missing from rec after rebuild")
		}
		t.Fatal(err)
	}
	if t2 != "" {
		t.Fatalf("title = %q, want empty (raw front-matter title, not derived)", t2)
	}
}

// ---------------------------------------------------------------------------
// AC-P1-10 · no network on the default path
// ---------------------------------------------------------------------------

// indexNetDeps returns the network-client packages found in a
// `go list -deps` output. Unlike the store gate (which forbids the whole
// net tree), the index gate pins the SQLite driver, whose pure-Go port
// legitimately depends on `net` and `net/netip` for socket-address
// plumbing — no dialing ever happens on the index path. The client stack
// (http, tls, rpc, smtp, …) stays forbidden.
func indexNetDeps(deps []string) []string {
	exact := map[string]bool{
		"net/http": true, "net/smtp": true, "net/rpc": true,
		"net/rpc/jsonrpc": true, "net/mail": true, "net/textproto": true,
		"net/cgi": true, "crypto/tls": true,
	}
	var hits []string
	for _, d := range deps {
		if exact[d] {
			hits = append(hits, d)
		}
	}
	return hits
}

func TestIndexNoNetworkClientDeps(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go binary unavailable: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goBin, "list", "-deps", "./internal/index")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local") // hard-offline
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps ./internal/index: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -deps ./internal/index: %v", err)
	}
	if hits := indexNetDeps(strings.Split(strings.TrimSpace(string(out)), "\n")); len(hits) != 0 {
		t.Errorf("internal/index gained network-client dependencies: %v", hits)
	}
}
