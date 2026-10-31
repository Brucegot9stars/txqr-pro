//go:build !windows

package scanner

import (
	"errors"
	"image"
)

// Non-Windows callers must supply a capture function (for example in tests).
func DefaultCapture(r Rect) (*image.RGBA, []byte, error) {
	return nil, nil, errors.New("screen capture requires Windows")
}
