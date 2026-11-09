// Package index implements the derived full-text index (P1.5): a SQLite
// FTS5 database rebuilt from canonical markdown records, stored in an
// engine-specific file that is separate from the Python engine's
// .memory/index.sqlite (REQ-IDX-01).
//
// Reference: ~/my-memory/bin/mem — reindex, fts_ids. FTS5 semantics
// (MATCH, bm25 ordering, unicode61 tokenizer) follow the S1-verified
// Python sqlite3 baseline: the same rec/fts table shapes, the same
// query-token expansion (tokens of >1 rune, OR-joined as prefix terms).
//
// The index is derived data only. The markdown working tree is the single
// source of truth (REQ-REC-01): Rebuild always DROP+CREATEs from records,
// the index is never merged incrementally (full rebuild is the P1 MVP;
// incremental rebuild is a later trigger), and nothing outside this
// engine's own index file is written. The file carries a schema_version
// stamp so two engine generations can never overwrite each other silently
// (design H1: refuse-and-rebuild on mismatch).
//
// The engine is local-only: this package performs no network calls
// (NFR-NET-01 / AC-P1-10).
package index

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/vec"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (cgo-free, FTS5 built in)
)

// ---------------------------------------------------------------------------
// Engine identity — REQ-IDX-01
// ---------------------------------------------------------------------------

const (
	// EngineName identifies this engine among the engines that may share
	// one memory home. The P1 spec supersedes the design draft's literal
	// "index-go.sqlite" with the language-neutral form
	// .memory/index-<engine>.sqlite; each engine owns a distinct name and
	// no two engines share an index file.
	EngineName = "engine"

	// FileName is this engine's derived-index filename inside .memory/.
	FileName = "index-" + EngineName + ".sqlite"

	// SchemaVersion is the derived-index schema generation this build
	// writes. A file stamped with a different value is stale: rebuild it,
	// never reuse it silently (AC-P1-09).
	SchemaVersion = 1

	// pythonIndexName is the Python engine's index file. This engine must
	// not open it for writing — REQ-IDX-01 forbids touching it.
	pythonIndexName = "index.sqlite"
)

