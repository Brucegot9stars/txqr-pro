package qr

import (
	"errors"
	"fmt"
	"image"
	"image/color"
)

// Color matrix optical transfer: replaces the black/white QR module grid
// with a grid of four maximally-distinguishable colors
// (Black/Red/Green/Blue) so each module carries 2 bits instead of 1. Layout
// follows QRFT_Color_Transfer_Optimization_Design.md:
//
//	+------------------------------------------------+
//	|####   ... black border (2 modules)   ...       |
//	|#RR#   R  G  B  corner markers (rotation)      |
//	|#  |   [calibration strip blk/red/grn/blu]     |
//	|#  |   [header cells QF+mode+len+crc16]        |
//	|#  |   [payload cells: 2 bit/module, rows]     |
//	+------------------------------------------------+
//
// The payload rectangle starts at cmPayTop below the calibration strip and
// spans the full width inside the black frame. Its bit stream is read
// MSB-first, 2 bits per module.
const (
	// ColorMatrixSize is the module matrix dimension (modules per side).
	ColorMatrixSize = 96
	// cmFrameW is the black outer border thickness in modules.
	cmFrameW = 2
	// cmCalTop is the first calibration-strip row; cmCalH its height.
	cmCalTop = 4
	cmCalH   = 4
	// cmHdrTop is the header block's first row (2 rows tall, cmHdrWid wide).
	cmHdrTop = 8
	cmHdrWid = 16

	// cmPayTop is the first payload row.
	cmPayTop = cmHdrTop + 2

	// cmModeRgb4 marks the RGB4 color mode in the header.
	cmModeRgb4 = 1
)

// cmNom are the nominal reference colors for module indices 0..3.
var cmNom = [4]color.NRGBA{
	{0, 0, 0, 255},   // 00 black
	{255, 0, 0, 255}, // 01 red
	{0, 255, 0, 255}, // 10 green
	{0, 0, 255, 255}, // 11 blue
}

// cmMagic is the 2-byte header magic.
var cmMagic = [2]byte{'Q', 'F'}

// headerSize is the header byte count encoded in the header block.
const headerSize = 8

// NumColorPayloadBytes returns the max payload bytes a matrix carries.
func NumColorPayloadBytes() int {
	rows := ColorMatrixSize - cmFrameW - cmPayTop
	cols := ColorMatrixSize - 2*cmFrameW
	return rows * cols * 2 / 8
}

// payloadCols returns the payload/calibration column range [lo,hi).
func payloadCols() (int, int) {
	return cmFrameW, ColorMatrixSize - cmFrameW
}

func barWidth() int {
	lo, hi := payloadCols()
	return (hi - lo) / 4
}

// ColorMatrixEncode renders data into a color-matrix RGBA image of the
// requested pixel size (rounded down to a multiple of ColorMatrixSize).
func ColorMatrixEncode(data []byte, pixelSize int) (image.Image, error) {
	max := NumColorPayloadBytes()
	if len(data) > max {
		return nil, fmt.Errorf("color: payload %d exceeds capacity %d", len(data), max)
	}
	pixelSize = pixelSize / ColorMatrixSize * ColorMatrixSize
	if pixelSize < ColorMatrixSize {
		pixelSize = ColorMatrixSize
	}
	scale := pixelSize / ColorMatrixSize
	m := ColorMatrixSize

	grid := make([][]int, m)
	for r := range grid {
		grid[r] = make([]int, m)
		for c := range grid[r] {
			grid[r][c] = -1 // background white
		}
	}

	// Black border.
	for r := 0; r < m; r++ {
		for c := 0; c < m; c++ {
			if r < cmFrameW || r >= m-cmFrameW || c < cmFrameW || c >= m-cmFrameW {
				grid[r][c] = 0
			}
		}
	}

	// Rotation corner markers: red TL, green TR, blue BL.
	mark := func(r, c, v int) {
		for dr := 0; dr < 2; dr++ {
			for dc := 0; dc < 2; dc++ {
				grid[r+dr][c+dc] = v
			}
		}
	}
	mark(cmFrameW, cmFrameW, 1)
	mark(cmFrameW, m-cmFrameW-2, 2)
	mark(m-cmFrameW-2, cmFrameW, 3)

	// Calibration strip: 4 equal solid bars.
	lo, hi := payloadCols()
	barw := barWidth()
	for r := cmCalTop; r < cmCalTop+cmCalH; r++ {
		for c := lo; c < hi; c++ {
			grid[r][c] = min3((c-lo)/barw)
		}
	}

	// Header block: 8 bytes -> 64 bits -> 32 modules (16 per row).
	writeHeader(grid, data)

	// Payload cells, row-major over the payload rectangle. Unused cells pad
	// with black (0), which decodes as zeros and is harmless.
	br := bitStreamFor(data)
	for r := cmPayTop; r < m-cmFrameW; r++ {
		for c := lo; c < hi; c++ {
			if v, ok := br.next2(); ok {
				grid[r][c] = int(v)
			} else {
				grid[r][c] = 0
			}
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, pixelSize, pixelSize))
	for r := 0; r < m; r++ {
		for c := 0; c < m; c++ {
			var cc color.NRGBA
			if grid[r][c] < 0 {
				cc = color.NRGBA{255, 255, 255, 255}
			} else {
				cc = cmNom[grid[r][c]]
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(c*scale+dx, r*scale+dy, cc)
				}
			}
		}
	}
	return img, nil
}

