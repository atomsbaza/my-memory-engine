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
	in := "## หัวข้อ: ค้นหาความจำ quick-sort_2"
	got := SegmentForFTS(in)
	want := "## หัวข้อ: ค้นหา ความจำ quick-sort_2"
	if got != want {
		t.Fatalf("SegmentForFTS = %q, want %q", got, want)
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
