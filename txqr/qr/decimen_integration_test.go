package qr

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/divan/txqr/decimen"
)

// TestDecimenFrameQRRoundTrip pushes a real decimen frame (DCF2 container →
// fountain encoder → packed frame) through a V40-L pinned-mask QR encode and
// a gozxing decode, then re-parses the frame and decodes the fountain. This
// is the definitive check that the byte-mode QR pipeline carries the exact
// wire bytes the Android receiver will depend on.
func TestDecimenFrameQRRoundTrip(t *testing.T) {	file := make([]byte, 120_000)
	for i := range file {
		file[i] = byte((i*37 + (i>>8)*11) & 0xff)
	}
	container, _, err := decimen.PackContainer("test.bin", "application/octet-stream", file)
	if err != nil {
		t.Fatal(err)
	}

	const blockLen = 2933
	enc := decimen.NewEncoder(container, blockLen, 0xbeef)
	hdr := decimen.Header{
		SessionId:  0xbeef,
		Seq:        0,
		K:          enc.K,
		BlockLen:   blockLen,
		TotalLen:   uint32(len(container)),
		PayloadFnv: decimen.Fnv1a(container),
	}

	dec := decimen.NewDecoder(hdr)
	cycle := 2 * int(enc.K)
	for seq := uint32(0); seq < uint32(cycle+int(enc.K)/4); seq++ {
		h := hdr
		h.Seq = seq
		frame := decimen.PackFrame(h, enc.Encode(seq))
		if len(frame) != 2953 {
			t.Fatalf("frame len %d, want 2953", len(frame))
		}

		img, err := EncodeVersioned(string(frame), 512, Low, 40, 4)
		if err != nil {
			t.Fatalf("encode seq %d: %v", seq, err)
		}
		raw, err := DecodeRaw(img)
		if err != nil {
			t.Fatalf("decode seq %d: %v", seq, err)
		}
		got := ExtractFramePayload(raw)
		if got == nil {
			t.Fatalf("seq %d: no byte-mode payload extracted (raw %d bytes)", seq, len(raw))
		}
		if !bytes.Equal(got, frame) {
			t.Fatalf("seq %d: frame mismatch (%d vs %d bytes)", seq, len(got), len(frame))
		}

		parsed := decimen.ParseFrame([]byte(got))
		if parsed == nil {
			t.Fatalf("seq %d: frame parse failed", seq)
		}
		dec.AddFrame(parsed.Header.Seq, parsed.Payload)
		if dec.IsComplete() {
			break
		}
	}
	if !dec.IsComplete() {
		t.Fatalf("fountain did not complete after a cycle")
	}
	recovered := dec.Assemble()
	if !bytes.Equal(recovered, container) {
		t.Fatalf("recovered container mismatch: %d vs %d bytes", len(recovered), len(container))
	}
	c, err := decimen.UnpackContainer(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "test.bin" || !bytes.Equal(c.Bytes, file) {
		t.Fatalf("unpacked file mismatch: name=%q", c.Name)
	}
}

// TestScreenCaptureDecode simulates a real desktop capture: the QR sits inside
// a larger frame with offset and margin (a boxed screen region), decoded via
// the normal detector path the desktop receiver uses.
func TestScreenCaptureDecode(t *testing.T) {
	payload := make([]byte, 90000)
	for i := range payload {
		payload[i] = byte((i*37 + (i>>8)*11) & 0xff)
	}
	container, _, err := decimen.PackContainer("cap.bin", "application/octet-stream", payload)
	if err != nil {
		t.Fatal(err)
	}
	const blockLen = 2933
	enc := decimen.NewEncoder(container, blockLen, 0xbeef)
	hdr := decimen.Header{
		SessionId:  0xbeef, K: enc.K, BlockLen: blockLen,
		TotalLen:   uint32(len(container)),
		PayloadFnv: decimen.Fnv1a(container),
	}
	h := hdr
	h.Seq = 0
	frame := decimen.PackFrame(h, enc.Encode(0))

	qrImg, err := EncodeVersioned(string(frame), 400, Low, 40, 4)
	if err != nil {
		t.Fatal(err)
	}
	// Put the 400px code into an 800x700 "screen" at an offset with margin.
	canvas := image.NewRGBA(image.Rect(0, 0, 800, 700))
	draw.Draw(canvas, canvas.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{220, 220, 220, 255}), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(120, 90, 120+400, 90+400), qrImg, image.Point{}, draw.Src)

	raw, err := DecodeRaw(canvas)
	if err != nil {
		t.Fatalf("DecodeRaw on screen-like capture: %v", err)
	}
	got := ExtractFramePayload(raw)
	if got == nil || !bytes.Equal(got, frame) {
		t.Fatalf("screen capture frame mismatch: %v", len(got))
	}
}