// IndexPath returns this engine's derived-index path for a memory home
// root: <root>/.memory/index-engine.sqlite.
func IndexPath(root string) string {
	return filepath.Join(root, ".memory", FileName)
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

// Index is an open derived-index database. Everything in it can be
// reconstructed by Rebuild from the markdown records; nothing here is a
// source of truth.
type Index struct {
	path string
	db   *sql.DB
	fts  bool // FTS5 vtable present; false → Search uses the substring fallback
}

// Open opens (creating when absent) the derived index at path. It refuses
// the Python engine's index.sqlite even if handed that path by mistake
// (REQ-IDX-01), and fails loudly on a corrupt or unreadable file instead
// of deferring the failure to the first query.
func Open(path string) (*Index, error) {
	if filepath.Base(path) == pythonIndexName {
		return nil, fmt.Errorf("index: refusing to open %s: that file belongs to the Python engine; this engine writes %s (REQ-IDX-01)", path, FileName)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("index: create %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("index: open %s: %w", path, err)
	}
	ix := &Index{path: path, db: db}
	if err := ix.refreshFTS(); err != nil {
		db.Close()
		return nil, err
	}
	return ix, nil
}

// refreshFTS probes the database — surfacing a corrupt file now, not at
// first query — and records whether the fts vtable exists.
func (ix *Index) refreshFTS() error {
	var n int
	err := ix.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'fts'`).Scan(&n)
	if err != nil {
		return fmt.Errorf("index: open %s: %w", ix.path, err)
	}
	ix.fts = n > 0
	return nil
}

// Close closes the index database.
func (ix *Index) Close() error {
	if err := ix.db.Close(); err != nil {
		return fmt.Errorf("index: close %s: %w", ix.path, err)
	}
	return nil
}

// Path returns the index file path.
func (ix *Index) Path() string { return ix.path }

// FTS5 reports whether FTS5 full-text search is in use. false means the
// index was rebuilt without the FTS5 vtable and Search answers via the
// substring fallback — callers must report that to the user, never
// present fallback results as ranked search (AC-P1-09).
func (ix *Index) FTS5() bool { return ix.fts }

// Count returns the number of records in the rec table, so callers can
// compare the index against the markdown working tree (verify).
func (ix *Index) Count() (int, error) {
	var n int
	if err := ix.db.QueryRow(`SELECT count(*) FROM rec`).Scan(&n); err != nil {
		return 0, fmt.Errorf("index: count %s: %w", ix.path, err)
	}
	return n, nil
}

// Version returns the schema_version stamped in the file and whether a
// stamp is present at all. A file without the meta table (fresh, never
// rebuilt) simply has no stamp; a file that cannot be read errors.
func (ix *Index) Version() (version int, ok bool, err error) {
	var have int
	if err := ix.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&have); err != nil {
		return 0, false, fmt.Errorf("index: read schema_version %s: %w", ix.path, err)
	}
	if have == 0 {
		return 0, false, nil
	}
	var v string
	if err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("index: read schema_version %s: %w", ix.path, err)
	}
	n, aerr := strconv.Atoi(v)
	if aerr != nil {
		return 0, true, fmt.Errorf("index: schema_version %q: %w", v, aerr)
	}
	return n, true, nil
}

// Stale reports whether the file's schema_version was written by a
// different index-schema generation (present and != SchemaVersion). A
// stale index must be rebuilt before use — never queried as-is and never
// silently overwritten by a foreign generation (design H1).
func (ix *Index) Stale() (bool, error) {
	v, ok, err := ix.Version()
	if err != nil || !ok {
		return false, err
	}
	return v != SchemaVersion, nil
}

// ---------------------------------------------------------------------------
// Rebuild — REQ-IDX-02
// ---------------------------------------------------------------------------
// createFTS creates the FTS5 virtual table inside a rebuild transaction.
// It is a package-level seam so tests can simulate an FTS5-unavailable
// build (the AC-P1-09 discriminating control) without shipping a second
// driver build.
var createFTS = func(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE VIRTUAL TABLE fts USING fts5(id UNINDEXED, title, body, tags, tokenize='unicode61')`)
	return err
}

// embMetaKeys are the meta stamps that describe the emb table. Rebuild
// carries them across (the emb table itself survives an automatic rebuild —
// only `mem reindex` and DropEmb invalidate embeddings deliberately).
var embMetaKeys = [3]string{"embed_model", "embed_dim", "embed_count"}

// Rebuild replaces the lexical index from records in one transaction:
// drop + create the rec/fts/meta tables, insert every record, stamp
// schema_version. It is idempotent — rebuilding over an existing index
// (stale, corrupt-schema, or current) yields the same content — and it
// writes nothing but this engine's own index file. When FTS5 is
// unavailable the rec table and stamps are still written and Search falls
// back to substring matching; the degraded mode is observable via FTS5()
// and the fts5 meta row, never silent.
//
// The emb table and its meta stamps are deliberately preserved: automatic
// rebuilds (the stop hook) must not silently discard the embedding index,
// which only a mode-aware `mem reindex` rebuilds. `mem reindex` in off mode
// calls DropEmb so an off-mode index is provably emb-free.
func (ix *Index) Rebuild(records []store.Record) error {
	embStamps := ix.readEmbStamps()
	tx, err := ix.db.Begin()
	if err != nil {
		return fmt.Errorf("index: rebuild %s: begin: %w", ix.path, err)
	}
	defer tx.Rollback() // no-op after a successful commit

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS rec`,
		`DROP TABLE IF EXISTS fts`,
		`DROP TABLE IF EXISTS meta`,
		`CREATE TABLE rec (id TEXT, path TEXT, layer TEXT, scope TEXT, status TEXT, ` +
			`title TEXT, tags TEXT, created TEXT, last_used TEXT, body TEXT)`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("index: rebuild %s: %q: %w", ix.path, stmt, err)
		}
	}
	_ = embStamps

	fts := true
	if err := createFTS(tx); err != nil {
		fts = false // FTS5 unavailable in this build → substring fallback (REQ-IDX-02)
	}

	insRec, err := tx.Prepare(`INSERT INTO rec VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("index: rebuild %s: prepare rec insert: %w", ix.path, err)
	}
	defer insRec.Close()
	var insFts *sql.Stmt
	if fts {
		if insFts, err = tx.Prepare(`INSERT INTO fts VALUES (?,?,?,?)`); err != nil {
			return fmt.Errorf("index: rebuild %s: prepare fts insert: %w", ix.path, err)
		}
		defer insFts.Close()
	}

	for _, r := range records {
		// Column mapping mirrors Python reindex 1:1 — rec gets the raw
		// front-matter title (not the derived title), space-joined tags,
		// and str() semantics for created/last_used.
		title := frontString(r.Front, "title")
		tags := strings.Join(r.Tags, " ")
		if _, err := insRec.Exec(r.ID, r.Rel, r.Layer, r.Scope, r.Status, title, tags,
			frontString(r.Front, "created"), frontString(r.Front, "last_used"), r.Body); err != nil {
			return fmt.Errorf("index: rebuild %s: insert rec %s: %w", ix.path, r.ID, err)
		}
		if fts {
			if _, err := insFts.Exec(r.ID, title, r.Body, tags); err != nil {
				return fmt.Errorf("index: rebuild %s: insert fts %s: %w", ix.path, r.ID, err)
			}
		}
	}

	stamps := [][2]string{
		{"schema_version", strconv.Itoa(SchemaVersion)},
		{"engine", EngineName},
		{"fts5", "1"},
	}
	if !fts {
		stamps[2] = [2]string{"fts5", "0"}
	}
	stamps = append(stamps, embStamps...)
	for _, kv := range stamps {
		if _, err := tx.Exec(`INSERT INTO meta VALUES (?,?)`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("index: rebuild %s: stamp %s: %w", ix.path, kv[0], err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("index: rebuild %s: commit: %w", ix.path, err)
	}
	ix.fts = fts
	return nil
}

// ---------------------------------------------------------------------------
// Embeddings — the optional emb table (semantic-search mode)
// ---------------------------------------------------------------------------

// EmbRow is one stored chunk vector: the chunk_idx-th chunk of record id.
type EmbRow struct {
	ID       string
	ChunkIdx int
	Vec      []byte // embed.EncodeVec output (little-endian float32)
}

// readEmbStamps returns the embed_* meta rows so Rebuild can carry them
// across a lexical rebuild (the emb table itself is not dropped).
func (ix *Index) readEmbStamps() [][2]string {
	var have int
	if err := ix.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&have); err != nil || have == 0 {
		return nil
	}
	var out [][2]string
	for _, k := range embMetaKeys {
		var v string
		err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, k).Scan(&v)
		if err != nil {
			continue // absent stamp: nothing to carry
		}
		out = append(out, [2]string{k, v})
	}
	return out
}

