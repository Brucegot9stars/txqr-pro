package qr

import (
	"testing"
	"time"
)

// TestV40LCapacity verifies a full 2953-byte decimen frame (20-byte header +
// 2933-byte block) encodes AND decodes at V40-L with the pinned mask.
func TestV40LCapacity(t *testing.T) {
	data := make([]byte, 2953)
	for i := range data {
		data[i] = byte(i*3 + i>>8)
	}
	img, err := EncodeVersioned(string(data), 512, Low, 40, 4)
	if err != nil {
		t.Fatalf("EncodeVersioned 2953B: %v", err)
	}
	if img == nil {
		t.Fatal("nil image")
	}
	got, err := Decode(img)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != string(data) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(data))
	}
}

func TestPinnedMaskIsFaster(t *testing.T) {
	// Smoke guard: a pinned-mask V40 render must complete in bounded time so
	// the parallel pre-render of a full 2k-frame carousel stays affordable
	// (measured ~20ms/frame; 716 frames over GOMAXPROCS workers ≈ seconds).
	data := make([]byte, 2953)
	for i := range data {
		data[i] = byte(i*3 + i>>8)
	}
	start := time.Now()
	if _, err := EncodeVersioned(string(data), 512, Low, 40, 4); err != nil {
		t.Fatalf("EncodeVersioned: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("pinned-mask V40 render took %s, unexpectedly slow", elapsed)
	}
}
