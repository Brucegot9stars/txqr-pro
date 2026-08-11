package qr

import (
	"bytes"
	"fmt"
	"image"

	"github.com/makiuchi-d/gozxing"
	zqrcode "github.com/makiuchi-d/gozxing/qrcode"
	"github.com/skip2/go-qrcode"
)

// RecoveryLevel represents QR encoding error detection/recovery capacity.
type RecoveryLevel int

const (
	// Low is Level L: 7% error recovery.
	Low RecoveryLevel = iota

	// Medium is Level M: 15% error recovery. Good default choice.
	Medium

	// High is Level Q: 25% error recovery.
	High

	// Highers is Level H: 30% error recovery.
	Highest
)

// Encode encodes data into the image with QR code.
func Encode(data string, size int, lvl RecoveryLevel) (image.Image, error) {
	qr, err := qrcode.New(data, qrcode.RecoveryLevel(lvl))
	if err != nil {
		return nil, fmt.Errorf("encode QR: %v", err)
	}
	return qr.Image(size), nil
}

// EncodeVersioned encodes data into a QR code with a forced version and mask.
// Pinning the version keeps every code at identical geometry (required for
// tiling a grid of same-size frames); pinning the mask skips the 8-way mask
// evaluation, which is what makes high-fps streaming generation affordable.
// The mask is read from the symbol's format info by any decoder, so a pinned
// mask never affects the recovered bytes.
func EncodeVersioned(data string, size int, lvl RecoveryLevel, version, mask int) (image.Image, error) {
	qr, err := qrcode.NewFixed(data, version, qrcode.RecoveryLevel(lvl), mask)
	if err != nil {
		return nil, fmt.Errorf("encode QR V%d mask %d: %v", version, mask, err)
	}
	return qr.Image(size), nil
}

// Decode an image with QR code.
func Decode(img image.Image) (string, error) {
	raw, err := DecodeRaw(img)
	if err != nil {
		return "", err
	}
	// DecodeRaw can return the raw data codewords for a code the charset
	// decoder rejects; fall back to text only when a raw decode failed.
	if payload := ExtractFramePayload(raw); payload != nil {
		return string(payload), nil
	}
	return string(raw), nil
}

// decodeOnce runs the QR reader, optionally with the pure-barcode fast path.
func decodeOnce(img image.Image, pure bool) (*gozxing.Result, error) {
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, err
	}
	hints := make(map[gozxing.DecodeHintType]interface{})
	if pure {
		hints[gozxing.DecodeHintType_PURE_BARCODE] = true
	} else {
		// The full detector asserts this hint unconditionally; an absent key
		// panics gozxing, so a no-op callback keeps it quiet. TRY_HARDER makes
		// the detector search harder — the difference between a synthetic
		// render and a blurred/scaled real screen capture.
		hints[gozxing.DecodeHintType_NEED_RESULT_POINT_CALLBACK] = gozxing.ResultPointCallback(func(gozxing.ResultPoint) {})
		hints[gozxing.DecodeHintType_TRY_HARDER] = true
	}
	return zqrcode.NewQRCodeReader().Decode(bitmap, hints)
}

// ScaleBilinear resizes an image with bilinear interpolation (exported for
// callers that need to pre-process frames before decode).
func ScaleBilinear(img image.Image, nw, nh int) image.Image {
	return scaleBilinear(img, nw, nh)
}

