package domain

import (
	"strings"
	"testing"

	"github.com/atomsbaza/my-memory-engine/internal/codec"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

func TestRenderContextKnapsack3Tier(t *testing.T) {
	recs := []store.Record{
		{ID: "rule.r1", Rel: "rules/r1.md", Layer: "rule", Scope: "global", Status: "active", Body: "always verify", Front: codec.Frontmatter{"title": "Verify"}},
		{ID: "handoff.h1", Rel: "handoffs/h1.md", Layer: "handoff", Scope: "global", Status: "active", Expires: "none", Body: "current task", Front: codec.Frontmatter{"title": "Handoff"}},
		{ID: "lesson.l1", Rel: "lessons/l1.md", Layer: "lesson", Scope: "global", Status: "active", Body: "short high-value lesson\n**NOT.** bad", Front: codec.Frontmatter{"title": "Short", "confidence": 0.9, "created": "2026-11-26", "last_used": "2026-11-26"}},
		{ID: "lesson.l2", Rel: "lessons/l2.md", Layer: "lesson", Scope: "global", Status: "active", Body: strings.Repeat("verbose filler ", 180) + "\n**NOT.** bad", Front: codec.Frontmatter{"title": "Verbose", "confidence": 0.9, "created": "2026-11-26", "last_used": "2026-11-26"}},
	}

	out, _, err := RenderContext(recs, ContextOptions{
		Scope:    "global",
		Query:    "lesson",
		Budget:   2600,
		PackMode: "knapsack",
	})
	if err != nil {
		t.Fatalf("RenderContext knapsack: %v", err)
	}
	if !strings.Contains(out, "Rules (MUST follow)") || !strings.Contains(out, "Working State") || !strings.Contains(out, "Dynamic Pack (Knapsack)") {
		t.Fatalf("missing 3-tier sections:\n%s", out)
	}
	if !strings.Contains(out, "lesson.l1") {
		t.Fatalf("high-density short lesson.l1 should be packed:\n%s", out)
	}
	if strings.Contains(out, "lesson.l2") {
		t.Fatalf("oversized low-density lesson.l2 should be skipped in tight budget:\n%s", out)
	}
}

func TestRenderContextWithExternalPointers(t *testing.T) {
	recs := []store.Record{
		{ID: "rule.r1", Rel: "rules/r1.md", Layer: "rule", Scope: "global", Status: "active", Body: "always verify", Front: codec.Frontmatter{"title": "Verify"}},
	}
	out, _, err := RenderContext(recs, ContextOptions{
		Scope:  "global",
		Query:  "oauth",
		Budget: 4000,
		ExternalHits: []ExternalHit{
			{Vault: "obsidian", Path: "Auth/OAuth.md", Heading: "PKCE", Snippet: "Use PKCE for public clients"},
		},
	})
	if err != nil {
		t.Fatalf("RenderContext with external: %v", err)
	}
	if !strings.Contains(out, "External Vault") || !strings.Contains(out, "Auth/OAuth.md") {
		t.Fatalf("missing External Vault section:\n%s", out)
	}
}
