// Payload extraction: every harness names the prompt and session fields
// differently, so the adapter tries its own keys first, then the shared list,
// at the top level and inside the wrapper objects harnesses nest them in
// (REQ-HOOK-01).
package hook

import (
	"encoding/json"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
)

// promptKeys is the shared field list, in the Python baseline's order. The
// harness-specific keys are prepended per adapter.
var promptKeys = []string{
	"prompt", "user_prompt", "userPrompt", "message", "instruction",
	"query", "text", "user_message", "userMessage",
}

// sessionKeys is the shared session field list.
var sessionKeys = []string{"session_id", "sessionId"}

// wrapperKeys are the objects harnesses wrap the payload in.
var wrapperKeys = []string{"extra", "payload", "event", "data", "input"}

// ExtractPrompt returns the user's request from a harness payload. A payload
// that is not JSON is taken as the raw request (truncated), matching the
// Python hook_payload_query fallback.
func ExtractPrompt(raw, harness string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return truncateRunes(raw, 400)
	}
	switch v := doc.(type) {
	case string:
		return truncateRunes(v, 400)
	case map[string]any:
		keys := mergeKeys(adapterKeys(harness, func(h Harness) []string { return h.PromptKeys }), promptKeys)
		if s := firstString(v, keys); s != "" {
			return truncateRunes(s, 1000)
		}
		for _, wrapper := range wrapperKeys {
			inner, ok := v[wrapper].(map[string]any)
			if !ok {
				continue
			}
			if s := firstString(inner, keys); s != "" {
				return truncateRunes(s, 1000)
			}
		}
	}
	return ""
}

// ExtractSession returns the harness session id, "-" when absent.
func ExtractSession(raw, harness string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "-"
	}
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "-"
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "-"
	}
	keys := mergeKeys(adapterKeys(harness, func(h Harness) []string { return h.SessionKeys }), sessionKeys)
	if s := sessionFrom(obj, keys); s != "" {
		return truncateRunes(s, 100)
	}
	for _, wrapper := range wrapperKeys {
		inner, ok := obj[wrapper].(map[string]any)
		if !ok {
			continue
		}
		if s := sessionFrom(inner, keys); s != "" {
			return truncateRunes(s, 100)
		}
	}
	return "-"
}

// mergeKeys returns adapter keys followed by the shared keys in a fresh slice,
// so a lookup can never mutate the registry's own slices.
func mergeKeys(adapter, shared []string) []string {
	out := make([]string, 0, len(adapter)+len(shared))
	out = append(out, adapter...)
	out = append(out, shared...)
	return out
}

// adapterKeys returns the harness-specific keys, empty for an unknown harness.
func adapterKeys(harness string, pick func(Harness) []string) []string {
	if h, ok := HarnessByName(harness); ok {
		return pick(h)
	}
	return nil
}

// sessionFrom reads a session id from the flat keys or a nested session.id.
func sessionFrom(obj map[string]any, keys []string) string {
	if s := firstString(obj, keys); s != "" {
		return s
	}
	if sess, ok := obj["session"].(map[string]any); ok {
		if s, ok := sess["id"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// firstString returns the first non-blank string value among keys.
func firstString(obj map[string]any, keys []string) string {
	for _, k := range keys {
		if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ContentTokens is the Python content_tokens: tokens minus stopwords. A
// greeting or a two-word fragment must not trigger a recall injection.
func ContentTokens(s string) map[string]bool {
	out := map[string]bool{}
	for tok := range domain.Tokens(s) {
		if !stopwords[tok] {
			out[tok] = true
		}
	}
	return out
}

// stopwordList is the Python STOPWORDS set.
var stopwordList = []string{
	"the", "a", "an", "is", "are", "was", "were", "be", "to", "of", "and", "or", "for", "in", "on",
	"at", "it", "this", "that", "with", "my", "me", "we", "you", "i", "do", "does", "did", "how",
	"what", "why", "when", "where", "can", "could", "should", "would", "please", "help", "need",
	"want", "let", "us", "if", "then", "so", "ok", "okay", "yes", "no", "not", "have", "has", "get",
	"และ", "หรือ", "คือ", "ที่", "การ", "ความ", "ให้", "ได้", "ไม่", "มี", "เป็น", "จาก", "กับ",
	"แล้ว", "นี้", "นั้น", "ต้อง", "ทำ", "ยังไง", "อย่างไร", "อะไร", "ช่วย", "ผม", "เรา", "ของ",
	"ใน", "บน", "ก็", "จะ", "ไป", "มา", "อยู่", "กัน", "ด้วย", "โดย", "แต่", "ถ้า", "เมื่อ", "อยาก",
}

var stopwords = func() map[string]bool {
	set := make(map[string]bool, len(stopwordList))
	for _, w := range stopwordList {
		set[w] = true
	}
	return set
}()

// truncateRunes caps s at n runes (Python len() counts code points).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
