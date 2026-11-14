// Behaviour tests for content_sha256 idempotent ingest on the CLI surface
// (KB P1): re-adding the same body is an exit-0 success naming the twin,
// whitespace-only differences still collide, real changes write, cross-scope
// duplicates write with a warning.
package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// countMarkdownFiles counts the record files under home (git worktree files
// included, _archive excluded).
func countMarkdownFiles(t *testing.T, home string) int {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, "*", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range paths {
		if !strings.Contains(p, "_archive") {
			n++
		}
	}
	return n
}

const idemBody = "idempotent ingest body for the CLI test\n" +
	"second line with enough words to be a real fact\n" +
	"(synthetic fixture — provenance: test-generated, not corpus-derived)"

func TestAddIdempotentCLI(t *testing.T) {
	home := fixtureHome(t)

	add := func(stdin, scope string) (int, string, string) {
		return runHome(t, home, stdin, "add", "--layer", "lesson", "--scope", scope,
			"--title", "idempotent cli test", "--tag", "test", "--file", "-")
	}

	code, out, stderr := add(idemBody, "global")
	mustCode(t, ExitOK, code, out, stderr, "first add")
	id := strings.Split(out, "\t")[0]
	rel := strings.Split(strings.TrimSpace(out), "\t")[1]
	before := countMarkdownFiles(t, home)

	// Identical body: exit 0, the Thai message names id + file, no file.
	code, out, stderr = add(idemBody, "global")
	mustCode(t, ExitOK, code, out, stderr, "re-add identical")
	if !strings.HasPrefix(out, "content ซ้ำเป๊ะ (sha256 ") {
		t.Errorf("idempotent message wrong: %q", out)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, rel) {
		t.Errorf("message must name the existing record and file: %q", out)
	}
	if countMarkdownFiles(t, home) != before {
		t.Errorf("idempotent re-add wrote a file: %d -> %d", before, countMarkdownFiles(t, home))
	}

	// Whitespace-only differences still collide (normalization).
	stdin := strings.ReplaceAll(idemBody, "\n", "\r\n")
	code, out, stderr = add(stdin, "global")
	mustCode(t, ExitOK, code, out, stderr, "re-add CRLF variant")
	if !strings.HasPrefix(out, "content ซ้ำเป๊ะ (sha256 ") {
		t.Errorf("CRLF variant was not dedup-ed: %q", out)
	}

	stdin = strings.ReplaceAll(idemBody, "test\n", "test   \n") // trailing spaces
	code, out, stderr = add(stdin, "global")
	mustCode(t, ExitOK, code, out, stderr, "re-add trailing-space variant")
	if !strings.HasPrefix(out, "content ซ้ำเป๊ะ (sha256 ") {
		t.Errorf("trailing-space variant was not dedup-ed: %q", out)
	}

	// Same content, different scope: written, plus a warning.
	code, out, stderr = add(idemBody, "project:IdemProj")
	mustCode(t, ExitOK, code, out, stderr, "cross-scope add")
	id2 := strings.Split(out, "\t")[0]
	if strings.HasPrefix(out, "content ซ้ำเป๊ะ") {
		t.Fatal("cross-scope add was silently dedup-ed")
	}
	if id2 == id {
		t.Fatalf("cross-scope add landed on the same id %s", id2)
	}
	warned := false
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, id) && strings.Contains(l, "content_sha256") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("cross-scope duplication earned no warning (stderr: %q)", stderr)
	}
	if countMarkdownFiles(t, home) != before+1 {
		t.Errorf("cross-scope add file count = %d, want %d", countMarkdownFiles(t, home), before+1)
	}

	// A real body change still writes.
	changed := idemBody + "\nthird fact line that changes the content"
	code, out, stderr = add(changed, "global")
	mustCode(t, ExitOK, code, out, stderr, "changed body add")
	if strings.HasPrefix(out, "content ซ้ำเป๊ะ") {
		t.Fatal("changed body was dedup-ed")
	}
	if strings.Split(out, "\t")[0] == id {
		t.Error("changed body reused the old id")
	}
	if countMarkdownFiles(t, home) != before+2 {
		t.Errorf("changed add file count = %d, want %d", countMarkdownFiles(t, home), before+2)
	}
}
