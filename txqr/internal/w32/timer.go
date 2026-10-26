package w32

import "unsafe"

// SetTimer installs a repeating timer; ms is the period. A timer keeps posting
// messages until it is killed, so the matching KillTimer is mandatory — a stray
// one keeps the process alive after its window is gone.
func SetTimer(hwnd uintptr, id int, ms uint32) {
	procSetTimer.Call(hwnd, uintptr(id), uintptr(ms), 0)
}

// KillTimer removes a timer installed by SetTimer.
func KillTimer(hwnd uintptr, id int) {
	procKillTimer.Call(hwnd, uintptr(id))
}

var (
	procSetTimer  = user32.NewProc("SetTimer")
	procKillTimer = user32.NewProc("KillTimer")
)

// SetCursor switches the mouse cursor; zero clears it.
func SetCursor(h uintptr) { procSetCursor.Call(h) }

// SetCapture routes all mouse input to hwnd until ReleaseCapture is called.
func SetCapture(hwnd uintptr) { procSetCapture.Call(hwnd) }

// ReleaseCapture returns mouse input to normal hit testing.
func ReleaseCapture() { procReleaseCapture.Call() }

// WindowRect returns hwnd's window rectangle in screen coordinates.
func WindowRect(hwnd uintptr) Rect {
	var r Rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// ScreenDC acquires a DC for the whole screen; pair it with ReleaseDC(0, hdc).
func ScreenDC() uintptr {
	h, _, _ := procGetDC.Call(0)
	return h
}

// ReleaseDC releases a screen DC obtained from ScreenDC.
func ReleaseDC(hdc uintptr) { procReleaseDC.Call(0, hdc) }

// ReadBits copies a bitmap's pixels into a fresh top-down BGRA buffer. It is the
// same conversion Capture uses, split out so callers can read back their own
// off-screen bitmaps (e.g. for exporting a UI snapshot).
func ReadBits(dc *DC, bmp *Bitmap, w, h int32) []byte {
	if dc == nil || bmp == nil || w <= 0 || h <= 0 {
		return nil
	}
	buf := make([]byte, int(w)*int(h)*4)
	bi := bitmapInfo{bmiHeader: bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    w,
		biHeight:   -h, // top-down rows
		biPlanes:   1,
		biBitCount: 32,
	}}
	if n, _, _ := procGetDIBits.Call(dc.Handle(), bmp.Handle(), 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), DibRGBColors); n == 0 {
		return nil
	}
	return buf
}
