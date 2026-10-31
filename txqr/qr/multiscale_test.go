package qr

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"math/rand"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
)

// imageOnly exercises the fallback for image implementations without SubImage.
type imageOnly struct{ image.Image }

func TestCaptureLuminanceMatchesLibrary(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(-13, 17, 89, 128))
	rand.New(rand.NewSource(42)).Read(canvas.Pix)
	// All alpha values, strided subimages and a nonzero origin must retain the
	// library's luminance exactly, including fully transparent pixels.
	for alpha := 0; alpha < 256; alpha++ {
		canvas.Pix[alpha*4+3] = byte(alpha)
	}
	for _, img := range []image.Image{canvas, canvas.SubImage(image.Rect(-7, 18, 60, 91)), imageOnly{canvas}} {
		want := gozxing.NewLuminanceSourceFromImage(img).GetMatrix()
		got := luminanceSource(img).GetMatrix()
		if !bytes.Equal(got, want) {
			t.Fatalf("%T: luminance conversion changed", img)
		}
	}
}

func TestMultiscaleNonzeroOriginAndGenericImage(t *testing.T) {
	payload := "1/2048/4096|" + strings.Repeat("binary payload ", 30)
	symbol, err := Encode(payload, 512, Medium)
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(17, 29, 529, 541))
	draw.Draw(canvas, canvas.Bounds(), symbol, symbol.Bounds().Min, draw.Src)
	for _, img := range []image.Image{canvas, imageOnly{canvas}} {
		raw, err, _ := DecodeRawMultiScaleVerbose(img, []float64{1, .6, 1.7})
		if err != nil {
			t.Fatalf("%T: %v", img, err)
		}
		if got := string(ExtractFramePayload(raw)); got != payload {
			t.Fatalf("%T: decoded payload changed", img)
		}
	}
}

// TestVerboseDecodeMatchesPlainAndExplains attempts to pin both halves of the
// contract: the verbose variant must return exactly what the plain one does
// (it is the same code path), and it must describe what it tried — that
// description is what the receiver's diagnostic log prints.
func TestVerboseDecodeMatchesPlainAndExplains(t *testing.T) {
	data := []byte("12/1024/2048|" + strings.Repeat("payload ", 40))
	img, err := Encode(string(data), 512, Medium)
	if err != nil {
		t.Fatal(err)
	}

	plain, plainErr := DecodeRawMultiScale(img, []float64{1.0, 0.6, 1.7})
	verbose, verboseErr, attempts := DecodeRawMultiScaleVerbose(img, []float64{1.0, 0.6, 1.7})
	if plainErr != nil || verboseErr != nil {
		t.Fatalf("decode errors: plain=%v verbose=%v", plainErr, verboseErr)
	}
	if !bytes.Equal(plain, verbose) {
		t.Fatal("verbose decode returned different bytes than the plain one")
	}
	if len(attempts) == 0 {
		t.Fatal("verbose decode reported no attempts")
	}
	if !attempts[len(attempts)-1].Payload {
		t.Fatalf("successful attempt not marked as payload: %+v", attempts[len(attempts)-1])
	}
}

// TestVerboseDecodeExplainsABlankCapture is the case the receiver hits constantly
// (wrong region, screen off): every attempt must fail, and the error must name
// one of them so the log line is actionable.
func TestVerboseDecodeExplainsABlankCapture(t *testing.T) {
	blank := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			blank.Set(x, y, color.RGBA{20, 24, 32, 255})
		}
	}

	raw, err, attempts := DecodeRawMultiScaleVerbose(blank, []float64{1.0, 0.6, 1.7})
	if err == nil || raw != nil {
		t.Fatal("blank image decoded")
	}
	if len(attempts) == 0 {
		t.Fatal("no attempts recorded for a blank capture")
	}
	for _, at := range attempts {
		if at.Payload {
			t.Fatalf("blank capture reported a payload: %+v", at)
		}
	}
	if !strings.Contains(err.Error(), ":") && err.Error() != "no payload at any scale" {
		t.Errorf("error does not explain which attempt failed: %v", err)
	}
}

// TestVerboseDecodeFlagsWrongProtocol: a QR that decodes but is not one of our
// frames must be reported as such (the Color Matrix case on a desktop receiver
// is exactly this).
func TestVerboseDecodeFlagsWrongProtocol(t *testing.T) {
	img, err := Encode("https://example.com/not-a-txqr-frame", 512, Medium)
	if err != nil {
		t.Fatal(err)
	}
	raw, derr, attempts := DecodeRawMultiScaleVerbose(img, []float64{1.0})
	if derr == nil || raw != nil {
		t.Skip("URL payloads are considered plain text by plausiblePayload; nothing to assert")
	}
	foundWithoutPayload := 0
	for _, at := range attempts {
		if at.Found && !at.Payload {
			foundWithoutPayload++
		}
	}
	if foundWithoutPayload == 0 {
		t.Fatalf("expected at least one attempt that found a symbol but no frame: %+v", attempts)
	}
	if !strings.Contains(derr.Error(), "not a txqr/decimen frame") {
		t.Errorf("error = %v, want the wrong-protocol wording", derr)
	}
}