// scaleBilinear resizes an image with bilinear interpolation (stdlib-only
// replacement for golang.org/x/image/draw's ApproxBiLinear). Used to shrink
// oversized screen captures and to simulate display-scaling blur in tests.
func scaleBilinear(img image.Image, nw, nh int) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	sx := func(x int) (int, int, float64) {
		f := float64(x) * float64(b.Dx()) / float64(nw)
		i := int(f)
		if i >= b.Dx()-1 {
			return b.Dx() - 1, b.Dx() - 1, 0
		}
		return i, i + 1, f - float64(i)
	}
	sy := func(y int) (int, int, float64) {
		f := float64(y) * float64(b.Dy()) / float64(nh)
		i := int(f)
		if i >= b.Dy()-1 {
			return b.Dy() - 1, b.Dy() - 1, 0
		}
		return i, i + 1, f - float64(i)
	}
	sample := func(x, y int) [4]float64 {
		r, g, bb, aa := img.At(x, y).RGBA()
		return [4]float64{float64(r), float64(g), float64(bb), float64(aa)}
	}
	for y := 0; y < nh; y++ {
		y0, y1, wy := sy(y)
		for x := 0; x < nw; x++ {
			x0, x1, wx := sx(x)
			c00 := sample(x0, y0)
			c10 := sample(x1, y0)
			c01 := sample(x0, y1)
			c11 := sample(x1, y1)
			var o [4]float64
			for i := 0; i < 4; i++ {
				top := c00[i]*(1-wx) + c10[i]*wx
				bot := c01[i]*(1-wx) + c11[i]*wx
				o[i] = top*(1-wy) + bot*wy
			}
			pix := dst.Pix[dst.PixOffset(x, y):]
			for i := 0; i < 4; i++ {
				v := int(o[i] + 0.5)
				if v > 0xFFFF {
					v = 0xFFFF
				}
				pix[i] = byte(v >> 8)
			}
		}
	}
	return dst
}

// scaleForDecode shrinks oversized captures (a full-screen region on a big
// monitor) so gozxing's detector and binarizer run on a sane resolution.
// A V40 code needs ~2px/module; 1500px still gives ~8px/module.
func scaleForDecode(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	maxd := w
	if h > maxd {
		maxd = h
	}
	if maxd <= 1500 {
		return img
	}
	s := 1500.0 / float64(maxd)
	return scaleBilinear(img, int(float64(w)*s), int(float64(h)*s))
}

// DecodeRaw decodes a QR and returns the raw decoded byte stream. Unlike
// Decode it skips text decoding entirely: for byte-mode codes — which is what
// the decimen frames and every txqr sender emit — GetText() would map bytes
// through a charset and corrupt any byte ≥ 0x80, so the raw bytes are the only
// safe representation of a binary frame.
//
// The full detector runs first (robust to real captures that have quiet-zone
// margin, edges and background); if that fails, the pure-barcode fast path is
// tried, which assumes the image is exactly the code.
func DecodeRaw(img image.Image) ([]byte, error) {
	img = scaleForDecode(img)
	result, err := decodeOnce(img, false)
	if err != nil {
		result, err = decodeOnce(img, true)
	}
	if err != nil {
		return nil, fmt.Errorf("gozxing: %v", err)
	}
	return result.GetRawBytes(), nil
}

