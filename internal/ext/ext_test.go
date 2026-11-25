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
