// Recall-log append — Python log_event(). The row writer lives in domain
// because every entrypoint (CLI, hook, MCP) records the same events into the
// same append-only file; keeping one implementation is what makes the log a
// single signal instead of three drifting formats.
package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RecallLogRel is the append-only injection/usage log (Python log_event):
// ops/ has file_mode append-only, so it is not a record layer.
const RecallLogRel = "ops/recall-log.jsonl"

// LogEvent appends one JSONL row to ops/recall-log.jsonl under root.
func LogEvent(root string, row map[string]any) error {
	path := filepath.Join(root, filepath.FromSlash(RecallLogRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("recall log: %w", err)
	}
	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("recall log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("recall log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("recall log: %w", err)
	}
	return nil
}
