package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 8, 15, 4, 5, 0, time.UTC) // .bak-20261108150405

// TestOrderedRoundTrip: key order, number literals and non-ASCII strings
// survive a load→save cycle byte-stable (Python dict parity).
func TestOrderedRoundTrip(t *testing.T) {
	src := `{"model":"sonnet","a":{"y":2.50,"x":[1,2,null,true,"éทดสอบ"]},"mcpServers":{}}`
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := loadJSONFile(path)
	if err != nil {
		t.Fatalf("loadJSONFile: %v", err)
	}
	root := asObj(v)
	if root == nil {
		t.Fatal("root is not an object")
	}
	if got, want := strings.Join(root.Keys(), ","), "model,a,mcpServers"; got != want {
		t.Errorf("top-level key order = %q, want %q", got, want)
	}
	inner := asObj(mapGet(root, "a"))
	if got, want := strings.Join(inner.Keys(), ","), "y,x"; got != want {
		t.Errorf("nested key order = %q, want %q", got, want)
	}
	if _, _, err := saveJSONFile(path, root, testNow); err != nil {
		t.Fatalf("saveJSONFile: %v", err)
	}
	first, _ := os.ReadFile(path)
	// Second cycle must be byte-identical — no churn on idempotent reruns.
	v2, err := loadJSONFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, _, err := saveJSONFile(path, v2, testNow); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("save not byte-stable:\nfirst:  %s\nsecond: %s", first, second)
	}
	if !strings.Contains(string(first), "2.50") {
		t.Errorf("number literal 2.50 not preserved verbatim: %s", first)
	}
	if bak, _ := os.ReadFile(path + ".bak-20261108150405"); len(bak) == 0 {
		t.Error("first save should have backed the original file up")
	}
}

// TestSetAppendsAndReplaces: new keys append, existing keys keep position.
func TestSetAppendsAndReplaces(t *testing.T) {
	o := newObj()
	o.Set("b", 1)
	o.Set("a", 2)
	o.Set("c", 3)
	o.Set("a", 9) // replace in place
	if got, want := strings.Join(o.Keys(), ","), "b,a,c"; got != want {
		t.Errorf("Keys() = %q, want %q", got, want)
	}
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"b":1,"a":9,"c":3}`; got != want {
		t.Errorf("marshal = %s, want %s", got, want)
	}
}

// TestSaveJSONFileSkipsUnchangedWrite: saving identical content writes
// nothing and creates no backup.
func TestSaveJSONFileSkipsUnchangedWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	o := newObj()
	o.Set("k", "v1")
	if _, _, err := saveJSONFile(path, o, testNow); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, wrote, err := saveJSONFile(path, o, testNow); err != nil || wrote {
		t.Fatalf("unchanged save: wrote=%v err=%v, want wrote=false err=nil", wrote, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("unchanged save modified the file")
	}
	if _, err := os.Stat(path + ".bak-" + testNow.Format(backupTimeFormat)); err == nil {
		t.Error("unchanged save created a backup")
	}
}

// TestLoadJSONFileRejectsTrailingGarbage: malformed documents error out
// instead of silently truncating user configs.
func TestLoadJSONFileRejectsTrailingGarbage(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"broken.json":   `{"a":`,
		"trailing.json": `{} extra`,
		"array.json":    `[1,2]`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadJSONFile(path); name == "array.json" {
			if err != nil {
				t.Errorf("%s: array document should load (as []any), got %v", name, err)
			}
		} else if err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
