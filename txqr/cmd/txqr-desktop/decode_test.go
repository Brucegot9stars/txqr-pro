package main

import (
	"bytes"
	"image"
	"image/draw"
	"testing"

	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"github.com/divan/txqr/qr"
)

func toRGBA(img image.Image) *image.RGBA {
	b := image.NewRGBA(img.Bounds())
	draw.Draw(b, b.Bounds(), img, img.Bounds().Min, draw.Src)
	return b
}

// TestDecimenScanPipeline drives decodeState.feed with rendered decimen frames
// (the exact path the desktop capture loop uses) and checks the recovered file.
func TestDecimenScanPipeline(t *testing.T) {
	file := make([]byte, 120_000)
	for i := range file {
		file[i] = byte((i*37 + (i>>8)*11) & 0xff)
	}
	container, _, err := decimen.PackContainer("scan.bin", "application/octet-stream", file)
	if err != nil {
		t.Fatal(err)
	}

	const blockLen = 2933
	enc := decimen.NewEncoder(container, blockLen, 0x1234)
	hdr := decimen.Header{
		SessionId:  0x1234,
		Seq:        0,
		K:          enc.K,
		BlockLen:   blockLen,
		TotalLen:   uint32(len(container)),
		PayloadFnv: decimen.Fnv1a(container),
	}

	var st decodeState
	cycle := 2 * int(enc.K)
	completed := false
	for seq := uint32(0); seq < uint32(cycle); seq++ {
		h := hdr
		h.Seq = seq
		frame := decimen.PackFrame(h, enc.Encode(seq))
		img, err := qr.EncodeVersioned(string(frame), 512, qr.Low, 40, 4)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if st.feed(toRGBA(img)) {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("did not complete within one cycle")
	}
	st.mu.Lock()
	result, name := st.result, st.fileName
	st.mu.Unlock()
	if name != "scan.bin" || !bytes.Equal(result, file) {
		t.Fatalf("recovered mismatch: name=%q bytes=%d want %d", name, len(result), len(file))
	}
}

// TestTxqrScanPipeline drives the classic txqr path.
func TestTxqrScanPipeline(t *testing.T) {
	payload := "hello desktop receiver \u4e2d\u6587 test 0123456789"
	enc := txqr.NewEncoder(64, txqr.CodecLT)
	frames, err := enc.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	var st decodeState
	completed := false
	for _, f := range frames {
		img, err := qr.Encode(f, 256, qr.Medium)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if st.feed(toRGBA(img)) {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("txqr did not complete")
	}
	st.mu.Lock()
	result := st.result
	st.mu.Unlock()
	if string(result) != payload {
		t.Fatalf("txqr mismatch: %q", string(result))
	}
}
