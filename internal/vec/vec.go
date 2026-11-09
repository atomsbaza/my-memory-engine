// Package vec is the pure vector codec shared by the embed provider, the
// derived index and the dense-search wiring: little-endian float32 blobs and
// cosine similarity. It deliberately imports nothing beyond the standard
// library's math/encoding primitives so packages with a no-network-client
// dependency guard (internal/index, internal/store) can use it freely.
package vec

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Encode serializes v as little-endian float32 bytes (the index emb.vec
// column format — deliberately a binary blob, never JSON).
func Encode(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// Decode parses a little-endian float32 blob. A length not divisible by 4 is
// a hard error (corrupt index row), never a silent short read.
func Decode(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vec: blob ยาว %d bytes ไม่ใช่ float32 array", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// Cosine returns the cosine similarity of a and b. Length-mismatched inputs
// are an error (a dim change between index and query must be observable).
func Cosine(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vec: cosine คำนวณไม่ได้ — dim ไม่ตรง (%d กับ %d; รัน `mem reindex`)", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}
