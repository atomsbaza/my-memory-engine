// Package ext indexes external read-only markdown vaults (e.g. Obsidian)
// configured in <home>/.memory/external.json into a separate derived SQLite
// FTS5 database (<home>/.memory/external-engine.sqlite).
package ext

import (
	"database/sql"
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

// ReindexResult summarizes an external index rebuild.
type ReindexResult struct {
	Vaults int `json:"vaults"`
	Files  int `json:"files"`
	Chunks int `json:"chunks"`
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

// Reindex rebuilds external-engine.sqlite from all configured vaults (or one
// named vault when onlyVault is non-empty, still rebuilding the full table).
func Reindex(home, onlyVault string) (ReindexResult, error) {
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

	var allChunks []chunk
	fileCount := 0
	for _, v := range cfg.Vaults {
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
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(v.Path, p)
			if err != nil {
				rel = d.Name()
			}
			rel = filepath.ToSlash(rel)
			fileCount++
			allChunks = append(allChunks, chunkMarkdown(v.Name, rel, string(raw))...)
			return nil
		})
		if err != nil {
			return ReindexResult{}, fmt.Errorf("ext: walk %s (%s): %w", v.Name, v.Path, err)
		}
	}

	db, err := openDB(home)
	if err != nil {
		return ReindexResult{}, err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return ReindexResult{}, err
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ext_fts`,
		`DROP TABLE IF EXISTS ext_chunks`,
		`CREATE TABLE ext_chunks (id INTEGER PRIMARY KEY AUTOINCREMENT, vault TEXT NOT NULL, rel_path TEXT NOT NULL, heading TEXT NOT NULL, body TEXT NOT NULL)`,
		`CREATE VIRTUAL TABLE ext_fts USING fts5(chunk_id UNINDEXED, vault UNINDEXED, rel_path, heading, body, tokenize='unicode61')`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return ReindexResult{}, fmt.Errorf("ext: schema: %w", err)
		}
	}

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

	for _, c := range allChunks {
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

	if err := tx.Commit(); err != nil {
		return ReindexResult{}, err
	}
	return ReindexResult{Vaults: len(cfg.Vaults), Files: fileCount, Chunks: len(allChunks)}, nil
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
