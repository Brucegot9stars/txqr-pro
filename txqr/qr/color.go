package qr

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

// Color matrix optical transfer with QR-style finder patterns at 3 corners
// for robust detection and perspective correction.
//
// Layout (MxM modules, M = 96, 0-based row/col from top-left):
//
//	+------------------------------------------------+
//	|##  .   black border (2 mod)   .  ##            |
//	|## (.)  quiet zone (1 mod)      (..)##          |
//	|##   FP = 7x7 finder (TL/TR/BL)    ##          |
//	|##        calibration strip         ##          |
//	|##        header cells              ##          |
//	|##        payload cells             ##          |
//	|##   FP                          FP  ##        |
//	+------------------------------------------------+

const (
	ColorMatrixSize = 96
	cmFrameW        = 2

	cmFinderSz  = 7
	cmFinderOff = cmFrameW + 1 // 3 — 1-mod quiet zone after border

	cmCalTop = cmFinderOff + cmFinderSz + 1 // 11
	cmCalH   = 4
	cmHdrTop = cmCalTop + cmCalH            // 15
	cmHdrWid = 16
	cmPayTop = cmHdrTop + 2                 // 17

	// Payload ends before the bottom finder region.
	cmPayEnd = ColorMatrixSize - cmFinderOff - cmFinderSz // 86

	cmModeRgb4 = 1
)

var cmNom = [4]color.NRGBA{
	{0, 0, 0, 255},   // 00 black
	{255, 0, 0, 255}, // 01 red
	{0, 255, 0, 255}, // 10 green
	{0, 0, 255, 255}, // 11 blue
}

var cmMagic = [2]byte{'Q', 'F'}

const headerSize = 8

func NumColorPayloadBytes() int {
	rows := cmPayEnd - cmPayTop
	cols := ColorMatrixSize - 2*cmFrameW
	return rows * cols * 2 / 8
}

func payloadCols() (int, int) { return cmFrameW, ColorMatrixSize - cmFrameW }

func barWidth() int {
	lo, hi := payloadCols()
	return (hi - lo) / 4
}

// --- finder mask ---------------------------------------------------------

var finderMask [cmFinderSz][cmFinderSz]int

func init() {
	for dr := 0; dr < cmFinderSz; dr++ {
		for dc := 0; dc < cmFinderSz; dc++ {
			outer := dr == 0 || dr == cmFinderSz-1 || dc == 0 || dc == cmFinderSz-1
			inner := dr >= 2 && dr <= cmFinderSz-3 && dc >= 2 && dc <= cmFinderSz-3
			if outer || inner {
				finderMask[dr][dc] = 0 // black
			} else {
				finderMask[dr][dc] = 1 // white
			}
		}
	}
}

func drawFinder(grid [][]int, r, c int) {
	for dr := 0; dr < cmFinderSz; dr++ {
		for dc := 0; dc < cmFinderSz; dc++ {
			if finderMask[dr][dc] == 0 {
				grid[r+dr][c+dc] = 0
			}
		}
	}
}

func finderTL() (int, int) { return cmFinderOff, cmFinderOff }
func finderTR() (int, int) { return cmFinderOff, ColorMatrixSize - cmFinderOff - cmFinderSz }
func finderBL() (int, int) { return ColorMatrixSize - cmFinderOff - cmFinderSz, cmFinderOff }

func finderCenters() [3]image.Point {
	m := ColorMatrixSize
	return [3]image.Point{
		{cmFinderOff + cmFinderSz/2, cmFinderOff + cmFinderSz/2},
		{m - cmFinderOff - cmFinderSz/2 - 1, cmFinderOff + cmFinderSz/2},
		{cmFinderOff + cmFinderSz/2, m - cmFinderOff - cmFinderSz/2 - 1},
	}
}

