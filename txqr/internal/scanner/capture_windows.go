//go:build windows

package scanner

import (
	"github.com/divan/txqr/internal/w32"
	"image"
)

// DefaultCapture uses the Windows screen DC.
func DefaultCapture(r Rect) (*image.RGBA, []byte, error) {
	return w32.Capture(w32.Rect{L: r.L, T: r.T, R: r.R, B: r.B})
}
