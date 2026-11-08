// Home-level helpers shared by the subcommands: scanning, schema, the derived
// index seam, and the append-only recall log under ops/.
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/schema"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// today is the ISO date used for last_used/created (Python TODAY).
func today() string { return time.Now().Format("2006-01-02") }

// nowStamp is the RFC3339 timestamp written into recall-log rows.
func nowStamp() string { return time.Now().Format(time.RFC3339) }

// scan reads every record under root (skip rules apply: ops/ append-only and
// "_" prefixed paths are not records).
func (e *Env) scan(root string) ([]store.Record, error) {
	recs, err := store.Scan(root)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	return recs, nil
}

// schemaOf loads the effective schema of the home.
func (e *Env) schemaOf(root string) (schema.Schema, error) {
	sch, err := schema.LoadSchema(root)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("schema %s: %w", root, err)
	}
	return sch, nil
}

// ftsSeam opens the derived index and returns its search function plus a close
// hook. With no index file the seam is nil (the Python "no index file" state:
// selection still works from the lexical vector ranks) unless rebuild is set,
// in which case the index is rebuilt first.
func (e *Env) ftsSeam(root string, rebuild bool) (func(string, int) ([]string, error), func(), error) {
	path := index.IndexPath(root)
	if _, err := os.Stat(path); err != nil {
		if !rebuild {
			return nil, func() {}, nil
		}
		if rerr := domain.ReindexRoot(root); rerr != nil {
			return nil, nil, rerr
		}
	}
	ix, err := index.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("index %s: %w", path, err)
	}
	return ix.Search, func() { _ = ix.Close() }, nil
}

// contextRequest is the shared input of `mem context` and the session-start
// hook (Python run_cmd(cmd_context) reuse).
type contextRequest struct {
	Scope  string
	Query  string
	Budget int
}

// contextText renders the memory block for one scope. It returns the rendered
// text and the records the renderer surfaced (the caller decides whether to
// touch them).
func (e *Env) contextText(root string, o contextRequest) (string, []store.Record, error) {
	recs, err := e.scan(root)
	if err != nil {
		return "", nil, err
	}
	sch, err := e.schemaOf(root)
	if err != nil {
		return "", nil, err
	}
	fts, closer, err := e.ftsSeam(root, false)
	if err != nil {
		return "", nil, err
	}
	defer closer()
	scoring := sch.Scoring
	text, touched, err := domain.RenderContext(recs, domain.ContextOptions{
		Root:    root,
		Scope:   o.Scope,
		Query:   o.Query,
		Budget:  o.Budget,
		Scoring: &scoring,
		FTS:     fts,
		Indexed: fts != nil,
	})
	if err != nil {
		return "", nil, err
	}
	return text, touched, nil
}

// logEvent appends one JSONL row to ops/recall-log.jsonl. Callers decide how
// loud a failure is: the hook fail-opens (REQ-HOOK-02) while a CLI command
// reports it on stderr without changing its exit code. The writer itself is
// domain.LogEvent so the MCP entrypoint records byte-identical rows.
func logEvent(root string, row map[string]any) error {
	return domain.LogEvent(root, row)
}

// appendRuntimeLog writes one observable fail-open line under .memory/ (the
// gitignored runtime log). It never returns an error: the caller is already in
// a failure path and must not lose the user's work over its own logging
// (REQ-HOOK-02).
func appendRuntimeLog(root, line string) {
	_ = store.LogRuntime(root, nowStamp()+" "+line)
}

// agentEnv is the harness identity used in log rows (Python MEM_AGENT).
func agentEnv() string {
	if v := os.Getenv("MEM_AGENT"); v != "" {
		return v
	}
	return "-"
}

// runeLen mirrors Python len() over str for the char counters in log rows.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// firstLine returns the first line of s, trimmed (error/stderr summaries).
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
