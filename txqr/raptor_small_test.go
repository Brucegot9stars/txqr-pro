package txqr

import (
	"strings"
	"testing"
)

// gofountain's Raptor codec only supports K in [4, 8192]. Verify the encoder
// never panics for tiny messages and emits decodable frames by falling back
// to LT when there are fewer than 4 source blocks.
func TestRaptorSmallKFallsBackToLT(t *testing.T) {
	sizes := []int{10, 500, 1024, 1025, 2048, 2049, 3072} // K = 1..3 source blocks
	for _, sz := range sizes {
		data := strings.Repeat("r", sz)
		enc := NewEncoder(1024, CodecRaptor)
		chunks, err := enc.Encode(data)
		if err != nil {
			t.Fatalf("size=%d encode: %v", sz, err)
		}
		if len(chunks) == 0 {
			t.Fatalf("size=%d: no frames", sz)
		}
		// Fallback frames must be LT (3-part header, no codec tag).
		if strings.Contains(chunks[0], "/raptor|") {
			t.Fatalf("size=%d: expected LT fallback, got raptor frame %q", sz, chunks[0])
		}
		d := NewDecoder()
		for _, ch := range chunks {
			if err := d.Decode(ch); err != nil {
				t.Fatalf("size=%d decode: %v", sz, err)
			}
			if d.IsCompleted() {
				break
			}
		}
		if !d.IsCompleted() || d.Data() != data {
			t.Fatalf("size=%d: round trip failed (completed=%v)", sz, d.IsCompleted())
		}
	}
}

// Raptor with K >= 4 must keep emitting raptor-tagged frames when the data
// length is an exact multiple of the chunk size (the case gofountain can
// round-trip), and must silently degrade to LT otherwise.
func TestRaptorNormalKKeepsRaptor(t *testing.T) {
	data := strings.Repeat("M", 1024*10) // exact multiple, K = 10
	enc := NewEncoder(1024, CodecRaptor)
	chunks, err := enc.Encode(data)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(chunks) == 0 || !strings.Contains(chunks[0], "/raptor|") {
		t.Fatalf("expected raptor frame, got %q", chunks[0])
	}
	d := NewDecoder()
	for _, ch := range chunks {
		_ = d.Decode(ch)
		if d.IsCompleted() {
			break
		}
	}
	if !d.IsCompleted() || d.Data() != data {
		t.Fatalf("round trip failed (completed=%v)", d.IsCompleted())
	}
}

// Raptor with K >= 4 but a size gofountain can't decode its own output for
// (any non-multiple of the chunk size) must gracefully degrade to LT frames.
func TestRaptorNonMultipleFallsBackToLT(t *testing.T) {
	data := strings.Repeat("M", 1024*10+5) // K = 11, broken in gofountain
	enc := NewEncoder(1024, CodecRaptor)
	chunks, err := enc.Encode(data)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(chunks) == 0 || strings.Contains(chunks[0], "/raptor|") {
		t.Fatalf("expected LT fallback frame, got %q", chunks[0])
	}
	d := NewDecoder()
	for _, ch := range chunks {
		_ = d.Decode(ch)
		if d.IsCompleted() {
			break
		}
	}
	if !d.IsCompleted() || d.Data() != data {
		t.Fatalf("round trip failed (completed=%v)", d.IsCompleted())
	}
}
