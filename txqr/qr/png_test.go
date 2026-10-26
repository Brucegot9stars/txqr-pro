package qr

import (
	"bytes"
	"image/png"
	"testing"
)

func TestColorMatrixPNGRoundTrip(t *testing.T) {
	frame := "3/200/28|hello color matrix test 1234567890"
	img, err := ColorMatrixEncode([]byte(frame), 512)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	src, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	got, err := ColorMatrixDecode(src)
	if err != nil {
		t.Fatalf("decode color: %v", err)
	}
	if string(got) != frame {
		t.Fatalf("mismatch:\n got: %q\n want: %q", string(got), frame)
	}
}