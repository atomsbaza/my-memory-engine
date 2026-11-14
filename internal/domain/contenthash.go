// content_sha256 — idempotent ingest (KB-patterns P1): every record carries a
// hash of its *normalized* body so re-adding byte-equivalent content is a
// no-op success instead of a dedup-block conflict or a second file. The hash
// ignores everything machines disagree about (NFC form, CRLF, trailing
// spaces, over-blank lines) but nothing semantic.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/norm"
)

// NormalizeBody canonicalises a record body for hashing:
//
//  1. NFC unicode form — the same text from any editor/normalizer hashes equal
//     (see internal/norm, validated against Python unicodedata);
//  2. CRLF/CR line endings to LF;
//  3. trailing spaces/tabs stripped per line;
//  4. runs of 3+ blank lines collapsed to one blank line (editor churn);
//  5. overall trim.
//
// Content between the lines survives untouched — this must never change
// meaning, only presentation.
func NormalizeBody(body string) string {
	s := norm.NFC(body)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blanks++
			continue
		}
		switch {
		case blanks == 1, blanks == 2: // one or two blank lines survive as-is
			for range blanks {
				out = append(out, "")
			}
		case blanks >= 3: // 3+ blank lines collapse to one (editor churn)
			out = append(out, "")
		}
		blanks = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// contentHashHex is the shape a stored content_sha256 must have.
var contentHashHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidContentSHA reports whether v is a well-formed content hash value.
func ValidContentSHA(v string) bool { return contentHashHex.MatchString(v) }

// ContentSHA256 returns the hex sha256 over NormalizeBody(body).
func ContentSHA256(body string) string {
	sum := sha256.Sum256([]byte(NormalizeBody(body)))
	return hex.EncodeToString(sum[:])
}