// SetEmb replaces the emb table with rows in one transaction and stamps
// embed_model/embed_dim/embed_count. Every row's vector must share one
// dimension — a ragged set is refused before any write.
func (ix *Index) SetEmb(rows []EmbRow, model string, dim int) error {
	if dim <= 0 {
		return fmt.Errorf("index: set emb %s: dim %d ไม่ถูกต้อง", ix.path, dim)
	}
	for i, r := range rows {
		if len(r.Vec) != 4*dim {
			return fmt.Errorf("index: set emb %s: row %d (%s#%d) ยาว %d bytes ไม่ตรง dim %d", ix.path, i, r.ID, r.ChunkIdx, len(r.Vec), dim)
		}
	}
	tx, err := ix.db.Begin()
	if err != nil {
		return fmt.Errorf("index: set emb %s: begin: %w", ix.path, err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{`DROP TABLE IF EXISTS emb`, `CREATE TABLE emb (id TEXT, chunk_idx INTEGER, vec BLOB)`} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("index: set emb %s: %q: %w", ix.path, stmt, err)
		}
	}
	ins, err := tx.Prepare(`INSERT INTO emb VALUES (?,?,?)`)
	if err != nil {
		return fmt.Errorf("index: set emb %s: prepare: %w", ix.path, err)
	}
	defer ins.Close()
	for _, r := range rows {
		if _, err := ins.Exec(r.ID, r.ChunkIdx, r.Vec); err != nil {
			return fmt.Errorf("index: set emb %s: insert %s#%d: %w", ix.path, r.ID, r.ChunkIdx, err)
		}
	}
	for _, kv := range [][2]string{
		{"embed_model", model},
		{"embed_dim", strconv.Itoa(dim)},
		{"embed_count", strconv.Itoa(len(rows))},
	} {
		if _, err := tx.Exec(`INSERT INTO meta VALUES (?,?)`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("index: set emb %s: stamp %s: %w", ix.path, kv[0], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("index: set emb %s: commit: %w", ix.path, err)
	}
	return nil
}

// DropEmb removes the emb table and its meta stamps — the provable off-mode
// state: an off-mode index carries no embedding bytes at all.
func (ix *Index) DropEmb() error {
	tx, err := ix.db.Begin()
	if err != nil {
		return fmt.Errorf("index: drop emb %s: begin: %w", ix.path, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS emb`); err != nil {
		return fmt.Errorf("index: drop emb %s: %w", ix.path, err)
	}
	for _, k := range embMetaKeys {
		if _, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, k); err != nil {
			return fmt.Errorf("index: drop emb %s: clear %s: %w", ix.path, k, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("index: drop emb %s: commit: %w", ix.path, err)
	}
	return nil
}

// EmbMeta returns the embedding stamps: the model, vector dimension and
// stored-chunk count. ok is false when no emb table exists (off mode, or
// embeddings never built).
func (ix *Index) EmbMeta() (model string, dim, count int, ok bool, err error) {
	var have int
	if err = ix.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'emb'`).Scan(&have); err != nil {
		return "", 0, 0, false, fmt.Errorf("index: read emb meta %s: %w", ix.path, err)
	}
	if have == 0 {
		return "", 0, 0, false, nil
	}
	var m, d, c string
	if err = ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'embed_model'`).Scan(&m); err != nil {
		return "", 0, 0, false, fmt.Errorf("index: read embed_model %s: %w", ix.path, err)
	}
	if err = ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'embed_dim'`).Scan(&d); err != nil {
		return "", 0, 0, false, fmt.Errorf("index: read embed_dim %s: %w", ix.path, err)
	}
	if err = ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'embed_count'`).Scan(&c); err != nil {
		return "", 0, 0, false, fmt.Errorf("index: read embed_count %s: %w", ix.path, err)
	}
	dim, derr := strconv.Atoi(d)
	if derr != nil {
		return "", 0, 0, false, fmt.Errorf("index: embed_dim %q: %w", ix.path, derr)
	}
	count, cerr := strconv.Atoi(c)
	if cerr != nil {
		return "", 0, 0, false, fmt.Errorf("index: embed_count %q: %w", ix.path, cerr)
	}
	return m, dim, count, true, nil
}

// DenseSearch ranks record ids by the best cosine similarity between qvec and
// any stored chunk of that record ("max per record"), most similar first, at
// most limit ids. A dimension mismatch between qvec and a stored row is an
// observable error, never a silently wrong similarity.
func (ix *Index) DenseSearch(qvec []float32, limit int) ([]string, error) {
	rows, err := ix.db.Query(`SELECT id, vec FROM emb`)
	if err != nil {
		return nil, fmt.Errorf("index: dense search %s: %w", ix.path, err)
	}
	defer rows.Close()
	best := map[string]float64{}
	var order []string
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, fmt.Errorf("index: dense search %s: %w", ix.path, err)
		}
		stored, derr := vec.Decode(blob)
		if derr != nil {
			return nil, fmt.Errorf("index: dense search %s: %w", ix.path, derr)
		}
		sim, cerr := vec.Cosine(qvec, stored)
		if cerr != nil {
			return nil, cerr
		}
		if _, seen := best[id]; !seen {
			// Mark the key even when the first chunk scores 0 so later
			// chunks of the same record cannot re-append the id.
			best[id] = math.Inf(-1)
			order = append(order, id)
		}
		if sim > best[id] {
			best[id] = sim
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: dense search %s: %w", ix.path, err)
	}
	sort.SliceStable(order, func(i, j int) bool { return best[order[i]] > best[order[j]] })
	if limit >= 0 && len(order) > limit {
		order = order[:limit]
	}
	return order, nil
}

// ---------------------------------------------------------------------------
// Search — FTS5 MATCH + bm25 (Python fts_ids parity)
// ---------------------------------------------------------------------------

// Search returns the ids of records matching query, best bm25 score
// first, at most limit ids — mirroring Python fts_ids: the query is
// tokenized, tokens longer than one rune are kept, and each becomes a
// prefix term OR-joined into the FTS5 MATCH expression. limit follows SQL
// LIMIT semantics (pass 40 for the Python default; a negative limit is
// unlimited). A query with no usable tokens matches nothing: (nil, nil).
// Without the FTS5 vtable the substring fallback answers instead
// (REQ-IDX-02); FTS5() reports which path is live.
func (ix *Index) Search(query string, limit int) ([]string, error) {
	if !ix.fts {
		return ix.searchSubstring(query, limit)
	}
	toks := queryTokens(query)
	if len(toks) == 0 {
		return nil, nil
	}
	rows, err := ix.db.Query(`SELECT id FROM fts WHERE fts MATCH ? ORDER BY bm25(fts) LIMIT ?`,
		matchExpr(toks), limit)
	if err != nil {
		return nil, fmt.Errorf("index: search %q in %s: %w", query, ix.path, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("index: search %q in %s: %w", query, ix.path, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: search %q in %s: %w", query, ix.path, err)
	}
	return ids, nil
}

// searchSubstring is the FTS5-unavailable fallback (REQ-IDX-02): a plain
// case-insensitive substring scan over the rec table, OR across query
// tokens, in record order. It is not bm25-ranked — the caller must report
// the fallback (FTS5() == false), never present it as ranked search.
func (ix *Index) searchSubstring(query string, limit int) ([]string, error) {
	toks := queryTokens(query)
	if len(toks) == 0 || limit == 0 {
		return nil, nil
	}
	rows, err := ix.db.Query(`SELECT id, title, body, tags FROM rec`)
	if err != nil {
		return nil, fmt.Errorf("index: substring search %q in %s: %w (index not rebuilt?)", query, ix.path, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, title, body, tags string
		if err := rows.Scan(&id, &title, &body, &tags); err != nil {
			return nil, fmt.Errorf("index: substring search %q in %s: %w", query, ix.path, err)
		}
		hay := strings.ToLower(title + "\n" + body + "\n" + tags)
		for _, t := range toks { // tokens are already lowercased
			if strings.Contains(hay, t) {
				ids = append(ids, id)
				break
			}
		}
		if limit > 0 && len(ids) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: substring search %q in %s: %w", query, ix.path, err)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// Tokenizer — Python tokens() / fts_ids parity
// ---------------------------------------------------------------------------

// tokenRe mirrors Python tokens(): re.findall(r"[0-9A-Za-z_ก-๙]+", s.lower()).
var tokenRe = regexp.MustCompile(`[0-9A-Za-z_ก-๙]+`)

// tokens returns the distinct [0-9A-Za-z_ก-๙]+ matches over the lowercased
// input, sorted. Python returns a set; OR-matching is order-insensitive,
// and sorting keeps the generated MATCH expression deterministic.
func tokens(s string) []string {
	seen := make(map[string]struct{})
	for _, t := range tokenRe.FindAllString(strings.ToLower(s), -1) {
		seen[t] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// queryTokens keeps tokens longer than one rune, as Python fts_ids does.
func queryTokens(query string) []string {
	all := tokens(query)
	out := all[:0]
	for _, t := range all {
		if utf8.RuneCountInString(t) > 1 {
			out = append(out, t)
		}
	}
	return out
}

// matchExpr builds the FTS5 MATCH expression: "tok1* OR tok2* …".
func matchExpr(toks []string) string {
	parts := make([]string, len(toks))
	for i, t := range toks {
		parts[i] = t + "*"
	}
	return strings.Join(parts, " OR ")
}

// ---------------------------------------------------------------------------
// Front-matter helpers
// ---------------------------------------------------------------------------

// frontString mirrors Python str(meta.get(key, "")): absent → "", string →
// itself, anything else → its string form. Used for the rec-table columns
// Python derives straight from raw front-matter (title, created,
// last_used).
func frontString(fm codec.Frontmatter, key string) string {
	v, ok := fm[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
