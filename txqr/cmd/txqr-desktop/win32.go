package main

import (
	"fmt"
	"image"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procUpdateWindow        = user32.NewProc("UpdateWindow")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procSetCapture          = user32.NewProc("SetCapture")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetCursor           = user32.NewProc("SetCursor")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procFillRect            = user32.NewProc("FillRect")
	procFrameRect           = user32.NewProc("FrameRect")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procCreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	procSetTextColor          = gdi32.NewProc("SetTextColor")
	procSetBkMode             = gdi32.NewProc("SetBkMode")
	procTextOutW               = gdi32.NewProc("TextOutW")
	procGetTextExtentPoint32W  = gdi32.NewProc("GetTextExtentPoint32W")
	procStretchDIBits          = gdi32.NewProc("StretchDIBits")

	procGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
)

const (
	srcCopy      = 0x00CC0020
	dibRGBColors = 0
	transparent  = 1
	opaque       = 2
)

type wndclassex struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wparam  uintptr
	lparam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type point struct{ x, y int32 }

type rect struct{ left, top, right, bottom int32 }

// paintstruct mirrors the Win32 PAINTSTRUCT.
type paintstruct struct {
	hdc        uintptr
	fErase     int32
	rcPaint    rect
	fRestore   int32
	fIncUpdate int32
	rgbReserved [32]byte
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	bmiHeader bitmapInfoHeader
	bmiColors [1]uint32
}

type openfilename struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
}

func utf16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// utf16N returns the UTF-16 pointer and its code-unit count (excluding the
// trailing NUL) — TextOutW and GetTextExtentPoint32W take the code-unit count,
// which is NOT the Go string's UTF-8 byte length for non-ASCII text.
func utf16N(s string) (*uint16, int) {
	u, _ := syscall.UTF16FromString(s)
	return &u[0], len(u) - 1
}

func procCall(p *windows.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

func getModuleHandle(name *uint16) uintptr {
	return procCall(procGetModuleHandleW, uintptr(unsafe.Pointer(name)))
}

func getDC(hwnd uintptr) uintptr {
	return procCall(procGetDC, hwnd)
}

func releaseDC(hwnd, hdc uintptr) {
	procReleaseDC.Call(hwnd, hdc)
}

func createCompatibleDC(hdc uintptr) uintptr {
	return procCall(procCreateCompatibleDC, hdc)
}

func deleteDC(hdc uintptr) {
	procDeleteDC.Call(hdc)
}

func createCompatibleBitmap(hdc uintptr, w, h int) uintptr {
	return procCall(procCreateCompatibleBitmap, hdc, uintptr(w), uintptr(h))
}

func selectObject(hdc, obj uintptr) uintptr {
	return procCall(procSelectObject, hdc, obj)
}

func deleteObject(obj uintptr) {
	procDeleteObject.Call(obj)
}

func bitBlt(dst uintptr, dx, dy, dw, dh int, src uintptr, sx, sy int, rop uint32) {
	procBitBlt.Call(dst, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh), src, uintptr(sx), uintptr(sy), uintptr(rop))
}

func stretchDIBits(hdc uintptr, dx, dy, dw, dh, sx, sy, sw, sh int, bits uintptr, bi *bitmapInfo) {
	procStretchDIBits.Call(hdc, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh),
		uintptr(sx), uintptr(sy), uintptr(sw), uintptr(sh), bits, uintptr(unsafe.Pointer(bi)),
		uintptr(dibRGBColors), uintptr(srcCopy))
}

func createSolidBrush(rgb uint32) uintptr {
	return procCall(procCreateSolidBrush, uintptr(rgb))
}

func fillRect(hdc uintptr, rc *rect, br uintptr) {
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(rc)), br)
}

func frameRect(hdc uintptr, rc *rect, br uintptr) {
	procFrameRect.Call(hdc, uintptr(unsafe.Pointer(rc)), br)
}

func setTextColor(hdc uintptr, rgbVal uint32) {
	procSetTextColor.Call(hdc, uintptr(rgbVal))
}

