package qr_test

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

// Reference renderer keeps the previous palette conversion and pixel-setting
// behavior as an oracle, including padding, tiny sizes and equal/alpha colors.
func referenceQRImage(q *qrcode.QRCode, size int) *image.Paletted {
	bitmap := q.Bitmap()
	real := len(bitmap)
	if size < 0 {
		size = -size * real
	}
	if size < real {
		size = real
	}
	ppm := size / real
	offset := (size - real*ppm) / 2
	img := image.NewPaletted(image.Rect(0, 0, size, size), color.Palette{q.BackgroundColor, q.ForegroundColor})
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, q.BackgroundColor)
		}
	}
	for y, row := range bitmap {
		for x, black := range row {
			if black {
				for yy := y*ppm + offset; yy < (y+1)*ppm+offset; yy++ {
					for xx := x*ppm + offset; xx < (x+1)*ppm+offset; xx++ {
						img.Set(xx, yy, q.ForegroundColor)
					}
				}
			}
		}
	}
	return img
}

func TestQRImageMatchesPreviousPixelsAndPalette(t *testing.T) {
	for _, version := range []int{1, 40} {
		q, err := qrcode.NewFixed("Q|test", version, qrcode.Medium, -1)
		if err != nil {
			t.Fatal(err)
		}
		colors := [][2]color.Color{{color.White, color.Black}}
		sizes := []int{-1, 741}
		if version == 1 {
			sizes = []int{-3, 1, 99, 100}
			colors = append(colors, [2]color.Color{color.Black, color.Black}, [2]color.Color{color.NRGBA{R: 255, A: 128}, color.RGBA{B: 128, A: 128}})
		}
		for _, palette := range colors {
			q.BackgroundColor, q.ForegroundColor = palette[0], palette[1]
			for _, size := range sizes {
				want := referenceQRImage(q, size)
				got := q.Image(size).(*image.Paletted)
				if got.Bounds() != want.Bounds() || got.Stride != want.Stride || !bytes.Equal(got.Pix, want.Pix) {
					t.Fatalf("V%d size=%d: renderer changed pixels/padding", version, size)
				}
			}
		}
	}
}
