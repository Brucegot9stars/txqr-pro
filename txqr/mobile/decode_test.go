package txqr

import (
	"testing"
	"time"
)

func TestTotalTime(t *testing.T) {
	dur := 12345678 * time.Microsecond // 12.345678s
	got := formatDuration(dur)
	expected := "12.3s"
	if got != expected {
		t.Fatalf("Expected str to be '%s', but got '%s'", expected, got)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		dur  time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{12345 * time.Microsecond, "12.345ms"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.dur)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.dur, got, tt.want)
		}
	}
}

func TestDeduplication(t *testing.T) {
	dec := NewDecoder()
	err := dec.Decode("0/10/100|aaaaaaaaaa")
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	err = dec.Decode("0/10/100|aaaaaaaaaa")
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if dec.IsCompleted() {
		t.Fatal("IsCompleted expected to be false after dedup")
	}
}

func TestInvalidHeader(t *testing.T) {
	dec := NewDecoder()
	err := dec.Decode("nonsense")
	if err == nil {
		t.Fatal("Expected error for invalid header")
	}
}

func TestProgressIncomplete(t *testing.T) {
	dec := NewDecoder()
	err := dec.Decode("0/10/100|aaaaaaaaaa")
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if dec.IsCompleted() {
		t.Fatal("Should not be completed yet")
	}
}

func TestReadInterval(t *testing.T) {
	dec := NewDecoder()
	_ = dec.Decode("0/10/100|aaaaaaaaaa")
	// After first decode, ReadInterval should be 0 (no previous chunk)
	if dec.ReadInterval() != 0 {
		t.Fatalf("ReadInterval expected 0, got %d", dec.ReadInterval())
	}
}

func TestTotalSize(t *testing.T) {
	dec := NewDecoder()
	err := dec.Decode("0/10/100|aaaaaaaaaa")
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	// TotalSize should return formatted string of total
	ts := dec.TotalSize()
	if ts == "" {
		t.Fatal("TotalSize should not be empty")
	}
}