func setBkMode(hdc, mode uintptr) {
	procSetBkMode.Call(hdc, mode)
}

func textOutW(hdc uintptr, x, y int, text *uint16, n int) {
	procTextOutW.Call(hdc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(text)), uintptr(n))
}

func textExtent(hdc uintptr, text *uint16, n int) (int32, int32) {
	var sz struct{ cx, cy int32 }
	procGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(text)), uintptr(n), uintptr(unsafe.Pointer(&sz)))
	return sz.cx, sz.cy
}

func getSaveFileName(ofn *openfilename) bool {
	return procCall(procGetSaveFileNameW, uintptr(unsafe.Pointer(ofn))) != 0
}

func messageBox(hwnd uintptr, text, caption string, flags uint32) uintptr {
	return procCall(procMessageBoxW, hwnd, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16(caption))), uintptr(flags))
}

func getWindowRect(hwnd uintptr) rect {
	var r rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// paintBuffered runs the WM_PAINT draw callback against a memory DC and blits
// it to the window in one shot. Combined with BeginPaint/EndPaint (which
// validates the update region) this eliminates the flicker a GetDC-based
// paint produces — especially the selection overlay, which repaints on every
// mouse move.
func paintBuffered(hwnd uintptr, draw func(hdc uintptr)) {
	var ps paintstruct
	hdc := procCall(procBeginPaint, hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	w := int(ps.rcPaint.right)
	h := int(ps.rcPaint.bottom)
	if w <= 0 || h <= 0 {
		return
	}
	mem := createCompatibleDC(hdc)
	if mem == 0 {
		return
	}
	defer deleteDC(mem)
	bmp := createCompatibleBitmap(hdc, w, h)
	if bmp == 0 {
		return
	}
	defer deleteObject(bmp)
	old := selectObject(mem, bmp)

	draw(mem)
	bitBlt(hdc, 0, 0, w, h, mem, 0, 0, srcCopy)
	selectObject(mem, old)
}

func getClientRect(hwnd uintptr) rect {
	var r rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func setWindowPos(hwnd uintptr, insertAfter uintptr, x, y, cx, cy int, flags uint32) {
	procSetWindowPos.Call(hwnd, insertAfter, uintptr(x), uintptr(y), uintptr(cx), uintptr(cy), uintptr(flags))
}

// captureRegion grabs a screen rectangle (virtual-screen coordinates) as a
// BGRA buffer plus an RGBA image for decoding. Negative coordinates are fine:
// BitBlt clips against the screen DC.
func captureRegion(x, y, w, h int) (*image.RGBA, []byte, error) {
	if w <= 0 || h <= 0 {
		return nil, nil, fmt.Errorf("empty region")
	}
	screen := getDC(0)
	if screen == 0 {
		return nil, nil, fmt.Errorf("GetDC failed")
	}
	defer releaseDC(0, screen)

	mem := createCompatibleDC(screen)
	if mem == 0 {
		return nil, nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer deleteDC(mem)

	bmp := createCompatibleBitmap(screen, w, h)
	if bmp == 0 {
		return nil, nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer deleteObject(bmp)

	selectObject(mem, bmp)
	bitBlt(mem, 0, 0, w, h, screen, x, y, srcCopy)

	buf := make([]byte, w*h*4)
	bi := &bitmapInfo{
		bmiHeader: bitmapInfoHeader{
			biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			biWidth:       int32(w),
			biHeight:      -int32(h), // top-down rows
			biPlanes:      1,
			biBitCount:    32,
			biCompression: 0, // BI_RGB
		},
	}
	if procCall(procGetDIBits, mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(bi)), dibRGBColors) == 0 {
		return nil, nil, fmt.Errorf("GetDIBits failed")
	}

	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, j := 0, 0; i < len(buf); i, j = i+4, j+4 {
		rgba.Pix[j] = buf[i+2]   // R
		rgba.Pix[j+1] = buf[i+1] // G
		rgba.Pix[j+2] = buf[i]   // B
		rgba.Pix[j+3] = 0xFF
	}
	return rgba, buf, nil
}
