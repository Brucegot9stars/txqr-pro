package qr

import (
	"bytes"
	"image"
	"image/draw"
	"strings"
	"testing"
)

func TestPreferredDecodeAndFallback(t *testing.T) {
	payload := []byte("12/2048/47104|" + strings.Repeat("payload ", 240))
	img, err := Encode(string(payload), 724, Medium)
	if err != nil {
		t.Fatal(err)
	}
	for _, hint := range []string{"bare", "crop10%@1.0x", "unknown"} {
		raw, err, attempts := DecodeRawMultiScalePreferred(img, []float64{1, .6, 1.7}, hint, func(got []byte) bool { return bytes.Equal(got, payload) })
		if err != nil || !bytes.Equal(ExtractFramePayload(raw), payload) {
			t.Fatalf("hint %q: payload changed, err=%v", hint, err)
		}
		if hint == "bare" && (len(attempts) != 1 || attempts[0].Label != "bare") {
			t.Fatalf("working hint repeated the search: %+v", attempts)
		}
		if hint == "crop10%@1.0x" {
			if attempts[0].Payload || attempts[0].Label != hint || len(attempts) < 2 {
				t.Fatalf("failed hint did not fall back: %+v", attempts)
			}
			for _, at := range attempts[1:] {
				if at.Label == hint {
					t.Fatal("failed hint was retried in the same search")
				}
			}
		}
		if hint == "unknown" && attempts[0].Label != "crop6%@1.0x" {
			t.Fatal("unknown hint changed the original search order")
		}
	}
}

func TestPreferredDecodeRejectsInvalidPayload(t *testing.T) {
	img, err := Encode("looks/like/a/frame|but is not valid", 256, Medium)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	raw, err, attempts := DecodeRawMultiScalePreferred(img, nil, "bare", func([]byte) bool { checked++; return false })
	if err == nil || raw != nil || checked < 2 {
		t.Fatalf("invalid payload stopped fallback: err=%v checks=%d", err, checked)
	}
	for _, at := range attempts {
		if at.Payload {
			t.Fatal("validator rejection was accepted")
		}
	}
}

func TestDecodeAttemptsPreserveSharedLuminance(t *testing.T) {
	payload := "1/512/1024|" + strings.Repeat("binary payload ", 30)
	img, err := Encode(payload, 512, Medium)
	if err != nil {
		t.Fatal(err)
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	source := luminanceSource(rgba)
	before := append([]byte(nil), source.GetMatrix()...)
	for _, pure := range []bool{false, true, false} {
		result, err := decodeOnce(source, pure)
		if err != nil || string(ExtractFramePayload(result.GetRawBytes())) != payload {
			t.Fatalf("pure=%v: %v", pure, err)
		}
		if !bytes.Equal(source.GetMatrix(), before) {
			t.Fatal("QR decoding mutated the shared luminance")
		}
	}
}
