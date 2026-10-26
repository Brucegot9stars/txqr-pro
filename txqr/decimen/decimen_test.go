package decimen

import (
	"bytes"
	"testing"
)

func testPayload(byteLength int) []byte {
	b := make([]byte, byteLength)
	for i := 0; i < byteLength; i++ {
		b[i] = byte((i*37 + (i>>8)*11) & 0xff)
	}
	return b
}

func TestHeaderGolden(t *testing.T) {
	frame := PackFrame(Header{
		SessionId:  0xbeef,
		Seq:        0x01020304,
		K:          0x0111,
		BlockLen:   6,
		TotalLen:   0x00fedcba,
		PayloadFnv: 0x89abcdef,
	}, []byte{1, 2, 3, 4, 5, 6})

	want := []byte{
		0xd1, 0x0d,
		0xef, 0xbe,
		0x04, 0x03, 0x02, 0x01,
		0x11, 0x01,
		0x06, 0x00,
		0xba, 0xdc, 0xfe, 0x00,
		0xef, 0xcd, 0xab, 0x89,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
	}
	if !bytes.Equal(frame, want) {
		t.Fatalf("frame bytes mismatch\n got: %x\nwant: %x", frame, want)
	}
	if len(frame) != HeaderLen+6 {
		t.Fatalf("frame length = %d, want %d", len(frame), HeaderLen+6)
	}

	parsed := ParseFrame(frame)
	if parsed == nil {
		t.Fatal("ParseFrame returned nil")
	}
	if parsed.Header.SessionId != 0xbeef || parsed.Header.Seq != 0x01020304 ||
		parsed.Header.K != 0x0111 || parsed.Header.BlockLen != 6 ||
		parsed.Header.TotalLen != 0x00fedcba || parsed.Header.PayloadFnv != 0x89abcdef {
		t.Fatalf("header round-trip mismatch: %+v", parsed.Header)
	}
	if !bytes.Equal(parsed.Payload, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("payload round-trip mismatch: %v", parsed.Payload)
	}
}