// bitStream yields successive 2-bit module values (MSB-first) from data.
type bitStream struct {
	d   []byte
	bit int
}

func bitStreamFor(data []byte) *bitStream { return &bitStream{d: data} }

func (b *bitStream) next2() (byte, bool) {
	if b.bit+2 > len(b.d)*8 {
		return 0, false
	}
	byteIdx := b.bit / 8
	shift := 8 - 2 - (b.bit % 8)
	v := (b.d[byteIdx] >> uint(shift)) & 0x3
	b.bit += 2
	return v, true
}

// writeHeader writes the 8 header bytes into the 2x16 header block.
func writeHeader(grid [][]int, data []byte) {
	crcSrc := make([]byte, 6)
	crcSrc[0] = cmMagic[0]
	crcSrc[1] = cmMagic[1]
	crcSrc[2] = cmModeRgb4
	crcSrc[3] = 0
	crcSrc[4] = byte(len(data))
	crcSrc[5] = byte(len(data) >> 8)
	crc := crc16(crcSrc)
	hdr := make([]byte, headerSize)
	copy(hdr, crcSrc)
	hdr[6] = byte(crc >> 8)
	hdr[7] = byte(crc)

	var pos int
	for r := 0; r < 2; r++ {
		for c := 0; c < cmHdrWid; c++ {
			if pos >= headerSize*8 {
				return
			}
			byteIdx := pos / 8
			shift := 8 - 2 - (pos % 8)
			grid[cmHdrTop+r][cmFrameW+c] = int((hdr[byteIdx] >> uint(shift)) & 0x3)
			pos += 2
		}
	}
}

func min3(v int) int {
	if v > 3 {
		return 3
	}
	return v
}

var errNoColor = errors.New("qr/color: decode failed")

