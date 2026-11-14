// Package norm implements Unicode NFC normalization from vendored tables,
// with zero new module dependencies (AGENTS.md: pure Go, no CGO, no new deps
// — golang.org/x/text is out of scope for this repo).
//
// The tables (tables_nfc.go) are generated from Python unicodedata by
// tools/gen-nfc-tables.py, which also validates this exact algorithm against
// unicodedata.normalize('NFC') over every code point, every composition pair
// and a seeded random corpus before emitting them. content_sha256 needs NFC so
// the same body text hashes identically no matter which machine or normalizer
// produced it.
//
// Algorithm (UAX#15): full canonical decomposition (Hangul syllables
// algorithmic, UAX#15 §3.12), canonical ordering of combining runs, then
// blocked canonical composition.
package norm

// Hangul constants (UAX#15 §3.12).
const (
	hangulSBase  = 0xAC00
	hangulLBase  = 0x1100
	hangulVBase  = 0x1161
	hangulTBase  = 0x11A7
	hangulLCount = 19
	hangulVCount = 21
	hangulTCount = 28
	hangulNCount = hangulVCount * hangulTCount
	hangulSCount = hangulLCount * hangulNCount
)

// ccc returns the canonical combining class of r (0 = starter).
func ccc(r rune) uint8 {
	return CCC[r]
}

// decomposeRune appends r's full canonical decomposition to dst.
func decomposeRune(dst []rune, r rune) []rune {
	if r >= hangulSBase && r < hangulSBase+hangulSCount {
		s := r - hangulSBase
		dst = append(dst, hangulLBase+s/hangulNCount,
			hangulVBase+(s%hangulNCount)/hangulTCount)
		if t := s % hangulTCount; t != 0 {
			dst = append(dst, hangulTBase+t)
		}
		return dst
	}
	if d, ok := NFD[r]; ok {
		for _, c := range d {
			dst = append(dst, c)
		}
		return dst
	}
	return append(dst, r)
}

// reorder applies canonical ordering to combining runs: a stable insertion
// sort within each run of non-starters (UAX#15 §3.4, "Canonical Ordering
// Algorithm").
func reorder(r []rune) {
	for i := 1; i < len(r); i++ {
		c := r[i]
		if ccc(c) == 0 {
			continue
		}
		j := i
		for j > 0 {
			p := ccc(r[j-1])
			if p == 0 || p <= ccc(c) {
				break
			}
			r[j] = r[j-1]
			j--
		}
		r[j] = c
	}
}

// compose applies blocked canonical composition in place, returning the
// composed slice.
func compose(r []rune) []rune {
	if len(r) == 0 {
		return r
	}
	out := r[:1]
	starterPos := 0
	lastClass := ccc(r[0])
	for i := 1; i < len(r); i++ {
		ch := r[i]
		cl := ccc(ch)
		starter := out[starterPos]
		done := false
		if target, ok := COMP[compPair{starter, ch}]; ok && (lastClass < cl || lastClass == 0) {
			out[starterPos] = target
			done = true
		}
		if !done {
			// Hangul algorithmic composition: L + V -> LV, LV + T -> LVT.
			if starter >= hangulLBase && starter < hangulLBase+hangulLCount &&
				ch >= hangulVBase && ch < hangulVBase+hangulVCount {
				out[starterPos] = hangulSBase + (starter-hangulLBase)*hangulNCount +
					(ch-hangulVBase)*hangulTCount
				done = true
			} else if starter >= hangulSBase && starter < hangulSBase+hangulSCount &&
				(starter-hangulSBase)%hangulTCount == 0 &&
				ch > hangulTBase && ch < hangulTBase+hangulTCount {
				out[starterPos] = starter + (ch - hangulTBase)
				done = true
			}
		}
		if !done {
			if cl == 0 {
				starterPos = len(out)
			}
			lastClass = cl
			out = append(out, ch)
		}
	}
	return out
}

// NFC returns the Unicode normal form C of s. The function is pure: an ASCII
// input returns without any allocation, anything else allocates one []rune
// working copy.
func NFC(s string) string {
	ascii := true
	for i := range len(s) {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	dec := make([]rune, 0, len(s))
	for _, r := range s {
		dec = decomposeRune(dec, r)
	}
	reorder(dec)
	return string(compose(dec))
}