// DecodeRawMultiScale decodes a QR trying several image scales in order,
// returning the first result that yields a real frame payload. Real screen
// captures can place the code at any relative size; gozxing's detector works
// best when the code is neither tiny nor enormous in the frame, so a downscale
// often turns a "too small relative to the box" miss into a hit.
func DecodeRawMultiScale(img image.Image, scales []float64) ([]byte, error) {
	var lastErr error
	for _, s := range scales {
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		nw, nh := int(float64(w)*s), int(float64(h)*s)
		if nw < 8 || nh < 8 {
			continue
		}
		cand := img
		if s != 1.0 {
			cand = scaleBilinear(img, nw, nh)
		}
		raw, err := DecodeRaw(cand)
		lastErr = err
		if err != nil {
			continue
		}
		if ExtractFramePayload(raw) != nil {
			return raw, nil
		}
	}

	// Fallback: the sender's range frame often gets boxed in with the code —
	// its border lines make gozxing read the format information wrong
	// (FormatException). Cropping the outer band then upscaling removes the
	// interference and raises the module resolution. Verified to turn the
	// previously-all-fail captures into decodes.
	b := img.Bounds()
	dx := int(float64(b.Dx()) * 0.06)
	dy := int(float64(b.Dy()) * 0.06)
	if b.Dx()-2*dx > 60 && b.Dy()-2*dy > 60 {
		cropped := image.NewRGBA(image.Rect(0, 0, b.Dx()-2*dx, b.Dy()-2*dy))
		for y := 0; y < cropped.Bounds().Dy(); y++ {
			for x := 0; x < cropped.Bounds().Dx(); x++ {
				cropped.Set(x, y, img.At(b.Min.X+x+dx, b.Min.Y+y+dy))
			}
		}
		for _, sc := range []float64{2.0, 2.5} {
			cw, ch := cropped.Bounds().Dx(), cropped.Bounds().Dy()
			scaled := scaleBilinear(cropped, int(float64(cw)*sc), int(float64(ch)*sc))
			raw, err := DecodeRaw(scaled)
			lastErr = err
			if err == nil && ExtractFramePayload(raw) != nil {
				return raw, nil
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no payload at any scale")
	}
	return nil, lastErr
}

// bitReader reads MSB-first from a byte slice.
type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) read(n int) (uint32, bool) {
	if n < 0 || r.pos+n > len(r.b)*8 {
		return 0, false
	}
	var v uint32
	for i := 0; i < n; i++ {
		bit := (r.b[r.pos>>3] >> uint(7-(r.pos&7))) & 1
		v = v<<1 | uint32(bit)
		r.pos++
	}
	return v, true
}

func (r *bitReader) remaining() int { return len(r.b)*8 - r.pos }

// ExtractFramePayload pulls the payload out of a QR's raw data codewords for
// a single byte-mode segment (the only layout our senders emit), handling both
// the 8-bit (V1-9) and 16-bit (V10-40) character-count widths. It validates
// the result against a real frame so a wrong width guess can't pass:
//
//   - a decimen frame (20-byte header whose length field matches), or
//   - a classic txqr chunk ("header|payload", ASCII).
//
// Returns nil when the code isn't a byte-mode frame.
func ExtractFramePayload(raw []byte) []byte {
	try := func(width int) []byte {
		r := &bitReader{b: raw}
		mode, ok := r.read(4)
		if !ok || mode != 4 { // 0100 = byte mode
			return nil
		}
		count, ok := r.read(width)
		if !ok || count == 0 || int(count)*8 > r.remaining() {
			return nil
		}
		out := make([]byte, count)
		for i := range out {
			v, ok := r.read(8)
			if !ok {
				return nil
			}
			out[i] = byte(v)
		}
		if plausiblePayload(out) {
			return out
		}
		return nil
	}
	if p := try(16); p != nil {
		return p
	}
	return try(8)
}

// plausiblePayload decides whether a guessed byte-mode payload is a real frame:
// a decimen frame, a classic txqr chunk (header|payload), or plain text.
// This keeps the wrong count-width guess from winning, while still accepting
// text-only codes (no '|') that a generic decoder should read.
func plausiblePayload(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if isDecimenFrame(b) {
		return true
	}
	if bytes.IndexByte(b, '|') >= 0 {
		return true
	}
	for _, c := range b {
		if c < 0x09 || (c > 0x0D && c < 0x20) || c > 0x7E {
			return false
		}
	}
	return true
}

func isDecimenFrame(b []byte) bool {
	if len(b) < 20 || b[0] != decimenMagic0 || b[1] != decimenMagic1 {
		return false
	}
	blockLen := int(b[10]) | int(b[11])<<8
	return blockLen > 0 && len(b) == 20+blockLen
}

const (
	decimenMagic0 = 0xD1
	decimenMagic1 = 0x0D
)

// String implements Stringer interface.
func (r RecoveryLevel) String() string {
	switch r {
	case Low:
		return "Low"
	case Medium:
		return "Medium"
	case High:
		return "High"
	case Highest:
		return "Highest"
	default:
		return "N/A"
	}
}
