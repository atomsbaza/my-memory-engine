// `mem embed` — the engine-only embedding-mode family: `embed status` shows
// the effective config plus the index's embedding state, `embed set` flips
// the taxonomy.json embedding block atomically (with a timestamped backup)
// between off (default, no network) and ollama (local semantic search).
//
// The dense query seam lives here too: it is the single place that turns
// "provider on + emb table present + model match" into the rank function the
// retrieval paths fuse as RRF list #4. Off mode never constructs it, which is
// what keeps off-mode retrieval byte-identical to the pre-embedding engine.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/embed"
	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/store"
	"github.com/atomsbaza/my-memory-engine/internal/vec"
)

// denseSeam returns the query-time dense rank function, or (nil, nil) when
// embedding mode is off or the index carries no emb table. A model mismatch
// between the index stamps and the configured provider is an error the search
// paths must surface — silently stale dense ranks are forbidden.
func (e *Env) denseSeam(root string, cfg embed.Config) (func(query string, limit int) ([]string, error), func(), error) {
	provider := embed.NewFromConfig(cfg)
	if provider == nil {
		return nil, func() {}, nil // off mode: no dense stream, ever
	}
	path := index.IndexPath(root)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, func() {}, nil // provider on, index never built: no emb yet
		}
		return nil, nil, err
	}
	ix, err := index.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("index %s: %w", path, err)
	}
	model, dim, _, ok, err := ix.EmbMeta()
	if err != nil {
		ix.Close()
		return nil, nil, err
	}
	if !ok {
		// Provider on but no emb table: embeddings not built yet (the user
		// has not run `mem reindex` since enabling). No dense stream — the
		// next reindex builds it.
		_ = ix.Close()
		return nil, func() {}, nil
	}
	if model != provider.Model() {
		ix.Close()
		return nil, nil, fmt.Errorf("embed: index สร้างด้วย model %q แต่ config ใช้ %q — รัน `mem reindex` ก่อน (ไม่งั้น dense ranks จะเก่า)", model, provider.Model())
	}
	search := func(query string, limit int) ([]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), embed.RequestTimeout)
		defer cancel()
		vecs, err := provider.Embed(ctx, []string{query})
		if err != nil {
			return nil, err
		}
		if len(vecs) != 1 {
			return nil, fmt.Errorf("embed: ได้ %d vectors จาก query 1 ข้อความ", len(vecs))
		}
		if len(vecs[0]) != dim {
			return nil, fmt.Errorf("embed: query dim %d ไม่ตรง index dim %d (model เปลี่ยน?) — รัน `mem reindex`", len(vecs[0]), dim)
		}
		return ix.DenseSearch(vecs[0], limit)
	}
	return search, func() { _ = ix.Close() }, nil
}

// buildEmbRows chunks every record body (the same 400-rune chunking the
// lexical vector ranks use) and embeds the chunk texts in provider-sized
// batches. Failures propagate — a half-embedded index must never be written.
func buildEmbRows(provider embed.Provider, recs []store.Record) ([]index.EmbRow, error) {
	type chunkRef struct {
		id  string
		idx int
	}
	var order []chunkRef
	texts := make([]string, 0, len(recs)*2)
	for _, r := range recs {
		for i, ch := range domain.ChunkText(r.Body, 400) {
			if strings.TrimSpace(ch) == "" {
				continue
			}
			order = append(order, chunkRef{id: r.ID, idx: i})
			texts = append(texts, ch)
		}
	}
	if len(texts) == 0 {
		return nil, nil
	}
	vecs, err := provider.Embed(context.Background(), texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("embed: ได้ %d vectors จาก %d chunks", len(vecs), len(texts))
	}
	dim := 0
	rows := make([]index.EmbRow, len(vecs))
	for i, v := range vecs {
		if dim == 0 {
			dim = len(v)
		} else if len(v) != dim {
			return nil, fmt.Errorf("embed: chunk %d มี dim %d ไม่ตรง %d — ตอบไม่สม่ำเสมอ", i, len(v), dim)
		}
		rows[i] = index.EmbRow{ID: order[i].id, ChunkIdx: order[i].idx, Vec: vec.Encode(v)}
	}
	return rows, nil
}

// cmdEmbed routes the embed subcommand family.
func cmdEmbed(e *Env, c command, argv []string) int {
	if len(argv) == 0 {
		printCommandHelp(e, c, e.Stderr)
		return ExitGuard
	}
	switch argv[0] {
	case "status":
		return e.embedStatus(c, argv[1:])
	case "set":
		return e.embedSet(c, argv[1:])
	default:
		fmt.Fprintf(e.Stderr, "mem embed: ไม่รู้จัก %q (ใช้ status หรือ set)\n", argv[0])
		printCommandHelp(e, c, e.Stderr)
		return ExitGuard
	}
}

