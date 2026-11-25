// Package ext indexes external read-only markdown vaults (e.g. Obsidian)
// configured in <home>/.memory/external.json into a separate derived SQLite
// FTS5 database (<home>/.memory/external-engine.sqlite).
package ext

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/tok"
	_ "modernc.org/sqlite"
)

const (
	ConfigFileName = "external.json"
	IndexFileName  = "external-engine.sqlite"
)

// Vault is one configured external markdown directory.
type Vault struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Config is the persisted .memory/external.json schema.
type Config struct {
	Vaults []Vault `json:"vaults"`
}

// ReindexResult summarizes an external index sync/rebuild.
type ReindexResult struct {
	Vaults  int `json:"vaults"`
	Files   int `json:"files"`
	Skipped int `json:"skipped"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	Chunks  int `json:"chunks"`
}

// Hit is one external vault search result.
type Hit struct {
	Vault   string `json:"vault"`
	RelPath string `json:"path"`
	Heading string `json:"heading"`
	Snippet string `json:"snippet"`
}

// ConfigPath returns <home>/.memory/external.json.
func ConfigPath(home string) string {
	return filepath.Join(home, ".memory", ConfigFileName)
}

// IndexPath returns <home>/.memory/external-engine.sqlite.
func IndexPath(home string) string {
	return filepath.Join(home, ".memory", IndexFileName)
}

// LoadConfig reads external.json; missing file returns an empty Config.
func LoadConfig(home string) (Config, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("ext: read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("ext: parse config: %w", err)
	}
	return cfg, nil
}

// SaveConfig writes external.json atomically.
func SaveConfig(home string, cfg Config) error {
	p := ConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("ext: mkdir config: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("ext: marshal config: %w", err)
	}
	data = append(data, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("ext: write temp config: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("ext: commit config: %w", err)
	}
	return nil
}

// AddVault registers a named external vault directory in external.json.
func AddVault(home, name, path string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("ext: vault name cannot be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("ext: resolve path %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("ext: stat %q: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("ext: %q is not a directory", abs)
	}
	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}
	for _, v := range cfg.Vaults {
		if v.Name == name {
			return fmt.Errorf("ext: vault %q already exists", name)
		}
	}
	cfg.Vaults = append(cfg.Vaults, Vault{Name: name, Path: abs})
	sort.Slice(cfg.Vaults, func(i, j int) bool { return cfg.Vaults[i].Name < cfg.Vaults[j].Name })
	return SaveConfig(home, cfg)
}

// RemoveVault unregisters a vault by name from external.json.
func RemoveVault(home, name string) (bool, error) {
	cfg, err := LoadConfig(home)
	if err != nil {
		return false, err
	}
	out := make([]Vault, 0, len(cfg.Vaults))
	removed := false
	for _, v := range cfg.Vaults {
		if v.Name == name {
			removed = true
			continue
		}
		out = append(out, v)
	}
	if !removed {
		return false, nil
	}
	cfg.Vaults = out
	if err := SaveConfig(home, cfg); err != nil {
		return false, err
	}
	return true, nil
}

type chunk struct {
	vault   string
	relPath string
	heading string
	body    string
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

func chunkMarkdown(vault, relPath, raw string) []chunk {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				start = i + 1
				break
			}
		}
	}

	var stack [6]string
	curHeading := relPath
	var buf []string
	var out []chunk

	flush := func() {
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		if text != "" {
			out = append(out, chunk{
				vault:   vault,
				relPath: relPath,
				heading: curHeading,
				body:    text,
			})
		}
		buf = buf[:0]
	}

	for i := start; i < len(lines); i++ {
		line := lines[i]
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			stack[level-1] = title
			for k := level; k < len(stack); k++ {
				stack[k] = ""
			}
			var parts []string
			for k := 0; k < level; k++ {
				if stack[k] != "" {
					parts = append(parts, stack[k])
				}
			}
			if len(parts) > 0 {
				curHeading = strings.Join(parts, " > ")
			}
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return out
}

func openDB(home string) (*sql.DB, error) {
	p := IndexPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("ext: mkdir index: %w", err)
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		return nil, fmt.Errorf("ext: open sqlite %s: %w", p, err)
	}
	return db, nil
}

type fileKey struct {
	vault   string
	relPath string
}

type fileState struct {
	mtimeNs int64
	sha256  string
}

// Reindex incrementally updates external-engine.sqlite (mtime + SHA-256).
func Reindex(home, onlyVault string) (ReindexResult, error) {
	return ReindexWithOptions(home, onlyVault, false)
}

// ReindexWithOptions updates external-engine.sqlite incrementally unless full is true.
func ReindexWithOptions(home, onlyVault string, full bool) (ReindexResult, error) {
	cfg, err := LoadConfig(home)
	if err != nil {
		return ReindexResult{}, err
	}
	var targets []Vault
	for _, v := range cfg.Vaults {
		if onlyVault == "" || v.Name == onlyVault {
			targets = append(targets, v)
		}
	}
	if onlyVault != "" && len(targets) == 0 {
		return ReindexResult{}, fmt.Errorf("ext: unknown vault %q", onlyVault)
	}

	db, err := openDB(home)
	if err != nil {
		return ReindexResult{}, err
	}
	defer db.Close()

	if !full {
		var hasFiles int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='ext_files'`).Scan(&hasFiles); err != nil || hasFiles == 0 {
			full = true
		}
	}

	existing := map[fileKey]fileState{}
	if !full {
		rows, err := db.Query(`SELECT vault, rel_path, mtime_ns, sha256 FROM ext_files`)
		if err != nil {
			return ReindexResult{}, fmt.Errorf("ext: read ext_files: %w", err)
		}
		for rows.Next() {
			var k fileKey
			var st fileState
			if err := rows.Scan(&k.vault, &k.relPath, &st.mtimeNs, &st.sha256); err != nil {
				rows.Close()
				return ReindexResult{}, err
			}
			existing[k] = st
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return ReindexResult{}, err
		}
		rows.Close()
	}

	type fileUpdate struct {
		key     fileKey
		mtimeNs int64
		sha256  string
		chunks  []chunk
		existed bool
	}

	var updates []fileUpdate
	var mtimeOnly []fileUpdate
	seen := map[fileKey]struct{}{}
	fileCount := 0
	skipped := 0

	walkVaults := cfg.Vaults
	if !full && onlyVault != "" {
		walkVaults = targets
	}

	for _, v := range walkVaults {
		err := filepath.WalkDir(v.Path, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(v.Path, p)
			if err != nil {
				rel = d.Name()
			}
			rel = filepath.ToSlash(rel)
			fk := fileKey{vault: v.Name, relPath: rel}
			seen[fk] = struct{}{}
			fileCount++

			mtimeNs := info.ModTime().UnixNano()
			prev, ok := existing[fk]
			if !full && ok && prev.mtimeNs == mtimeNs {
				skipped++
				return nil
			}

			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sumBytes := sha256.Sum256(raw)
			sum := hex.EncodeToString(sumBytes[:])
			if !full && ok && prev.sha256 == sum {
				skipped++
				mtimeOnly = append(mtimeOnly, fileUpdate{key: fk, mtimeNs: mtimeNs, sha256: sum, existed: true})
				return nil
			}

			updates = append(updates, fileUpdate{
				key:     fk,
				mtimeNs: mtimeNs,
				sha256:  sum,
				chunks:  chunkMarkdown(v.Name, rel, string(raw)),
				existed: ok,
			})
			return nil
		})
		if err != nil {
			return ReindexResult{}, fmt.Errorf("ext: walk %s (%s): %w", v.Name, v.Path, err)
		}
	}

	var deletes []fileKey
	if !full {
		for k := range existing {
			if onlyVault != "" && k.vault != onlyVault {
				continue
			}
			if _, ok := seen[k]; !ok {
				deletes = append(deletes, k)
			}
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return ReindexResult{}, err
	}
	defer tx.Rollback()

	if full {
		for _, stmt := range []string{
			`DROP TABLE IF EXISTS ext_fts`,
			`DROP TABLE IF EXISTS ext_chunks`,
			`DROP TABLE IF EXISTS ext_files`,
			`CREATE TABLE ext_chunks (id INTEGER PRIMARY KEY AUTOINCREMENT, vault TEXT NOT NULL, rel_path TEXT NOT NULL, heading TEXT NOT NULL, body TEXT NOT NULL)`,
			`CREATE TABLE ext_files (vault TEXT NOT NULL, rel_path TEXT NOT NULL, mtime_ns INTEGER NOT NULL, sha256 TEXT NOT NULL, PRIMARY KEY (vault, rel_path))`,
			`CREATE VIRTUAL TABLE ext_fts USING fts5(chunk_id UNINDEXED, vault UNINDEXED, rel_path, heading, body, tokenize='unicode61')`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return ReindexResult{}, fmt.Errorf("ext: schema: %w", err)
			}
		}
	}

	if !full && (len(deletes) > 0 || len(updates) > 0) {
		delFTS, err := tx.Prepare(`DELETE FROM ext_fts WHERE chunk_id IN (SELECT id FROM ext_chunks WHERE vault = ? AND rel_path = ?)`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer delFTS.Close()
		delChunks, err := tx.Prepare(`DELETE FROM ext_chunks WHERE vault = ? AND rel_path = ?`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer delChunks.Close()
		delFile, err := tx.Prepare(`DELETE FROM ext_files WHERE vault = ? AND rel_path = ?`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer delFile.Close()

		for _, dk := range deletes {
			if _, err := delFTS.Exec(dk.vault, dk.relPath); err != nil {
				return ReindexResult{}, err
			}
			if _, err := delChunks.Exec(dk.vault, dk.relPath); err != nil {
				return ReindexResult{}, err
			}
			if _, err := delFile.Exec(dk.vault, dk.relPath); err != nil {
				return ReindexResult{}, err
			}
		}
		for _, u := range updates {
			if !u.existed {
				continue
			}
			if _, err := delFTS.Exec(u.key.vault, u.key.relPath); err != nil {
				return ReindexResult{}, err
			}
			if _, err := delChunks.Exec(u.key.vault, u.key.relPath); err != nil {
				return ReindexResult{}, err
			}
			if _, err := delFile.Exec(u.key.vault, u.key.relPath); err != nil {
				return ReindexResult{}, err
			}
		}
	}

	if len(mtimeOnly) > 0 {
		updMtime, err := tx.Prepare(`UPDATE ext_files SET mtime_ns = ? WHERE vault = ? AND rel_path = ?`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer updMtime.Close()
		for _, m := range mtimeOnly {
			if _, err := updMtime.Exec(m.mtimeNs, m.key.vault, m.key.relPath); err != nil {
				return ReindexResult{}, err
			}
		}
	}

	if len(updates) > 0 {
		insChunk, err := tx.Prepare(`INSERT INTO ext_chunks (vault, rel_path, heading, body) VALUES (?,?,?,?)`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer insChunk.Close()
		insFTS, err := tx.Prepare(`INSERT INTO ext_fts (chunk_id, vault, rel_path, heading, body) VALUES (?,?,?,?,?)`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer insFTS.Close()
		insFile, err := tx.Prepare(`INSERT INTO ext_files (vault, rel_path, mtime_ns, sha256) VALUES (?,?,?,?)`)
		if err != nil {
			return ReindexResult{}, err
		}
		defer insFile.Close()

		for _, u := range updates {
			if _, err := insFile.Exec(u.key.vault, u.key.relPath, u.mtimeNs, u.sha256); err != nil {
				return ReindexResult{}, err
			}
			for _, c := range u.chunks {
				res, err := insChunk.Exec(c.vault, c.relPath, c.heading, c.body)
				if err != nil {
					return ReindexResult{}, err
				}
				cid, err := res.LastInsertId()
				if err != nil {
					return ReindexResult{}, err
				}
				if _, err := insFTS.Exec(cid, c.vault, c.relPath, tok.SegmentForFTS(c.heading), tok.SegmentForFTS(c.body)); err != nil {
					return ReindexResult{}, err
				}
			}
		}
	}

	var totalChunks int
	if err := tx.QueryRow(`SELECT count(*) FROM ext_chunks`).Scan(&totalChunks); err != nil {
		return ReindexResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReindexResult{}, err
	}
	return ReindexResult{
		Vaults:  len(cfg.Vaults),
		Files:   fileCount,
		Skipped: skipped,
		Updated: len(updates),
		Deleted: len(deletes),
		Chunks:  totalChunks,
	}, nil
}

// Search queries external-engine.sqlite via FTS5 with Thai-segmented tokens.
func Search(home, query, vault string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 10
	}
	if _, err := os.Stat(IndexPath(home)); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := openDB(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	seen := map[string]struct{}{}
	var toks []string
	for _, t := range tok.Tokenize(query) {
		if utf8.RuneCountInString(t) > 1 {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				toks = append(toks, t)
			}
		}
	}
	if len(toks) == 0 {
		return nil, nil
	}
	sort.Strings(toks)
	parts := make([]string, len(toks))
	for i, t := range toks {
		parts[i] = t + "*"
	}
	match := strings.Join(parts, " OR ")

	var rows *sql.Rows
	if vault != "" {
		rows, err = db.Query(`
			SELECT c.vault, c.rel_path, c.heading, c.body
			FROM ext_fts f
			JOIN ext_chunks c ON c.id = f.chunk_id
			WHERE ext_fts MATCH ? AND c.vault = ?
			ORDER BY bm25(ext_fts)
			LIMIT ?`, match, vault, limit)
	} else {
		rows, err = db.Query(`
			SELECT c.vault, c.rel_path, c.heading, c.body
			FROM ext_fts f
			JOIN ext_chunks c ON c.id = f.chunk_id
			WHERE ext_fts MATCH ?
			ORDER BY bm25(ext_fts)
			LIMIT ?`, match, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("ext: search %q: %w", query, err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		var body string
		if err := rows.Scan(&h.Vault, &h.RelPath, &h.Heading, &body); err != nil {
			return nil, err
		}
		h.Snippet = clipSnippet(body, 200)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func clipSnippet(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
