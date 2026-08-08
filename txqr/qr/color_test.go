package qr

import (
	"bytes"
	"image"
	"image/color"
	"math/rand"
	"testing"
)

func TestColorMatrixRoundTrip(t *testing.T) {
	payload := []byte("hello world " + string(make([]byte, 0)))
	// A payload sized to a couple of modules to exercise real usage.
	payload = make([]byte, 300)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	payload[0] = 'Q'
	payload[1] = 'F'

	img, err := ColorMatrixEncode(payload, 512)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := ColorMatrixDecode(img)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// shiftImage applies a global colour cast + per-pixel noise, simulating a
// screen with heavy colour management and video compression.
func shiftImage(t *testing.T, img image.Image, dr, dg, db int, noise int) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(b)
	rng := rand.New(rand.NewSource(42))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := rgbaAt(img, x, y)
			sr := int(c.R) + dr + rng.Intn(2*noise+1) - noise
			sg := int(c.G) + dg + rng.Intn(2*noise+1) - noise
			sb := int(c.B) + db + rng.Intn(2*noise+1) - noise
			clamp := func(v int) byte { return byte(max(0, min(255, v))) }
			out.Set(x, y, color.NRGBA{clamp(sr), clamp(sg), clamp(sb), 255})
		}
	}
	return out
}

func TestColorMatrixWithColorShiftAndNoise(t *testing.T) {
	payload := make([]byte, 512)
	for i := range payload {
		payload[i] = byte(i)
	}
	img, err := ColorMatrixEncode(payload, 512)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Strong red cast + 8-bit noise: the calibration strip must compensate.
	shifted := shiftImage(t, img, 30, -10, -20, 8)
	got, err := ColorMatrixDecode(shifted)
	if err != nil {
		t.Fatalf("decode with shift: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip with shift failed")
	}
}

func blame_(a int) byte {
	if a < 0 {
		return 0
	}
	if a > 255 {
		return 255
	}
	return byte(a)
}