// embedStatus prints the effective embedding config, the index's embedding
// state and (when ollama is selected) whether the server answers.
func (e *Env) embedStatus(c command, argv []string) int {
	fs := e.flagSet(c)
	asJSON := fs.Bool("json", false, "พิมพ์เป็น JSON document เดียว")
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

	reachable := ""
	var probeDim int
	if p, ok := embed.NewFromConfig(cfg).(*embed.Ollama); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if perr := p.Ping(ctx); perr != nil {
			reachable = "no (" + firstLine(perr.Error()) + ")"
		} else {
			reachable = "yes"
			probeDim = p.Dim()
		}
	}

	embModel, embDim, embCount, hasEmb := "", 0, 0, false
	if ix, ierr := index.Open(index.IndexPath(root)); ierr == nil {
		if m, d, n, ok, err := ix.EmbMeta(); err == nil && ok {
			embModel, embDim, embCount, hasEmb = m, d, n, true
		}
		_ = ix.Close()
	}
	recCount := 0
	if recs, serr := e.scan(root); serr == nil {
		recCount = len(recs)
	}

	if *asJSON {
		payload := map[string]any{
			"provider": cfg.Provider,
			"model":    cfg.Model,
			"base_url": cfg.BaseURL,
			"dim":      probeDim,
			"index": map[string]any{
				"records":    recCount,
				"has_emb":    hasEmb,
				"emb_model":  embModel,
				"emb_dim":    embDim,
				"emb_chunks": embCount,
			},
		}
		if reachable != "" {
			payload["ollama_reachable"] = reachable == "yes"
		}
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			return e.fail(err)
		}
		return ExitOK
	}

	fmt.Fprintf(e.Stdout, "provider: %s\n", cfg.Provider)
	fmt.Fprintf(e.Stdout, "model:    %s\n", cfg.Model)
	fmt.Fprintf(e.Stdout, "base_url: %s\n", cfg.BaseURL)
	if probeDim > 0 {
		fmt.Fprintf(e.Stdout, "dim:      %d (probed)\n", probeDim)
	} else if embDim > 0 {
		fmt.Fprintf(e.Stdout, "dim:      %d (index)\n", embDim)
	} else {
		fmt.Fprintln(e.Stdout, "dim:      unknown (ยังไม่เคย embed)")
	}
	if cfg.Enabled() {
		fmt.Fprintf(e.Stdout, "ollama:   %s\n", reachable)
	}
	fmt.Fprintf(e.Stdout, "index:    %d records · ", recCount)
	if hasEmb {
		fmt.Fprintf(e.Stdout, "emb %s · %d chunks\n", embModel, embCount)
	} else {
		fmt.Fprintln(e.Stdout, "emb ไม่มี (off หรือยังไม่ reindex)")
	}
	if cfg.Enabled() && !hasEmb {
		fmt.Fprintln(e.Stdout, "ต่อไป: รัน `mem reindex` เพื่อสร้าง dense index")
	}
	return ExitOK
}

