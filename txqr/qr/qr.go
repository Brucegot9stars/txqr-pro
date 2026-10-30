package qr

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"

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

// eccMByteCapacity is the byte-mode capacity of each QR version at error
// correction level M, indexed by version (0 unused). The receiver uses it to
// turn "this frame carries N bytes" into "the symbol has M modules", which is
// what lets it tell the user that a capture region is physically too small.
var eccMByteCapacity = [41]int{0,
	14, 26, 42, 62, 84, 106, 122, 152, 180, 213,
	251, 287, 331, 362, 412, 450, 504, 560, 624, 666,
	711, 779, 857, 911, 997, 1059, 1125, 1190, 1264, 1370,
	1452, 1538, 1628, 1722, 1809, 1911, 1989, 2099, 2213, 2331,
}

// ModulesForBytes reports how many modules a QR symbol holding n payload bytes
// occupies per side at error correction level M — the quiet zone included,
// because that is what has to fit inside a capture region.
//
// It returns 0 when n exceeds what a QR symbol can hold. Levels other than M
// change the answer by a version or two, which is within the tolerance of the
// rough "is my region big enough" advice this feeds.
func ModulesForBytes(n int) int {
	for v := 1; v <= 40; v++ {
		if n <= eccMByteCapacity[v] {
			return 21 + 4*(v-1) + 8
		}
	}
	return 0
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
	bitmap, err := gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(luminanceSource(img)))
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

// The library's generic RGBA conversion boxes one color value per pixel.
// Convert captures through their pixel buffer first, preserving its exact
// green-weighted luminance and white-background alpha arithmetic. Gray's
// single-byte color values avoid those per-pixel heap allocations.
func luminanceSource(img image.Image) gozxing.LuminanceSource {
	if rgba, ok := img.(*image.RGBA); ok {
		bounds := rgba.Bounds()
		gray := image.NewGray(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		for y := 0; y < bounds.Dy(); y++ {
			row := rgba.Pix[rgba.PixOffset(bounds.Min.X, bounds.Min.Y+y):]
			for x := 0; x < bounds.Dx(); x++ {
				p := row[x*4 : x*4+4]
				lum := (uint32(p[0]) + 2*uint32(p[1]) + uint32(p[2])) / 4
				alpha := uint32(p[3])
				gray.Pix[y*gray.Stride+x] = byte((lum*alpha + (255-alpha)*255) / 255)
			}
		}
		img = gray
	}
	return gozxing.NewLuminanceSourceFromImage(img)
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

// Attempt is one candidate the multi-scale search tried. It exists so a failed
// decode can be explained instead of merely counted: "a QR was found but its
// payload is not a frame" and "no QR was found at all" need completely
// different fixes (wrong region vs. wrong protocol).
type Attempt struct {
	Label   string // e.g. "crop6%@1.0x", "bare", "up1.5x"
	Err     error  // decoder error; nil when a symbol was decoded
	Found   bool   // a QR symbol was decoded
	Payload bool   // ...and its payload looked like a frame
}

// DecodeRawMultiScale decodes a QR trying cheap crops first, then scales.
// Real screen captures almost always box in the sender's range frame plus
// wide margins, so the bare image rarely decodes but a small edge-crop at the
// original resolution succeeds in ~10ms — putting that path first is what
// makes the desktop receiver fast. Upscales follow for very small codes.
func DecodeRawMultiScale(img image.Image, scales []float64) ([]byte, error) {
	raw, err, _ := DecodeRawMultiScaleVerbose(img, scales)
	return raw, err
}

// DecodeRawMultiScaleVerbose is DecodeRawMultiScale plus the list of attempts
// it made, for the receiver's diagnostic log.
func DecodeRawMultiScaleVerbose(img image.Image, scales []float64) ([]byte, error, []Attempt) {
	b := img.Bounds()
	var attempts []Attempt

	record := func(label string, raw []byte, err error) ([]byte, bool) {
		at := Attempt{Label: label, Err: err, Found: err == nil}
		if at.Found {
			at.Payload = ExtractFramePayload(raw) != nil
		}
		attempts = append(attempts, at)
		if at.Found && at.Payload {
			return raw, true
		}
		return nil, false
	}

	tryCrop := func(cropPct, sc float64) ([]byte, bool) {
		dx := int(float64(b.Dx()) * cropPct)
		dy := int(float64(b.Dy()) * cropPct)
		if b.Dx()-2*dx < 60 || b.Dy()-2*dy < 60 {
			return nil, false
		}
		rect := image.Rect(b.Min.X+dx, b.Min.Y+dy, b.Max.X-dx, b.Max.Y-dy)
		// Captures remain immutable during decode, so standard image types can
		// share their pixels. The QR reader honours nonzero image origins.
		var cropped image.Image
		if source, ok := img.(interface {
			SubImage(image.Rectangle) image.Image
		}); ok {
			cropped = source.SubImage(rect)
		} else {
			copy := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
			draw.Draw(copy, copy.Bounds(), img, rect.Min, draw.Src)
			cropped = copy
		}
		cand := cropped
		if sc != 1.0 {
			cw, ch := cropped.Bounds().Dx(), cropped.Bounds().Dy()
			cand = scaleBilinear(cropped, int(float64(cw)*sc), int(float64(ch)*sc))
		}
		raw, err := DecodeRaw(cand)
		return record(fmt.Sprintf("crop%d%%@%.1fx", int(cropPct*100), sc), raw, err)
	}

	// 1. cheap original-resolution crops (fast path for boxed-in frames)
	for _, cropPct := range []float64{0.06, 0.03, 0.10} {
		if raw, ok := tryCrop(cropPct, 1.0); ok {
			return raw, nil, attempts
		}
	}
	// 2. bare image (tight, border-free boxes)
	bareRaw, bareErr := DecodeRaw(img)
	if raw, ok := record("bare", bareRaw, bareErr); ok {
		return raw, nil, attempts
	}
	// 3. upscales for small codes
	for _, cropPct := range []float64{0.06, 0.03} {
		for _, sc := range []float64{1.5, 2.0} {
			if raw, ok := tryCrop(cropPct, sc); ok {
				return raw, nil, attempts
			}
		}
	}
	// 4. coarse scales as a last resort
	for _, s := range scales {
		if s == 1.0 {
			continue
		}
		w, h := b.Dx(), b.Dy()
		nw, nh := int(float64(w)*s), int(float64(h)*s)
		if nw < 8 || nh < 8 {
			continue
		}
		scaleRaw, scaleErr := DecodeRaw(scaleBilinear(img, nw, nh))
		if raw, ok := record(fmt.Sprintf("scale%.1fx", s), scaleRaw, scaleErr); ok {
			return raw, nil, attempts
		}
	}

	err := fmt.Errorf("no payload at any scale")
	for _, at := range attempts {
		if at.Found && !at.Payload {
			// A symbol was read, its bytes just were not a frame. That is the
			// actionable case (wrong protocol on screen), so say so.
			err = fmt.Errorf("QR decoded but payload is not a txqr/decimen frame")
			break
		}
		if at.Err != nil {
			err = fmt.Errorf("%s: %w", at.Label, at.Err)
		}
	}
	return nil, err, attempts
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
