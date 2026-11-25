package ext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExternalVaultAddListReindexSearchRemove(t *testing.T) {
	home := t.TempDir()
	vault := t.TempDir()

	// Create sample markdown notes (including Thai content and nested headings).
	if err := os.MkdirAll(filepath.Join(vault, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".obsidian", "ignored.md"), []byte("# Secret\nshould be skipped"), 0o644); err != nil {
		t.Fatal(err)
	}
	note := "---\ntags: [arch]\n---\n# ระบบความจำ\n\n## การค้นหาภาษาไทย\nใช้ FTS5 ร่วมกับตัวตัดคำไทยเพื่อค้นหาความจำระยะยาว\n\n## Latency\nต้องไม่เกิน 150ms\n"
	if err := os.WriteFile(filepath.Join(vault, "Projects", "memory.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddVault(home, "obsidian", vault); err != nil {
		t.Fatalf("AddVault: %v", err)
	}
	cfg, err := LoadConfig(home)
	if err != nil || len(cfg.Vaults) != 1 || cfg.Vaults[0].Name != "obsidian" {
		t.Fatalf("LoadConfig = %+v, err=%v", cfg, err)
	}

	res, err := Reindex(home, "")
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if res.Files != 1 || res.Chunks < 2 {
		t.Fatalf("Reindex = %+v, want 1 file and >=2 chunks", res)
	}

	hits, err := Search(home, "ความจำ", "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("Search(ความจำ) returned 0 hits")
	}
	if hits[0].Vault != "obsidian" || hits[0].RelPath != filepath.ToSlash(filepath.Join("Projects", "memory.md")) {
		t.Fatalf("unexpected hit[0]: %+v", hits[0])
	}
	if hits[0].Heading == "" {
		t.Fatalf("expected non-empty heading on hit[0]: %+v", hits[0])
	}

	// Ensure .obsidian was ignored.
	hiddenHits, err := Search(home, "skipped", "", 5)
	if err != nil {
		t.Fatalf("Search hidden: %v", err)
	}
	if len(hiddenHits) != 0 {
		t.Fatalf("hidden dir .obsidian should not be indexed, got: %+v", hiddenHits)
	}

	removed, err := RemoveVault(home, "obsidian")
	if err != nil || !removed {
		t.Fatalf("RemoveVault = %v, %v", removed, err)
	}
	cfg, err = LoadConfig(home)
	if err != nil || len(cfg.Vaults) != 0 {
		t.Fatalf("after remove LoadConfig = %+v, err=%v", cfg, err)
	}
}

func TestExtReindexIncremental(t *testing.T) {
	home := t.TempDir()
	vault := t.TempDir()
	p1 := filepath.Join(vault, "a.md")
	p2 := filepath.Join(vault, "b.md")
	if err := os.WriteFile(p1, []byte("# Note A\nalpha content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, []byte("# Note B\nbravo content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddVault(home, "v", vault); err != nil {
		t.Fatal(err)
	}

	r1, err := Reindex(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if r1.Updated != 2 || r1.Skipped != 0 {
		t.Fatalf("r1 = %+v, want Updated=2 Skipped=0", r1)
	}

	// Second pass: no file changed -> all skipped.
	r2, err := Reindex(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if r2.Skipped != 2 || r2.Updated != 0 || r2.Deleted != 0 {
		t.Fatalf("r2 = %+v, want Skipped=2 Updated=0 Deleted=0", r2)
	}

	// Update a.md and delete b.md.
	if err := os.WriteFile(p1, []byte("# Note A\nupdated zeta content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p2); err != nil {
		t.Fatal(err)
	}

	r3, err := Reindex(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if r3.Updated != 1 || r3.Deleted != 1 {
		t.Fatalf("r3 = %+v, want Updated=1 Deleted=1", r3)
	}
	if hits, err := Search(home, "zeta", "", 5); err != nil || len(hits) != 1 {
		t.Fatalf("search zeta = %+v, %v", hits, err)
	}
	if hits, err := Search(home, "bravo", "", 5); err != nil || len(hits) != 0 {
		t.Fatalf("deleted bravo still found = %+v, %v", hits, err)
	}
}