// embedSet rewrites the taxonomy.json embedding block: `set off` removes the
// block entirely (absent = off), `set ollama` writes it with the chosen
// model/base URL. The write is atomic and preceded by a .bak-<ts> backup of
// the previous file.
func (e *Env) embedSet(c command, argv []string) int {
	fs := e.flagSet(c)
	model := fs.String("model", "", "embedding model (default nomic-embed-text)")
	baseURL := fs.String("base-url", "", "ollama base URL (default http://127.0.0.1:11434)")
	args, code, stop := e.parseArgs(c, fs, argv)
	if stop {
		return code
	}
	if code, stop := e.needArgs(len(args), 1, "mode"); stop {
		return code
	}
	mode := strings.ToLower(strings.TrimSpace(args[0]))
	if mode != embed.ModeOff && mode != embed.ModeOllama {
		fmt.Fprintf(e.Stderr, "mem embed set: mode ต้องเป็น off หรือ ollama ไม่ใช่ %q\n", args[0])
		return ExitGuard
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	taxPath := filepath.Join(root, "schema", "taxonomy.json")

	old, err := loadEmbeddingBlock(taxPath)
	if err != nil {
		return e.fail(err)
	}

	var block *embed.Config
	if mode == embed.ModeOllama {
		b := embed.Config{Provider: embed.ModeOllama, Model: *model, BaseURL: *baseURL}.WithDefaults()
		block = &b
	}

	if err := writeEmbeddingBlock(taxPath, block); err != nil {
		return e.fail(err)
	}

	// Diff-style summary of what changed.
	fmt.Fprintf(e.Stdout, "%s:\n", filepath.Join("schema", "taxonomy.json"))
	newCfg := embed.Config{Provider: embed.ModeOff}.WithDefaults()
	if block != nil {
		newCfg = *block
	}
	if old.Provider != newCfg.Provider {
		fmt.Fprintf(e.Stdout, "  provider: %s -> %s\n", embOrDash(old.Provider), newCfg.Provider)
	}
	if old.Model != newCfg.Model {
		fmt.Fprintf(e.Stdout, "  model:    %s -> %s\n", embOrDash(old.Model), newCfg.Model)
	}
	if old.BaseURL != newCfg.BaseURL {
		fmt.Fprintf(e.Stdout, "  base_url: %s -> %s\n", embOrDash(old.BaseURL), newCfg.BaseURL)
	}
	if newCfg.Enabled() {
		fmt.Fprintf(e.Stdout, "ต่อไป: `mem reindex` สร้าง dense index (ต้องมี ollama กับ model %s ในเครื่อง)\n", newCfg.Model)
	} else {
		fmt.Fprintln(e.Stdout, "ต่อไป: `mem reindex` ลบ emb table ออกจาก index (off mode)")
	}
	return ExitOK
}

func embOrDash(s string) string {
	if s == "" {
		return "(ไม่มี)"
	}
	return s
}

// loadEmbeddingBlock reads the current embedding block of a taxonomy.json
// (zero Config when absent).
func loadEmbeddingBlock(path string) (embed.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return embed.Config{}, fmt.Errorf("embed: อ่าน %s: %w", path, err)
	}
	var doc struct {
		Embedding *embed.Config `json:"embedding"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return embed.Config{}, fmt.Errorf("embed: parse %s: %w", path, err)
	}
	if doc.Embedding == nil {
		return embed.Config{}, nil
	}
	return *doc.Embedding, nil
}

// writeEmbeddingBlock rewrites taxonomy.json with the embedding block set
// (block != nil) or removed (block == nil), preserving the document order and
// bytes of every other member. A timestamped backup of the previous file is
// written first; the replacement itself is atomic.
func writeEmbeddingBlock(path string, block *embed.Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("embed: อ่าน %s: %w", path, err)
	}
	members, order, err := splitJSONObject(data)
	if err != nil {
		return fmt.Errorf("embed: parse %s: %w", path, err)
	}
	if block != nil {
		raw, merr := json.MarshalIndent(block, "", "  ")
		if merr != nil {
			return fmt.Errorf("embed: encode block: %w", merr)
		}
		if _, exists := members["embedding"]; !exists {
			order = append(order, "embedding")
		}
		members["embedding"] = json.RawMessage(reindent(raw))
	} else {
		delete(members, "embedding") // the writer skips keys missing from members
	}

	if _, serr := os.Stat(path); serr == nil {
		if berr := store.AtomicWrite(backupPath(path, time.Now()), data); berr != nil {
			return fmt.Errorf("embed: เขียน backup: %w", berr)
		}
	}

	var b strings.Builder
	b.WriteString("{\n")
	first := true
	for _, k := range order {
		v, ok := members[k]
		if !ok {
			continue
		}
		if !first {
			b.WriteString(",\n")
		}
		first = false
		fmt.Fprintf(&b, "  %q: %s", k, v)
	}
	b.WriteString("\n}\n")
	return store.AtomicWrite(path, []byte(b.String()))
}

// backupPath renders the .bak-<timestamp> sibling of a taxonomy path.
func backupPath(path string, now time.Time) string {
	return path + ".bak-" + now.Format("20060102T150405")
}

// reindent shifts a marshaled block two spaces right so it sits at depth 1
// beside the other taxonomy members.
func reindent(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// splitJSONObject decodes a JSON object into its raw members plus the key
// order as written, so a rewrite can preserve the original layout.
func splitJSONObject(data []byte) (map[string]json.RawMessage, []string, error) {
	members := map[string]json.RawMessage{}
	var order []string
	dec := json.NewDecoder(strings.NewReader(string(data)))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, fmt.Errorf("ต้องเป็น JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("object key ไม่ใช่ string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		if _, dup := members[key]; !dup {
			order = append(order, key)
		}
		members[key] = raw
	}
	if _, err := dec.Token(); err != nil { // closing }
		return nil, nil, err
	}
	return members, order, nil
}