// ColorMatrixDecode decodes a color-matrix image back to the byte payload
// with local calibration from the strip.
func ColorMatrixDecode(img image.Image) ([]byte, error) {
	m := ColorMatrixSize
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < m || h < m {
		return nil, fmt.Errorf("qr/color: image %dx%d too small", w, h)
	}
	scale := w / m

	pick := func(r, c int) color.NRGBA {
		x := c*scale + scale/2
		y := r*scale + scale/2
		return rgbaAt(img, x, y)
	}

	rot := estimateRotation(pick)

	// Calibrated reference colors from the strip.
	lo, hi := payloadCols()
	barW := barWidth()
	refs := cmNom
	for i := 0; i < 4; i++ {
		var acc [3]int
		n := 0
		for dy := 1; dy < cmCalH-1; dy++ {
			for dx := 1; dx < barW-1; dx++ {
				p := pickRel(pick, lo+i*barW+dx, cmCalTop+dy, rot)
				acc[0] += int(p.R)
				acc[1] += int(p.G)
				acc[2] += int(p.B)
				n++
			}
		}
		if n > 0 {
			refs[i] = color.NRGBA{uint8(acc[0] / n), uint8(acc[1] / n), uint8(acc[2] / n), 255}
		}
	}

	// Header bytes.
	head := readHeader(pick, rot)
	if head[0] != cmMagic[0] || head[1] != cmMagic[1] {
		return nil, errNoColor
	}
	if crc16(head[:6]) != (uint16(head[6])<<8)|uint16(head[7]) {
		return nil, errNoColor
	}
	plen := int(head[4]) | int(head[5])<<8
	if plen <= 0 || plen > NumColorPayloadBytes() {
		return nil, errNoColor
	}

	// Payload stream.
	out := make([]byte, plen)
	var bitPos int
	for r := cmPayTop; r < m-cmFrameW; r++ {
		for c := lo; c < hi; c++ {
			if bitPos >= plen*8 {
				return out, nil
			}
			p := pickRel(pick, c, r, rot)
			idx := nearest(refs, p)
			byteIdx := bitPos / 8
			shift := 8 - 2 - (bitPos % 8)
			out[byteIdx] |= byte(idx) << uint(shift)
			bitPos += 2
		}
	}
	return out, nil
}

// pickRel resolves an absolute module coordinate under the current rotation.
func pickRel(pick func(int, int) color.NRGBA, c, r, rot int) color.NRGBA {
	last := ColorMatrixSize - 1
	var rr, cc int
	switch rot {
	case 90:
		rr, cc = c, last-r
	case 180:
		rr, cc = last-r, last-c
	case 270:
		rr, cc = last-c, r
	default:
		rr, cc = r, c
	}
	return pick(rr, cc)
}

// estimateRotation picks the rotation that best matches the corner markers.
func estimateRotation(pick func(int, int) color.NRGBA) int {
	type corner struct{ r, c int }
	corners := []corner{
		{cmFrameW, cmFrameW},                          // TL -> red
		{cmFrameW, ColorMatrixSize - cmFrameW - 2},     // TR -> green
		{ColorMatrixSize - cmFrameW - 2, cmFrameW},     // BL -> blue
	}
	var best int
	bestScore := int(^uint(0) >> 1)
	for _, rot := range []int{0, 90, 180, 270} {
		score := 0
		for i, pos := range corners {
			p := pickRel(pick, pos.c, pos.r, rot)
			score += distSq(cmNom[i+1], p)
		}
		if score < bestScore {
			bestScore = score
			best = rot
		}
	}
	return best
}

func distSq(a, b color.NRGBA) int {
	d0, d1, d2 := int(a.R)-int(b.R), int(a.G)-int(b.G), int(a.B)-int(b.B)
	return d0*d0 + d1*d1 + d2*d2
}

// readHeader reads the 2x16 header block back into 8 bytes.
func readHeader(pick func(int, int) color.NRGBA, rot int) []byte {
	out := make([]byte, headerSize)
	var pos int
	for r := 0; r < 2; r++ {
		for c := 0; c < cmHdrWid; c++ {
			if pos >= headerSize*8 {
				return out
			}
			p := pickRel(pick, cmFrameW+c, cmHdrTop+r, rot)
			idx := nearest(cmNom, p)
			byteIdx := pos / 8
			shift := 8 - 2 - (pos % 8)
			out[byteIdx] |= byte(idx) << uint(shift)
			pos += 2
		}
	}
	return out
}

// nearest returns the reference index closest (Euclidean) to p.
func nearest(refs [4]color.NRGBA, p color.NRGBA) int {
	best := 0
	bd := 1 << 30
	for i := range refs {
		d := distSq(refs[i], p)
		if d < bd {
			bd, best = d, i
		}
	}
	return best
}

func rgbaAt(img image.Image, x, y int) color.NRGBA {
	c := img.At(x, y)
	if n, ok := c.(color.NRGBA); ok {
		return n
	}
	r, g, b, _ := c.RGBA()
	return color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
}

// crc16 is the CCITT-FALSE CRC-16 used in the header.
func crc16(b []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, bb := range b {
		crc ^= uint16(bb) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc & 0xFFFF
}