// --- encode --------------------------------------------------------------

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
			grid[r][c] = -1
		}
	}

	// Border.
	for r := 0; r < m; r++ {
		for c := 0; c < m; c++ {
			if r < cmFrameW || r >= m-cmFrameW || c < cmFrameW || c >= m-cmFrameW {
				grid[r][c] = 0
			}
		}
	}

	// Finders.
	r0, c0 := finderTL()
	r1, c1 := finderTR()
	r2, c2 := finderBL()
	drawFinder(grid, r0, c0)
	drawFinder(grid, r1, c1)
	drawFinder(grid, r2, c2)

	// Calibration.
	lo, hi := payloadCols()
	barw := barWidth()
	for r := cmCalTop; r < cmCalTop+cmCalH; r++ {
		for c := lo; c < hi; c++ {
			grid[r][c] = min3((c-lo)/barw)
		}
	}

	writeHeader(grid, data)

	// Payload.
	br := bitStreamFor(data)
	for r := cmPayTop; r < cmPayEnd; r++ {
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

// --- bit stream ----------------------------------------------------------

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

// --- decode (direct) -----------------------------------------------------

var errNoColor = errors.New("qr/color: decode failed")

func ColorMatrixDecode(img image.Image) ([]byte, error) {
	data, err := colorMatrixDecodeDirect(img)
	if err == nil {
		return data, nil
	}
	return colorMatrixDecodeFinder(img)
}

func colorMatrixDecodeDirect(img image.Image) ([]byte, error) {
	m := ColorMatrixSize
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < m || h < m {
		return nil, fmt.Errorf("qr/color: image %dx%d too small", w, h)
	}
	scale := w / m
	pick := func(r, c int) color.NRGBA {
		return rgbaAt(img, c*scale+scale/2, r*scale+scale/2)
	}

	lo, hi := payloadCols()
	barW := barWidth()
	refs := cmNom
	for i := 0; i < 4; i++ {
		var acc [3]int; n := 0
		for dy := 1; dy < cmCalH-1; dy++ {
			for dx := 1; dx < barW-1; dx++ {
				p := pick(cmCalTop+dy, lo+i*barW+dx)
				acc[0] += int(p.R); acc[1] += int(p.G); acc[2] += int(p.B); n++
			}
		}
		if n > 0 {
			refs[i] = color.NRGBA{uint8(acc[0] / n), uint8(acc[1] / n), uint8(acc[2] / n), 255}
		}
	}
	head := readHeaderDirect(pick)
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
	out := make([]byte, plen)
	var bitPos int
	for r := cmPayTop; r < cmPayEnd; r++ {
		for c := lo; c < hi; c++ {
			if bitPos >= plen*8 {
				return out, nil
			}
			idx := nearest(refs, pick(r, c))
			byteIdx := bitPos / 8
			shift := 8 - 2 - (bitPos % 8)
			out[byteIdx] |= byte(idx) << uint(shift)
			bitPos += 2
		}
	}
	return out, nil
}

func readHeaderDirect(pick func(int, int) color.NRGBA) []byte {
	out := make([]byte, headerSize)
	var pos int
	for r := 0; r < 2; r++ {
		for c := 0; c < cmHdrWid; c++ {
			if pos >= headerSize*8 {
				return out
			}
			p := pick(cmHdrTop+r, cmFrameW+c)
			idx := nearest(cmNom, p)
			byteIdx := pos / 8
			shift := 8 - 2 - (pos % 8)
			out[byteIdx] |= byte(idx) << uint(shift)
			pos += 2
		}
	}
	return out
}

// --- decode (finder-based) -----------------------------------------------

func colorMatrixDecodeFinder(img image.Image) ([]byte, error) {
	m := ColorMatrixSize
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	total := w * h
	lum := make([]byte, total)
	idx := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			l := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			if l > 255 { l = 255 }
			lum[idx] = byte(l)
			idx++
		}
	}

	modulePx := w / m
	pts := locateFinders(lum, w, w, h, modulePx)
	if pts == nil {
		return nil, errNoColor
	}
	known := finderCenters()

	var axx, axy, atx, ayx, ayy, aty float64
	{
		x1, y1 := float64(pts[0].X), float64(pts[0].Y)
		x2, y2 := float64(pts[1].X), float64(pts[1].Y)
		x3, y3 := float64(pts[2].X), float64(pts[2].Y)
		u1, v1 := float64(known[0].X), float64(known[0].Y)
		u2, v2 := float64(known[1].X), float64(known[1].Y)
		u3, v3 := float64(known[2].X), float64(known[2].Y)
		det := (x1-x3)*(y2-y3) - (x2-x3)*(y1-y3)
		if math.Abs(det) < 1e-6 {
			return nil, errNoColor
		}
		inv := 1.0 / det
		axx = ((u1-u3)*(y2-y3) - (u2-u3)*(y1-y3)) * inv
		axy = ((u1-u3)*(x3-x2) + (u2-u3)*(x1-x3)) * inv
		atx = u1 - axx*x1 - axy*y1
		ayx = ((v1-v3)*(y2-y3) - (v2-v3)*(y1-y3)) * inv
		ayy = ((v1-v3)*(x3-x2) + (v2-v3)*(x1-x3)) * inv
		aty = v1 - ayx*x1 - ayy*y1
	}

	sampleModule := func(row, col int) color.NRGBA {
		px := axx*float64(col) + axy*float64(row) + atx
		py := ayx*float64(col) + ayy*float64(row) + aty
		return rgbaAt(img, int(math.Round(px)), int(math.Round(py)))
	}

	lo, hi := payloadCols()
	barW := barWidth()
	refs := cmNom
	for i := 0; i < 4; i++ {
		var acc [3]int; n := 0
		for dy := 1; dy < cmCalH-1; dy++ {
			for dx := 1; dx < barW-1; dx++ {
				p := sampleModule(cmCalTop+dy, lo+i*barW+dx)
				acc[0] += int(p.R); acc[1] += int(p.G); acc[2] += int(p.B); n++
			}
		}
		if n > 0 {
			refs[i] = color.NRGBA{uint8(acc[0] / n), uint8(acc[1] / n), uint8(acc[2] / n), 255}
		}
	}

	head := readHeaderDirect(sampleModule)
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

	out := make([]byte, plen)
	var bitPos int
	for r := cmPayTop; r < cmPayEnd; r++ {
		for c := lo; c < hi; c++ {
			if bitPos >= plen*8 {
				return out, nil
			}
			idx := nearest(refs, sampleModule(r, c))
			byteIdx := bitPos / 8
			shift := 8 - 2 - (bitPos % 8)
			out[byteIdx] |= byte(idx) << uint(shift)
			bitPos += 2
		}
	}
	return out, nil
}

