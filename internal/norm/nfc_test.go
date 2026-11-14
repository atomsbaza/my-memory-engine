package norm

import (
	"strconv"
	"strings"
	"testing"
)

// golden vectors emitted by tools/gen-nfc-tables.py: each entry is
// (input runes, expected NFC runes) as comma-separated hex columns validated
// against unicodedata.normalize('NFC') at generation time (unidata 13.0.0).
var goldenNFC = [][2]string{
	{"0x0043, 0x0061, 0x0066, 0x00E9", "0x0043, 0x0061, 0x0066, 0x00E9"},                 // precomposed stays
	{"0x0043, 0x0061, 0x0066, 0x0065, 0x0301", "0x0043, 0x0061, 0x0066, 0x00E9"},         // e + acute composes
	{"0x0061, 0x0301, 0x0328", "0x0105, 0x0301"},                                         // reorder changes composition target
	{"0x0061, 0x0328, 0x0301", "0x0105, 0x0301"},                                         // same result through the other order
	{"0x0E01, 0x0E33, 0x0E19, 0x0E2D, 0x0E07", "0x0E01, 0x0E33, 0x0E19, 0x0E2D, 0x0E07"}, // Thai stable
	{"0x0E33, 0x0E33", "0x0E33, 0x0E33"},                                                 // Thai marks, no canonical pairs
	{"0x1100, 0x1161, 0x11A8", "0xAC01"},                                                 // Hangul jamo -> syllable
	{"0x00C5", "0x00C5"},                                                                 // singleton composition result
	{"0xFB01, 0x006E, 0x0065", "0xFB01, 0x006E, 0x0065"},                                 // compatibility ligature untouched
	{"0x0065, 0x0301, 0x0065, 0x0301", "0x00E9, 0x00E9"},                                 // repeated composition
	{"0x0041, 0x030A, 0x0042, 0x0323", "0x00C5, 0x1E04"},                                 // two composable pairs stacked
}

func runes(t *testing.T, hex string) []rune {
	t.Helper()
	var out []rune
	for _, f := range strings.Split(hex, ", ") {
		v, err := strconv.ParseUint(f, 0, 32)
		if err != nil {
			t.Fatalf("bad hex column %q: %v", f, err)
		}
		out = append(out, rune(v))
	}
	return out
}

func TestGoldenNFC(t *testing.T) {
	for i, g := range goldenNFC {
		in := string(runes(t, g[0]))
		want := string(runes(t, g[1]))
		if got := NFC(in); got != want {
			t.Errorf("vector %d: NFC(%q) = %q, want %q", i, in, got, want)
		}
	}
}

func TestNFCASCIIFastPath(t *testing.T) {
	// The fast path must return the exact input (aliasing, no rewrite).
	s := "plain ascii body\nsecond line\n"
	if got := NFC(s); got != s {
		t.Errorf("ASCII input was rewritten: %q", got)
	}
	if NFC("") != "" {
		t.Error("empty string changed")
	}
}
