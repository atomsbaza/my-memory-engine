// Harness adapters: the six harness entries that wire my-memory hooks. Each
// entry declares the output format it reads and the event names it uses, so a
// harness config can omit --format/--event-name and still get the right
// contract (AC-P2-10: six entries, extraction per harness, not one shared key).
package hook

import "strings"

// Harness is one adapter entry.
type Harness struct {
	// Name is the --agent value and the registry key.
	Name string
	// Format is the output contract the harness reads.
	Format Format
	// Events maps an engine event to the harness's event key. A missing entry
	// means the harness does not wire that event (gemini has no Stop hook,
	// hermes has no session-start hook).
	Events map[Event]string
	// PromptKeys are the harness-specific payload fields, tried before the
	// shared key list. They carry the shapes a harness actually sends.
	PromptKeys []string
	// SessionKeys are the harness-specific session fields, tried first.
	SessionKeys []string
}

// EventName returns the harness event key for e ("" when unwired).
func (h Harness) EventName(e Event) string {
	return h.Events[e]
}

// harnesses is the frozen adapter registry. Adding a harness is one entry
// here, or a declarative entry in schema/agents.json once it is installed.
var harnesses = []Harness{
	{
		Name:   "claude",
		Format: FormatText,
		Events: map[Event]string{
			SessionStart: "SessionStart",
			UserPrompt:   "UserPromptSubmit",
			Stop:         "Stop",
		},
		PromptKeys:  []string{"prompt"},
		SessionKeys: []string{"session_id"},
	},
	{
		Name:   "codex",
		Format: FormatText,
		Events: map[Event]string{
			SessionStart: "SessionStart",
			UserPrompt:   "UserPromptSubmit",
			Stop:         "Stop",
		},
		PromptKeys:  []string{"prompt"},
		SessionKeys: []string{"session_id"},
	},
	{
		Name:   "gemini",
		Format: FormatJSON,
		// gemini forces stdout to be pure JSON; BeforeAgent is its prompt hook.
		Events: map[Event]string{
			SessionStart: "SessionStart",
			UserPrompt:   "BeforeAgent",
		},
		PromptKeys:  []string{"prompt"},
		SessionKeys: []string{"session_id"},
	},
	{
		Name:   "opencode",
		Format: FormatText,
		// opencode has no shell hook; its plugin forwards the same events.
		Events: map[Event]string{
			SessionStart: "session-start",
			UserPrompt:   "user-prompt",
			Stop:         "stop",
		},
		PromptKeys:  []string{"prompt"},
		SessionKeys: []string{"session_id"},
	},
	{
		Name:   "cursor-agent",
		Format: FormatCursor,
		Events: map[Event]string{
			SessionStart: "sessionStart",
			UserPrompt:   "beforeSubmitPrompt",
			Stop:         "stop",
		},
		PromptKeys:  []string{"prompt", "user_message"},
		SessionKeys: []string{"session_id", "conversation_id"},
	},
	{
		Name:   "hermes",
		Format: FormatHermes,
		// hermes injects through pre_llm_call only and commits on session end.
		Events: map[Event]string{
			UserPrompt: "pre_llm_call",
			Stop:       "on_session_end",
		},
		PromptKeys:  []string{"message"},
		SessionKeys: []string{"session_id"},
	},
}

// Harnesses returns a copy of the adapter registry in declaration order.
func Harnesses() []Harness {
	out := make([]Harness, len(harnesses))
	copy(out, harnesses)
	return out
}

// HarnessByName looks a harness up by its --agent value (case-insensitive).
func HarnessByName(name string) (Harness, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, h := range harnesses {
		if h.Name == name {
			return h, true
		}
	}
	return Harness{}, false
}
