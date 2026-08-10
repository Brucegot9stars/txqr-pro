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
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", fmt.Errorf("gozxing bitmap: %v", err)
	}

	hints := make(map[gozxing.DecodeHintType]interface{})
	hints[gozxing.DecodeHintType_PURE_BARCODE] = true
	result, err := zqrcode.NewQRCodeReader().Decode(bitmap, hints)
	if err != nil {
		return "", fmt.Errorf("gozxing: %v", err)
	}
	return result.GetText(), nil
}

// DecodeRaw decodes a QR and returns the raw decoded byte stream. Unlike
// Decode it skips text decoding entirely: for byte-mode codes — which is what
// the decimen frames and every txqr sender emit — GetText() would map bytes
// through a charset and corrupt any byte ≥ 0x80, so the raw bytes are the only
// safe representation of a binary frame.
func DecodeRaw(img image.Image) ([]byte, error) {
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, fmt.Errorf("gozxing bitmap: %v", err)
	}

	hints := make(map[gozxing.DecodeHintType]interface{})
	hints[gozxing.DecodeHintType_PURE_BARCODE] = true
	result, err := zqrcode.NewQRCodeReader().Decode(bitmap, hints)
	if err != nil {
		return nil, fmt.Errorf("gozxing: %v", err)
	}
	return result.GetRawBytes(), nil
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
		if isDecimenFrame(out) || bytes.IndexByte(out, '|') >= 0 {
			return out
		}
		return nil
	}
	if p := try(16); p != nil {
		return p
	}
	return try(8)
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