// --- finder location -----------------------------------------------------

func locateFinders(lum []byte, stride, w, h, modulePx int) []image.Point {
	const maxRatioErr = 0.25

	hits := make([]uint8, w*h)
	hRow := make([]byte, w)
	totalHits := 0
	for y := 0; y < h; y++ {
		copy(hRow, lum[y*stride:y*stride+w])
		for _, cx := range finder1D(hRow, maxRatioErr) {
			hits[y*w+cx] = 1
			totalHits++
		}
	}
	if totalHits < 3 {
		return nil
	}

	vCol := make([]byte, h)
	type xy struct{ x, y int }
	votes := map[xy]int{}
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			vCol[y] = lum[y*stride+x]
		}
		for _, cy := range finder1D(vCol, maxRatioErr) {
			if hits[cy*w+x] != 0 {
				votes[xy{x, cy}]++
			}
		}
	}
	if len(votes) < 3 {
		return nil
	}

	binSz := modulePx*7 + 4
	if binSz < 10 { binSz = 10 }
	type cluster struct{ cx, cy float64; count int }
	clMap := map[[2]int]*cluster{}
	for p, cnt := range votes {
		bk := [2]int{p.x / binSz, p.y / binSz}
		c := clMap[bk]
		if c == nil {
			clMap[bk] = &cluster{float64(p.x) * float64(cnt), float64(p.y) * float64(cnt), cnt}
		} else {
			c.cx += float64(p.x) * float64(cnt)
			c.cy += float64(p.y) * float64(cnt)
			c.count += cnt
		}
	}
	cl := make([]cluster, 0, len(clMap))
	for _, c := range clMap {
		c.cx /= float64(c.count)
		c.cy /= float64(c.count)
		cl = append(cl, *c)
	}
	if len(cl) < 3 { return nil }
	sort.Slice(cl, func(i, j int) bool { return cl[i].count > cl[j].count })
	cl = cl[:3]

	pts := make([]image.Point, 3)
	for i := range pts {
		pts[i] = image.Point{int(math.Round(cl[i].cx)), int(math.Round(cl[i].cy))}
	}
	sort.Slice(pts, func(i, j int) bool {
		di := pts[i].X*pts[i].X + pts[i].Y*pts[i].Y
		dj := pts[j].X*pts[j].X + pts[j].Y*pts[j].Y
		return di < dj
	})
	if pts[1].Y > pts[2].Y {
		pts[1], pts[2] = pts[2], pts[1]
	}
	return pts
}

func finder1D(pix []byte, maxRatioErr float64) []int {
	if len(pix) < 7 { return nil }
	type run struct{ dark bool; w int }
	runs := make([]run, 0, len(pix))
	prev := (pix[0] & 0xFF) < 128
	cw := 0
	for _, v := range pix {
		d := (v & 0xFF) < 128
		if d != prev {
			runs = append(runs, run{prev, cw})
			prev = d; cw = 0
		}
		cw++
	}
	runs = append(runs, run{prev, cw})

	var res []int
	for i := 0; i+5 <= len(runs); i++ {
		r := runs[i : i+5]
		if !r[0].dark || r[1].dark || !r[2].dark || r[3].dark || !r[4].dark {
			continue
		}
		total := float64(r[0].w + r[1].w + r[2].w + r[3].w + r[4].w)
		mod := total / 7
		if mod < 1 { continue }
		if ratioOk(float64(r[0].w), mod, 1, maxRatioErr) &&
			ratioOk(float64(r[1].w), mod, 1, maxRatioErr) &&
			ratioOk(float64(r[2].w), mod, 3, maxRatioErr) &&
			ratioOk(float64(r[3].w), mod, 1, maxRatioErr) &&
			ratioOk(float64(r[4].w), mod, 1, maxRatioErr) {
			cx := 0
			for j := 0; j < i; j++ { cx += runs[j].w }
			cx += r[0].w + r[1].w + r[2].w/2
			res = append(res, cx)
		}
	}
	return res
}

func ratioOk(w, mod float64, exp int, maxErr float64) bool {
	return math.Abs(w/mod-float64(exp)) < maxErr
}

// --- helpers -------------------------------------------------------------

func nearest(refs [4]color.NRGBA, p color.NRGBA) int {
	best := 0; bd := 1 << 30
	for i := range refs {
		if d := distSq(refs[i], p); d < bd {
			bd, best = d, i
		}
	}
	return best
}

func distSq(a, b color.NRGBA) int {
	d0 := int(a.R) - int(b.R)
	d1 := int(a.G) - int(b.G)
	d2 := int(a.B) - int(b.B)
	return d0*d0 + d1*d1 + d2*d2
}

func rgbaAt(img image.Image, x, y int) color.NRGBA {
	if c, ok := img.At(x, y).(color.NRGBA); ok {
		return c
	}
	r, g, b, _ := img.At(x, y).RGBA()
	return color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
}

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