func TestStreamFingerprint(t *testing.T) {
	golden := []struct {
		k, blockLen int
		sessionId   uint16
		want        string
	}{
		{1, 64, 1, "k=1 fnv=0xf6a115c5"},
		{23, 64, 7, "k=23 fnv=0x4a5d3eaa"},
		{179, 2933, 4242, "k=179 fnv=0x54f78d05"},
		{716, 1445, 65535, "k=716 fnv=0x75b73b85"},
	}
	for _, g := range golden {
		payload := testPayload(g.k*g.blockLen - 7)
		enc := NewEncoder(payload, g.blockLen, g.sessionId)
		if int(enc.K) != g.k {
			t.Fatalf("k=%d: encoder.K = %d", g.k, enc.K)
		}
		n := 64
		stream := make([]byte, n*g.blockLen)
		for seq := 0; seq < n; seq++ {
			block := enc.Encode(uint32(seq))
			if len(block) != g.blockLen {
				t.Fatalf("k=%d seq=%d: block len %d != %d", g.k, seq, len(block), g.blockLen)
			}
			copy(stream[seq*g.blockLen:], block)
		}
		got := "k=" + itoa(g.k) + " fnv=0x" + hex32(Fnv1a(stream))
		if got != g.want {
			t.Fatalf("stream fingerprint mismatch\n got: %s\nwant: %s", got, g.want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func hex32(v uint32) string {
	return hexByte(byte(v>>24)) + hexByte(byte(v>>16)) + hexByte(byte(v>>8)) + hexByte(byte(v))
}

func hexByte(b byte) string {
	const hexDigits = "0123456789abcdef"
	return string([]byte{hexDigits[b>>4], hexDigits[b&0xf]})
}

func TestEveryFrameBlockLen(t *testing.T) {
	blockLen := 1445
	enc := NewEncoder(testPayload(blockLen*5+1), blockLen, 3)
	if enc.K != 6 {
		t.Fatalf("K = %d, want 6", enc.K)
	}
	for seq := uint32(0); seq < 200; seq++ {
		if l := len(enc.Encode(seq)); l != blockLen {
			t.Fatalf("seq=%d block len %d != %d", seq, l, blockLen)
		}
	}
}

func TestCarouselComposition(t *testing.T) {
	for _, k := range []uint16{1, 17, 179, 4096} {
		cycle := CycleLength(k)
		for _, pos := range []uint32{0, uint32(k) >> 1, uint32(k - 1)} {
			got := FrameComposition(k, 9, pos)
			if len(got) != 1 || got[0] != uint16(pos) {
				t.Fatalf("k=%d pos=%d: systematic sweep = %v", k, pos, got)
			}
			got = FrameComposition(k, 9, pos+uint32(6)*uint32(cycle))
			if len(got) != 1 || got[0] != uint16(pos) {
				t.Fatalf("k=%d pos=%d later cycle: %v", k, pos, got)
			}
		}
		for _, seq := range []uint32{uint32(k), uint32(k) + 1, uint32(2*k) - 1} {
			idx := FrameComposition(k, 9, seq)
			lo := int(k)
			if repairDegreeMin < lo {
				lo = repairDegreeMin
			}
			hi := int(k)
			if repairDegreeMax > hi {
				hi = repairDegreeMax
			}
			if len(idx) < lo || len(idx) > hi {
				t.Fatalf("k=%d seq=%d: repair degree %d out of [%d,%d]", k, seq, len(idx), lo, hi)
			}
			seen := map[uint16]bool{}
			for _, b := range idx {
				if seen[b] {
					t.Fatalf("k=%d seq=%d: repeated block %d", k, seq, b)
				}
				seen[b] = true
			}
		}
	}
}

func TestFnv1a(t *testing.T) {
	if got := Fnv1a([]byte("hello")); got != 0x4f9f2cab {
		t.Fatalf("fnv1a(hello) = 0x%08x, want 0x4f9f2cab", got)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		length    int
		blockLen  int
		sessionId uint16
	}{
		{1, 64, 1},
		{64*7 + 1, 64, 3},
		{179*2933 - 7, 2933, 4242},
		{300000, 2933, 4242},
		{7, 2933, 65535},
	} {
		payload := testPayload(tc.length)
		enc := NewEncoder(payload, tc.blockLen, tc.sessionId)
		h := Header{
			SessionId:  enc.SessionId,
			Seq:        0,
			K:          enc.K,
			BlockLen:   uint16(enc.BlockLen),
			TotalLen:   uint32(len(payload)),
			PayloadFnv: Fnv1a(payload),
		}
		dec := NewDecoder(h)
		dropEvery := 51
		processed := 0
		for seq := uint32(0); seq < uint32(3*int(enc.K)); seq++ {
			if int(seq)%dropEvery == 0 {
				continue
			}
			block := enc.Encode(seq)
			dec.AddFrame(seq, block)
			processed++
			if dec.IsComplete() {
				break
			}
		}
		if !dec.IsComplete() {
			t.Fatalf("len=%d blockLen=%d: did not complete in %d frames",
				tc.length, tc.blockLen, processed)
		}
		got := dec.Assemble()
		if got == nil {
			t.Fatalf("len=%d blockLen=%d: Complete but Assemble failed", tc.length, tc.blockLen)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("len=%d blockLen=%d: payload mismatch (%d bytes vs %d)",
				tc.length, tc.blockLen, len(got), len(payload))
		}
	}
}

func TestParseFrameRejects(t *testing.T) {
	if ParseFrame(nil) != nil {
		t.Fatal("nil frame accepted")
	}
	if ParseFrame(make([]byte, HeaderLen-1)) != nil {
		t.Fatal("short frame accepted")
	}
	bad := make([]byte, HeaderLen+4)
	copy(bad, []byte{0xd1, 0x0d})
	if ParseFrame(bad) != nil {
		t.Fatal("bad magic accepted")
	}
}

func TestHeaderSeqBytes(t *testing.T) {
	// seq 0x000000ff must be little-endian ff 00 00 00, not ff 00 00.
	frame := PackFrame(Header{SessionId: 1, Seq: 0xff, K: 1, BlockLen: 1, TotalLen: 1, PayloadFnv: 0}, []byte{0})
	if frame[4] != 0xff || frame[5] != 0 || frame[6] != 0 || frame[7] != 0 {
		t.Fatalf("seq little-endian bytes wrong: %x", frame[4:8])
	}
}
