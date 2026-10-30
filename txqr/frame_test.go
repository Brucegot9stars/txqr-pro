package txqr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func encoded(t *testing.T, data string, split int, codec CodecType) []string {
	t.Helper()
	f, err := NewEncoder(split, codec).Encode(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSessionMixCannotComplete(t *testing.T) {
	a := encoded(t, strings.Repeat("A", 16), 8, CodecRaptorQ)
	b := encoded(t, strings.Repeat("B", 16), 8, CodecRaptorQ)
	d := NewDecoder()
	if err := d.Decode(a[0]); err != nil {
		t.Fatal(err)
	}
	if err := d.Decode(b[1]); !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("foreign frame: %v", err)
	}
	if d.IsCompleted() || d.Read() != 8 {
		t.Fatal("foreign frame changed state")
	}
	if err := d.Decode(a[1]); err != nil {
		t.Fatal(err)
	}
	if d.Data() != strings.Repeat("A", 16) {
		t.Fatal("source not recovered")
	}
}

func TestBadCRCDoesNotPoisonDedup(t *testing.T) {
	f := encoded(t, "ABC", 512, CodecLT)[0]
	bad := []byte(f)
	bad[len(bad)-3] ^= 0x40
	d := NewDecoder()
	if d.Decode(string(bad)) == nil || d.Read() != 0 || d.Total() != 0 {
		t.Fatal("corruption adopted")
	}
	if err := d.Decode(f); err != nil {
		t.Fatal(err)
	}
	if d.Data() != "ABC" {
		t.Fatal("valid retry failed")
	}
}

func TestSHARejectsValidCRCCorruption(t *testing.T) {
	f, err := ParseFrame(encoded(t, "ABC", 512, CodecLT)[0])
	if err != nil {
		t.Fatal(err)
	}
	f.Payload[0] ^= 1 // simulate a sender with an incorrect equation, and a valid packet CRC
	d := NewDecoder()
	if !errors.Is(d.Decode(f.pack()), ErrIntegrity) {
		t.Fatal("missing final integrity check")
	}
	if d.IsCompleted() || len(d.DataBytes()) != 0 {
		t.Fatal("corruption exposed as success")
	}
}

func TestRaptorQRankDeficiencyKeepsReceiving(t *testing.T) {
	source := string(bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 8))
	f := encoded(t, source, 8, CodecRaptorQ)
	d := NewDecoder()
	for _, id := range []int{10, 5, 13, 3, 11, 1, 15, 9} {
		if err := d.Decode(f[id]); err != nil {
			t.Fatal(err)
		}
	}
	if d.IsCompleted() {
		t.Fatal("rank-deficient symbols reported complete")
	}
	if err := d.Decode(f[8]); err != nil {
		t.Fatal(err)
	}
	if !d.IsCompleted() || d.Data() != source {
		t.Fatal("extra repair symbol did not recover")
	}
}

func TestSingleExactBlockAllCodecs(t *testing.T) {
	for _, ct := range []CodecType{CodecLT, CodecBinary, CodecRaptor, CodecRaptorQ, CodecOnline} {
		d := NewDecoder()
		for _, f := range encoded(t, "12345678", 8, ct) {
			if err := d.Decode(f); err != nil {
				t.Fatal(ct, err)
			}
		}
		if d.Data() != "12345678" {
			t.Fatal(ct, "single block failed")
		}
	}
}

func TestInvalidFrameParameters(t *testing.T) {
	f, _ := ParseFrame(encoded(t, "12345678", 8, CodecRaptorQ)[0])
	for _, mutate := range []func(*Frame){
		func(f *Frame) { f.ChunkLen = 0 }, func(f *Frame) { f.Total = MaxTransferBytes + 1 },
		func(f *Frame) { f.ChunkLen = 1; f.Total = 8193 }, func(f *Frame) { f.BlockCode = -2 },
		func(f *Frame) { f.BlockCode = MaxBlockCode + 1 }, func(f *Frame) { f.Payload = []byte{1} },
	} {
		cp := *f
		mutate(&cp)
		if _, err := ParseFrame(cp.pack()); err == nil {
			t.Fatal("accepted invalid frame")
		}
	}
	if _, err := ParseFrame("0/8/8|12345678"); err == nil {
		t.Fatal("unverified legacy accepted")
	}
}

func TestUnicodeNameIsBoundedAndDecodable(t *testing.T) {
	e := NewEncoder(8, CodecLT)
	e.SetFileName(strings.Repeat("🧪", 120))
	f, err := e.Encode("abc")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseFrame(f[0])
	if err != nil || !parsed.Name {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(parsed.Payload), "🧪") {
		t.Fatal("invalid name")
	}
}

func TestCanceledEncodeDoesNotProduceFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ct := range []CodecType{CodecLT, CodecBinary, CodecRaptor, CodecRaptorQ, CodecOnline} {
		if _, err := NewEncoder(8, ct).EncodeContext(ctx, "123456789"); !errors.Is(err, context.Canceled) {
			t.Fatal(ct, err)
		}
	}
}

func TestReceiverAcceptsLargeRaptorQSession(t *testing.T) {
	f := &Frame{Session: strings.Repeat("0", 32), BlockCode: 0, ChunkLen: 2048,
		Total: 8 << 20, Codec: CodecRaptorQ, Payload: make([]byte, 2048)}
	d := NewDecoder()
	if err := d.Decode(f.pack()); err != nil {
		t.Fatal(err)
	}
	if d.Total() != 8<<20 || d.Read() != 2048 || d.IsCompleted() {
		t.Fatal("large session was not accepted")
	}
}

func TestReceiverDoesNotRejectLongRedundantSequence(t *testing.T) {
	source := []byte("AB")
	f := &Frame{Session: strings.Repeat("0", 32), SHA256: sha256.Sum256(source), ChunkLen: 1, Total: 2, Codec: CodecLT}
	codec := gofountainCodec(2, CodecLT)
	d := NewDecoder()
	count := 0
	var other int64
	for id := int64(0); count < 300; id++ {
		picks := codec.PickIndices(id)
		if len(picks) != 1 {
			continue
		}
		if picks[0] == 1 {
			other = id
			continue
		}
		f.BlockCode = id
		f.Payload = source[:1]
		if err := d.Decode(f.pack()); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if d.IsCompleted() {
		t.Fatal("one source cannot complete a two-source file")
	}
	f.BlockCode = other
	f.Payload = source[1:]
	if err := d.Decode(f.pack()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.DataBytes(), source) {
		t.Fatal("long redundant sequence failed recovery")
	}
}

func FuzzParseFrame(f *testing.F) {
	packets, err := NewEncoder(8, CodecLT).Encode("123456789")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(packets[0])
	f.Add("0/0/1|x")
	f.Add("")
	f.Fuzz(func(t *testing.T, packet string) {
		frame, err := ParseFrame(packet)
		if err != nil {
			return
		}
		if frame.Total <= 0 || frame.ChunkLen <= 0 || frame.Total > MaxTransferBytes {
			t.Fatal("invalid frame adopted")
		}
	})
}
