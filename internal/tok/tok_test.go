package tok

import (
	"reflect"
	"testing"
)

func TestTokenizeBasicThaiAndMixed(t *testing.T) {
	got := Tokenize("Hello, HELLO quick-sort_2 ความจำระยะยาว")
	want := []string{"hello", "hello", "quick", "sort_2", "ความจำ", "ระยะยาว"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeMixedNoSpace(t *testing.T) {
	got := Tokenize("ค้นหาquick-sort_2ในระบบ")
	want := []string{"ค้นหา", "quick", "sort_2", "ใน", "ระบบ"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

func TestTokenizeUnknownThaiGrouped(t *testing.T) {
	// Consecutive unknown Thai runes must stay grouped as >1 rune token
	// instead of breaking into isolated 1-rune letters.
	got := Tokenize("ฅฅฆฆฒฒ")
	if len(got) != 1 || got[0] != "ฅฅฆฆฒฒ" {
		t.Fatalf("Tokenize unknown = %v, want single grouped token [ฅฅฆฆฒฒ]", got)
	}
	mixed := Tokenize("ค้นหาฅฅฆฆระบบ")
	want := []string{"ค้นหา", "ฅฅฆฆ", "ระบบ"}
	if !reflect.DeepEqual(mixed, want) {
		t.Fatalf("Tokenize mixed unknown = %v, want %v", mixed, want)
	}
}

func TestSegmentForFTS(t *testing.T) {
	in := "## หัวข้อ: ค้นหาความจำ quick-sort_2 ค้นหาQuick-Sort_2ในระบบ"
	got := SegmentForFTS(in)
	want := "## หัวข้อ: ค้นหา ความจำ quick-sort_2 ค้นหา Quick-Sort_2 ใน ระบบ"
	if got != want {
		t.Fatalf("SegmentForFTS = %q, want %q", got, want)
	}
}

func TestTokenizeDPMinimalSegments(t *testing.T) {
	// "ไปหามา" in greedy left-to-right could pick "ไปหา" + "มา" (2) or if
	// a prefix traps into 3 tokens; DP picks minimal segments ("ไปหา","มา" or "ไป","หามา").
	// Verify deterministic minimal segmentation (2 tokens, not 3).
	got := Tokenize("ไปหามา")
	if len(got) != 2 {
		t.Fatalf("Tokenize(ไปหามา) = %v (len %d), want 2 segments", got, len(got))
	}
}

func TestTokenizeEmpty(t *testing.T) {
	if got := Tokenize("   \n\t "); len(got) != 0 {
		t.Fatalf("Tokenize empty = %v, want nil/empty", got)
	}
	if got := SegmentForFTS(""); got != "" {
		t.Fatalf("SegmentForFTS empty = %q, want empty", got)
	}
}
