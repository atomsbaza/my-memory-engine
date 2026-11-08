// Output formats: each harness accepts a different shape on stdout, and
// printing the wrong one is silent data loss (gemini treats a plain-text line
// as a parse failure; hermes ignores anything but {"context": …}).
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Emit renders one hook payload in the harness's contract.
//
//	text   : stdout as-is (claude, codex)
//	json   : hookSpecificOutput.additionalContext (gemini)
//	hermes : {"context": …} (hermes pre_llm_call)
//	cursor : {"additional_context": …}, plus continue:true for beforeSubmitPrompt
//
// Empty text emits nothing, except cursor's beforeSubmitPrompt, which must
// answer continue:true or the turn may end.
func Emit(out io.Writer, format Format, eventName, text string) {
	if strings.TrimSpace(text) == "" {
		if format == FormatCursor && IsBeforeSubmit(eventName) {
			writeJSON(out, map[string]any{"continue": true})
		}
		return
	}
	switch format {
	case FormatJSON:
		writeJSON(out, map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     eventName,
				"additionalContext": text,
			},
		})
	case FormatHermes:
		writeJSON(out, map[string]any{"context": text})
	case FormatCursor:
		payload := map[string]any{"additional_context": text}
		if IsBeforeSubmit(eventName) {
			payload["continue"] = true
		}
		writeJSON(out, payload)
	default:
		// Python print(text): one trailing newline on top of whatever the
		// block already carries, so the byte shape matches the baseline.
		fmt.Fprintln(out, text)
	}
}

// IsBeforeSubmit reports whether eventName is cursor's prompt hook, the one
// event whose reply must carry continue.
func IsBeforeSubmit(eventName string) bool {
	switch strings.ToLower(eventName) {
	case "beforesubmitprompt", "beforesubmit":
		return true
	}
	return false
}

// writeJSON emits one JSON document without HTML escaping (Python
// json.dumps(ensure_ascii=False) parity).
func writeJSON(out io.Writer, v any) {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "mem: hook emit: %v\n", err)
	}
}
