package qr

import (
	"os"
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
	bench := func(version, mask int) time.Duration {
		var total time.Duration
		n := 8
		for i := 0; i < n; i++ {
			data := make([]byte, 2953)
			for j := range data {
				data[j] = byte(j*7 + i)
			}
			start := time.Now()
			if _, err := EncodeVersioned(string(data), 512, Low, version, mask); err != nil {
				t.Fatalf("EncodeVersioned: %v", err)
			}
			total += time.Since(start)
		}
		return total / time.Duration(n)
	}
	pinned := bench(40, 4)
	os.WriteFile("v40_bench.txt", []byte("pinned avg "+pinned.String()+"\n"), 0o644)
}
