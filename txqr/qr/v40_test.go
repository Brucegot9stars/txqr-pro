package qr

import (
	"bytes"
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
	got, err := DecodeRaw(img)
	if err != nil {
		t.Fatalf("DecodeRaw: %v", err)
	}
	payload := ExtractFramePayload(got)
	if payload == nil {
		t.Fatalf("no byte-mode payload extracted (raw %d bytes)", len(got))
	}
	if !bytes.Equal(payload, data) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(payload), len(data))
	}
}

// TestPinnedMaskIsFaster is a smoke guard, not a benchmark: a pinned-mask V40
// render must stay in the tens of milliseconds so pre-rendering a 2k-frame
// carousel stays affordable (716 frames over GOMAXPROCS workers ≈ seconds).
//
// Timing assertions are skipped under -short, which is how the race-detector
// job runs: the instrumented build makes this render ~17x slower, so any
// millisecond budget would fail there for reasons unrelated to the code.
func TestPinnedMaskIsFaster(t *testing.T) {
	if testing.Short() {
		t.Skip("timing assertion skipped under -short")
	}
	data := make([]byte, 2953)
	for i := range data {
		data[i] = byte(i*3 + i>>8)
	}
	s := string(data)

	// One warm-up render so table setup and allocator growth are not measured.
	if _, err := EncodeVersioned(s, 512, Low, 40, 4); err != nil {
		t.Fatalf("EncodeVersioned: %v", err)
	}

	// Best of three: a single sample on a shared runner can be preempted.
	best := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		start := time.Now()
		if _, err := EncodeVersioned(s, 512, Low, 40, 4); err != nil {
			t.Fatalf("EncodeVersioned: %v", err)
		}
		if d := time.Since(start); d < best {
			best = d
		}
	}
	// ~20 ms locally; 300 ms leaves an order of magnitude of headroom for slow
	// CI machines while still catching a real regression (an accidental 8-way
	// mask evaluation, a per-pixel loop, etc.).
	if best > 300*time.Millisecond {
		t.Fatalf("pinned-mask V40 render took %s (best of 3), unexpectedly slow", best)
	}
}
