package txqr

import (
	"strings"
	"testing"
)

func TestNameFrameLT(t *testing.T) {
	data := strings.Repeat("abcdefghij", 100)
	enc := NewEncoder(50, CodecLT)
	enc.SetFileName("my important file.pdf")
	chunks, err := enc.Encode(data)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if !strings.HasPrefix(chunks[0], "-1/") {
		t.Fatalf("expected first frame to be metadata, got %q", chunks[0])
	}
	if !strings.HasSuffix(chunks[0], "name|my important file.pdf") {
		t.Fatalf("expected filename in metadata, got %q", chunks[0])
	}

	d := NewDecoder()
	for _, c := range chunks {
		if err := d.Decode(c); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if d.IsCompleted() {
			break
		}
	}
	if !d.IsCompleted() {
		t.Fatalf("decoder not completed")
	}
	if got := d.Data(); got != data {
		t.Fatalf("round trip mismatch, got %d bytes", len(got))
	}
	if d.Total() != len(data) {
		t.Fatalf("total mismatch: got %d want %d", d.Total(), len(data))
	}
}

func TestNameFrameRaptorAndRaptorQ(t *testing.T) {
	data := strings.Repeat("k123456789", 80)
	for _, c := range []CodecType{CodecRaptor, CodecRaptorQ} {
		enc := NewEncoder(40, c)
		enc.SetFileName("report.docx")
		chunks, err := enc.Encode(data)
		if err != nil {
			t.Fatalf("%s encode: %v", c, err)
		}
		if !strings.HasPrefix(chunks[0], "-1/") {
			t.Fatalf("%s: expected metadata first, got %q", c, chunks[0])
		}
		d := NewDecoder()
		for _, ch := range chunks {
			_ = d.Decode(ch)
			if d.IsCompleted() {
				break
			}
		}
		if !d.IsCompleted() || d.Data() != data {
			t.Fatalf("%s round trip failed (completed=%v)", c, d.IsCompleted())
		}
	}
}

func TestSetFileNameEmpty(t *testing.T) {
	enc := NewEncoder(50, CodecLT)
	enc.SetFileName("")
	chunks, err := enc.Encode("hello world")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.HasPrefix(chunks[0], "-1/") {
		t.Fatalf("metadata frame must not be emitted for empty file name, got %q", chunks[0])
	}
}
