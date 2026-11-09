// vec package tests: the little-endian float32 blob codec and cosine
// similarity, including the mismatched-dim rejection.
package vec

import (
	"math"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := []float32{0.5, -1.25, 3, 0, 1e-7}
	blob := Encode(in)
	if len(blob) != 4*len(in) {
		t.Fatalf("blob len = %d, want %d", len(blob), 4*len(in))
	}
	out, err := Decode(blob)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if math.Float32bits(in[i]) != math.Float32bits(out[i]) {
			t.Fatalf("round trip: in[%d]=%v out[%d]=%v", i, in[i], i, out[i])
		}
	}
}

func TestDecodeRejectsBadLength(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3}); err == nil || !strings.Contains(err.Error(), "float32") {
		t.Fatalf("bad length: err = %v", err)
	}
}

func TestCosineDeterministicVectors(t *testing.T) {
	// 4-dim probes: identical → 1, orthogonal → 0, opposite → -1.
	cases := []struct {
		a, b []float32
		want float64
	}{
		{[]float32{1, 0, 0, 0}, []float32{1, 0, 0, 0}, 1},
		{[]float32{1, 0, 0, 0}, []float32{0, 1, 0, 0}, 0},
		{[]float32{1, 0, 0, 0}, []float32{-1, 0, 0, 0}, -1},
		{[]float32{1, 1, 0, 0}, []float32{1, 0, 0, 0}, math.Sqrt2 / 2},
	}
	for _, c := range cases {
		got, err := Cosine(c.a, c.b)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("cosine(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	// Zero vector: similarity 0, not NaN.
	if got, err := Cosine([]float32{0, 0, 0, 0}, []float32{1, 0, 0, 0}); err != nil || got != 0 {
		t.Fatalf("zero vector: %v %v", got, err)
	}
}

func TestCosineRejectsDimMismatch(t *testing.T) {
	if _, err := Cosine([]float32{1, 0}, []float32{1, 0, 0}); err == nil || !strings.Contains(err.Error(), "dim") {
		t.Fatalf("dim mismatch: err = %v", err)
	}
}
