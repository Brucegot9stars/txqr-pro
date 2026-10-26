// Package w32 is a minimal Windows-only wrapper around the Win32 and GDI
// entry points this project needs. It intentionally has no widget policy —
// higher-level drawing lives in ../uikit — but it does centralise things that
// are easy to get subtly wrong:
//
//   - every resource (HDC/HBITMAP/HBRUSH/HPEN/HFONT) must be released exactly
//     once; the helpers here return typed handles with Close methods so owners
//     can defer that instead of leaking GDI objects (Windows caps a process at
//     ~10 000 and then every draw silently fails),
//   - coordinates are kept in system-DPI pixels (see DPI below), so text metrics,
//     BitBlt and GetSystemMetrics all agree.
package w32

import (
	"fmt"
	"image"
	"math"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// drawLog is an opt-in trace of every shape this package paints. Set
// QRT_DRAW_LOG to a path to get one line per call; it exists because the only
// reliable way to answer "why is this rectangle red" is to see the exact call
// that produced it.
var drawLog *os.File

func init() {
	if p := os.Getenv("QRT_DRAW_LOG"); p != "" {
		if f, err := os.Create(p); err == nil {
			drawLog = f
		}
	}
}

func logDraw(kind string, r Rect, c1, c2 Color) {
	if drawLog == nil {
		return
	}
	fmt.Fprintf(drawLog, "%s %d %d %d %d %06X %06X\n", kind, r.L, r.T, r.R, r.B,
		uint32(c1)&0xFFFFFF, uint32(c2)&0xFFFFFF)
}

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	gdiplus  = windows.NewLazySystemDLL("gdiplus.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")
	msimg32  = windows.NewLazySystemDLL("msimg32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
)

var (
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procCloseHandle      = kernel32.NewProc("CloseHandle")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procUpdateWindow        = user32.NewProc("UpdateWindow")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procGetUpdateRect       = user32.NewProc("GetUpdateRect")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procSetCapture          = user32.NewProc("SetCapture")
	procReleaseCapture      = user32.NewProc("ReleaseCapture")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procSetCursor           = user32.NewProc("SetCursor")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procSetProcessDPIAwareC = shcore.NewProc("SetProcessDpiAwareness")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procSetWindowTextW      = user32.NewProc("SetWindowTextW")
	procMoveWindow          = user32.NewProc("MoveWindow")
	procFillRect            = user32.NewProc("FillRect")
	procFrameRect           = user32.NewProc("FrameRect")
	procCreateSolidBrush    = gdi32.NewProc("CreateSolidBrush")
	procGetDeviceCaps       = gdi32.NewProc("GetDeviceCaps")
	procGetSystemPaletteEnt = gdi32.NewProc("GetSystemPaletteEntries")

	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procStretchBlt             = gdi32.NewProc("StretchBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procSetBkColor             = gdi32.NewProc("SetBkColor")
	procTextOutW               = gdi32.NewProc("TextOutW")
	procDrawTextW              = user32.NewProc("DrawTextW")
	procGetTextExtentPoint32W  = gdi32.NewProc("GetTextExtentPoint32W")
	procStretchDIBits          = gdi32.NewProc("StretchDIBits")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procRectangle              = gdi32.NewProc("Rectangle")
	procMoveToEx               = gdi32.NewProc("MoveToEx")
	procLineTo                 = gdi32.NewProc("LineTo")
	procCreateFontIndirectW    = gdi32.NewProc("CreateFontIndirectW")
	procDeleteObjectTracked    = gdi32.NewProc("DeleteObject")

	procAlphaBlend     = msimg32.NewProc("AlphaBlend")
	procGradientFill   = msimg32.NewProc("GradientFill")
	procTransparentBlt = msimg32.NewProc("TransparentBlt")

	procGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")

	procShellExecuteW = shell32.NewProc("ShellExecuteW")

	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procTerminateProcess         = kernel32.NewProc("TerminateProcess")
)

// ---------------------------------------------------------------- primitives

// Color is a Win32 COLORREF (0x00BBGGRR).
type Color uint32

// NoFill is a sentinel asking a shape routine to leave the interior untouched
// (GDI's RoundRect always fills with the current brush, so "no fill" means
// selecting the NULL brush rather than passing black). COLORREF only uses the
// low 24 bits, so this value can never be a real colour.
const NoFill Color = 0xFF000000

// RGB builds a COLORREF. Note the byte order: the low byte is red.
func RGB(r, g, b uint8) Color { return Color(r) | Color(g)<<8 | Color(b)<<16 }

// Mix blends two COLORREFs; t=0 keeps c1, t=1 becomes c2.
func Mix(c1, c2 Color, t float64) Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	f := func(s Color) uint8 { return uint8(s & 0xFF) }
	blend := func(a, b uint8) uint8 { return uint8(float64(a) + float64(int(b)-int(a))*t) }
	return RGB(blend(f(c1), f(c2)), blend(f(c1>>8), f(c2>>8)), blend(f(c1>>16), f(c2>>16)))
}

// Lighten moves a colour toward white by f (0..1).
func Lighten(c Color, f float64) Color {
	return Mix(c, RGB(255, 255, 255), f)
}

// Darken moves a colour toward black by f (0..1).
func Darken(c Color, f float64) Color {
	return Mix(c, RGB(0, 0, 0), f)
}

// Rect mirrors RECT.
type Rect struct{ L, T, R, B int32 }

// W/H are the rect's extents, never negative.
func (r Rect) W() int32 {
	w := r.R - r.L
	if w < 0 {
		return 0
	}
	return w
}

func (r Rect) H() int32 {
	h := r.B - r.T
	if h < 0 {
		return 0
	}
	return h
}

// Contains reports whether point (x,y) falls inside the rect.
func (r Rect) Contains(x, y int32) bool {
	return x >= r.L && x < r.R && y >= r.T && y < r.B
}

// Inset shrinks the rect on every side.
func (r Rect) Inset(dx, dy int32) Rect {
	return Rect{r.L + dx, r.T + dy, r.R - dx, r.B - dy}
}

// Point is a simple integer coordinate pair.
type Point struct{ X, Y int32 }

// LParamPoint unpacks the client coordinates packed into a WM_*BUTTON lParam.
// Coordinates can be negative on multi-monitor setups, so the low words are
// sign-extended deliberately.
func LParamPoint(l uintptr) Point {
	return Point{X: int32(int16(l & 0xFFFF)), Y: int32(int16((l >> 16) & 0xFFFF))}
}

// ---------------------------------------------------------------- DPI

const (
	monitorDefaultToPrimary   = 1
	mdtEffectiveDPI           = 2
	processPerMonitorDPIAware = 2
	// processSystemDPIAware keeps every coordinate in one space: GetSystemMetrics
	// reports logical pixels and GDI draws in the same units. Per-monitor aware
	// would need per-window scaling and breaks screen capture geometry (the
	// receiver frames virtual-screen rects captured via BitBlt).
	processSystemDPIAware = 1
)

var systemDPIScale = 1.0

// InitDPI installs process-wide DPI awareness and caches the system scale used
// to size fonts. A process whose manifest declares no awareness gets its window
// bitmap-stretched by Windows; this opts into crisp scaling instead.
//
// Errors are non-fatal: on an XP-era box without SetProcessDpiAwareness we fall
// back to SetProcessDPIAware and finally to 1.0.
func InitDPI() {
	if procSetProcessDPIAwareC.Find() == nil {
		if r, _, _ := procSetProcessDPIAwareC.Call(processSystemDPIAware); uint32(r) == 0 {
			if procSetProcessDPIAware.Find() == nil {
				procSetProcessDPIAware.Call()
			}
		}
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}

	const logpixelsy = 90
	if hdc, _, _ := procGetDC.Call(0); hdc != 0 {
		if dpi, _, _ := procGetDeviceCaps.Call(hdc, logpixelsy); dpi > 0 {
			systemDPIScale = float64(dpi) / 96.0
		}
		procReleaseDC.Call(0, hdc)
	}
	if systemDPIScale < 1 {
		systemDPIScale = 1
	}
}

// Scale maps a 96-dpi design unit to the current system scale.
func Scale(v float64) int32 {
	return int32(math.Round(v * systemDPIScale))
}

// ---------------------------------------------------------------- fonts

// Weight is a GDI font weight.
type Weight int32

const (
	WeightNormal      Weight = 400
	WeightMedium      Weight = 500
	WeightSemi        Weight = 600
	WeightBold        Weight = 700
	clearTypeQuality  byte   = 5
	defaultCharset    byte   = 1 // DEFAULT_CHARSET
	outDefaultPrecis  byte   = 0
	clipDefaultPrecis byte   = 0
	ffDontCare        byte   = 0
)

// Font is an owned HFONT. It must be closed, otherwise every window created
// leaks one GDI object per frame.
type Font struct{ h uintptr }

// NewFont creates a font by face name. sizePx is already in system-DPI pixels
// (use Pt to convert from typographic points); height is negative to request
// glyph-height sizing rather than cell height.
func NewFont(face string, size int32, w Weight) *Font {
	lf := struct {
		height, width, escapement, orientation int32
		weight                                 int32
		italic, underline, strikeOut           byte
		charSet, outPrecision, clipPrecision   byte
		quality, pitchAndFamily                byte
		faceName                               [32]uint16
	}{
		height:         -size,
		weight:         int32(w),
		charSet:        defaultCharset,
		outPrecision:   outDefaultPrecis,
		clipPrecision:  clipDefaultPrecis,
		quality:        clearTypeQuality,
		pitchAndFamily: ffDontCare,
	}
	u16, err16 := syscall.UTF16FromString(face)
	if err16 != nil {
		return nil
	}
	copy(lf.faceName[:], u16) // always NUL-terminated
	h, _, _ := procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&lf)))
	if h == 0 {
		return nil
	}
	return &Font{h: h}
}

// Pt converts a typographic point size to system-DPI pixels.
func Pt(points float64) int32 { return Scale(points * 96 / 72) }

// Handle returns the raw HFONT.
func (f *Font) Handle() uintptr {
	if f == nil {
		return 0
	}
	return f.h
}

// Close releases the font.
func (f *Font) Close() {
	if f != nil && f.h != 0 {
		procDeleteObject.Call(f.h)
		f.h = 0
	}
}

// DC wraps a device context together with the objects selected into it, so a
// drawing route can restore/delete them without bookkeeping at the call site.
//
// stockBrush/stockPen are the objects the DC was created with. Every shape
// routine puts the DC back to them before releasing its own handles, because
// GDI forbids deleting an object that is still selected — and if a routine is
// left selected anyway, the next "hollow" shape silently inherits its colour.
// That is exactly how a panel once came out bright red: it inherited the brush
// of the little status dot drawn before it.
type DC struct {
	h          uintptr
	old        map[uintptr]uintptr
	stockBrush uintptr
	stockPen   uintptr
}

const (
	objPen   = 1
	objBrush = 2
)

var procGetCurrentObject = gdi32.NewProc("GetCurrentObject")

func newDC(h uintptr) *DC {
	d := &DC{h: h, old: map[uintptr]uintptr{}}
	if h != 0 {
		if v, _, _ := procGetCurrentObject.Call(h, objBrush); v != 0 {
			d.stockBrush = v
		}
		if v, _, _ := procGetCurrentObject.Call(h, objPen); v != 0 {
			d.stockPen = v
		}
	}
	return d
}

// PaintDC begins painting hwnd and returns the DC plus a helper that must be
// called (normally via defer) to end the paint.
func PaintDC(hwnd uintptr) (*DC, func()) {
	var ps paintstruct
	h, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if h == 0 {
		return nil, func() {}
	}
	d := newDC(h)
	return d, func() { procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps))) }
}

// NewCompatibleDC creates a memory DC matching an existing one.
func NewCompatibleDC(ref uintptr) *DC {
	h, _, _ := procCreateCompatibleDC.Call(ref)
	if h == 0 {
		return nil
	}
	return newDC(h)
}

// resetObjects puts the DC's own brush and pen back, so no shape can inherit
// another shape's colour. Callers invoke it after drawing and before deleting
// the handles they selected.
func (d *DC) resetObjects() {
	if d == nil || d.h == 0 {
		return
	}
	if d.stockBrush != 0 {
		procSelectObject.Call(d.h, d.stockBrush)
	}
	if d.stockPen != 0 {
		procSelectObject.Call(d.h, d.stockPen)
	}
}

// stock returns the DC's original brush or pen, or the matching NULL stock
// object when the original is unknown. Stock handles are shared process-wide,
// so they must never be deleted.
func (d *DC) stock(which uintptr) uintptr {
	if which == objBrush {
		if d != nil && d.stockBrush != 0 {
			return d.stockBrush
		}
		v, _, _ := procGetStockObject.Call(nullBrush)
		return v
	}
	if d != nil && d.stockPen != 0 {
		return d.stockPen
	}
	v, _, _ := procGetStockObject.Call(nullPen)
	return v
}

// Select puts an object (bitmap, font, pen or brush) into the DC, remembering
// the previous one so Restore can put it back. GDI forbids deleting an object
// while it is still selected into any DC, which is exactly why ownership is
// tracked here.
func (d *DC) Select(obj uintptr) {
	if d.h == 0 || obj == 0 {
		return
	}
	if _, seen := d.old[obj]; seen {
		return
	}
	if prev, _, _ := procSelectObject.Call(d.h, obj); prev != 0 {
		d.old[obj] = prev
	}
}

// Restore deselects everything this DC selected, so owned objects can be freed.
func (d *DC) Restore() {
	for obj, prev := range d.old {
		procSelectObject.Call(d.h, prev)
		delete(d.old, obj)
	}
}

// Close releases the DC. Call Restore first if you selected owned objects.
func (d *DC) Close() {
	if d != nil && d.h != 0 {
		procDeleteDC.Call(d.h)
		d.h = 0
	}
}

// Handle returns the raw HDC.
func (d *DC) Handle() uintptr {
	if d == nil {
		return 0
	}
	return d.h
}

// ---------------------------------------------------------------- pens/brushes

// Pen is an HPEN. It is owned unless stock is set, in which case the handle is
// one of the process-wide stock objects and must never be deleted.
type Pen struct {
	h     uintptr
	stock bool
}

// NewPen creates a solid cosmetic pen. A width of 0 is documented to mean 1 px
// and is what we want for hairline borders.
func NewPen(c Color, width int32) *Pen {
	h, _, _ := procCreatePen.Call(0 /*PS_SOLID*/, uintptr(width), uintptr(c))
	if h == 0 {
		return nil
	}
	return &Pen{h: h}
}

func (p *Pen) Handle() uintptr {
	if p == nil {
		return 0
	}
	return p.h
}

// Close releases the pen. Stock objects are shared by the whole process, so
// they are left alone.
func (p *Pen) Close() {
	if p != nil && p.h != 0 && !p.stock {
		procDeleteObject.Call(p.h)
	}
	p.h = 0
}

// Brush is an HBRUSH. It is owned unless stock is set, in which case the
// handle is one of the process-wide stock objects and must never be deleted.
type Brush struct {
	h     uintptr
	stock bool
}

// NewBrush creates a solid brush.
func NewBrush(c Color) *Brush {
	h, _, _ := procCreateSolidBrush.Call(uintptr(c))
	if h == 0 {
		return nil
	}
	return &Brush{h: h}
}

func (b *Brush) Handle() uintptr {
	if b == nil {
		return 0
	}
	return b.h
}

// Close releases the brush. Stock objects are shared by the whole process, so
// they are left alone.
func (b *Brush) Close() {
	if b != nil && b.h != 0 && !b.stock {
		procDeleteObject.Call(b.h)
	}
	b.h = 0
}

// Bitmap is an owned HBITMAP.
type Bitmap struct{ h uintptr }

// NewCompatibleBitmap creates a bitmap matching a DC's pixel format.
func NewCompatibleBitmap(ref uintptr, w, h int32) *Bitmap {
	hb, _, _ := procCreateCompatibleBitmap.Call(ref, uintptr(w), uintptr(h))
	if hb == 0 {
		return nil
	}
	return &Bitmap{h: hb}
}

func (b *Bitmap) Handle() uintptr {
	if b == nil {
		return 0
	}
	return b.h
}

// Close releases the bitmap.
func (b *Bitmap) Close() {
	if b != nil && b.h != 0 {
		procDeleteObject.Call(b.h)
		b.h = 0
	}
}

// Draw helpers. Every one takes a plain int so callers never hand-roll casts.

// Blit copies from src to dst using SRCCOPY.
func (d *DC) Blit(dx, dy, dw, dh int32, src *DC, sx, sy int32) {
	procBitBlt.Call(d.h, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh), src.h, uintptr(sx), uintptr(sy), SrcCopy)
}

var procGetPixel = gdi32.NewProc("GetPixel")

// Pixel reads the colour at (x,y). It reports false when the point is outside
// the DC or the DC cannot be read; CLR_INVALID (0xFFFFFFFF) is treated as
// failure. Used by paint diagnostics to prove pixels reached a window DC.
func (d *DC) Pixel(x, y int32) (Color, bool) {
	if d == nil || d.h == 0 {
		return 0, false
	}
	r, _, _ := procGetPixel.Call(d.h, uintptr(x), uintptr(y))
	if uint32(r) == 0xFFFFFFFF {
		return 0, false
	}
	return Color(uint32(r)), true
}

// Stretch scales src into dst with bilinear smoothing when available.
func (d *DC) Stretch(dx, dy, dw, dh int32, src *DC, sx, sy, sw, sh int32) {
	const halfTone = 4
	procSetStretchBltMode.Call(d.h, halfTone)
	procStretchBlt.Call(d.h, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh),
		src.h, uintptr(sx), uintptr(sy), uintptr(sw), uintptr(sh), SrcCopy)
}

var procSetStretchBltMode = gdi32.NewProc("SetStretchBltMode")

// Alpha composites src over dst with a constant alpha (0..255).
func (d *DC) Alpha(dx, dy, dw, dh int32, src *DC, sx, sy, sw, sh int32, alpha uint8) {
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return
	}
	bf := blendfunction{sourceConstantAlpha: alpha}
	procAlphaBlend.Call(d.h, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh),
		src.h, uintptr(sx), uintptr(sy), uintptr(sw), uintptr(sh), uintptr(unsafe.Pointer(&bf)))
}

// trivertex is GRADIENT_FILL's TRIVERTEX: coordinates plus 16-bit channels.
type trivertex struct {
	x, y           int32
	r, g, b, alpha uint16
}

// gradientRect selects a pair of vertices for one gradient mesh.
type gradientRect struct{ upperLeft, lowerRight uint32 }

const gradientFillRectV = 0x00000001

// VerticalGradient fills r with a top→bottom two-stop gradient using the
// system's msimg32 implementation (one call, no per-scanline brushes). It falls
// back to a flat mid tone when msimg32 is unavailable.
func (d *DC) VerticalGradient(r Rect, top, bottom Color) {
	if d == nil || d.h == 0 || r.W() <= 0 || r.H() <= 0 {
		return
	}
	channels := func(c Color) (uint16, uint16, uint16) {
		return uint16(c&0xFF) << 8, uint16((c>>8)&0xFF) << 8, uint16((c>>16)&0xFF) << 8
	}
	tr, tg, tb := channels(top)
	br, bg, bb := channels(bottom)
	verts := [2]trivertex{
		{r.L, r.T, tr, tg, tb, 0xFFFF},
		{r.R, r.B, br, bg, bb, 0xFFFF},
	}
	mesh := [1]gradientRect{{upperLeft: 0, lowerRight: 1}}
	logDraw("grad", r, top, bottom)
	if n, _, _ := procGradientFill.Call(d.h, uintptr(unsafe.Pointer(&verts[0])), 2,
		uintptr(unsafe.Pointer(&mesh[0])), 1, gradientFillRectV); n == 0 {
		d.FillRect(r, Mix(top, bottom, 0.5))
	}
}

// Text helper -----------------------------------------------------------------

// TextMode controls how Text lays out inside its box.
type TextMode int

const (
	// TextLeft draws a single line, elided.
	TextLeft TextMode = iota
	TextCenter
	TextWrap
	// TextRight draws a single line right-aligned.
	TextRight
)

const (
	dtTop         = 0x00000000
	dtLeft        = 0x00000000
	dtCenter      = 0x00000001
	dtRight       = 0x00000002
	dtVCenter     = 0x00000004
	dtWordBreak   = 0x00000010
	dtSingleLine  = 0x00000020
	dtEndEllipsis = 0x00008000
	dtNoPrefix    = 0x00000800
	dtCalcRect    = 0x00000400
)

// SetTextColor selects the text foreground.
func (d *DC) SetTextColor(c Color) { procSetTextColor.Call(d.h, uintptr(c)) }

// SetBkMode sets the background mix mode (1 = transparent).
func (d *DC) SetBkMode(mode int32) { procSetBkMode.Call(d.h, uintptr(mode)) }

// TextOut draws s at (x,y) with the currently selected font.
func (d *DC) TextOut(x, y int32, s string) {
	if s == "" {
		return
	}
	u, n := utf16N(s)
	procTextOutW.Call(d.h, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(u)), uintptr(n))
}

// Text draws s inside r using mode. TextWrap collapses to single-line ellipsis
// when it would need more lines than the box offers.
func (d *DC) Text(r Rect, s string, mode TextMode) {
	if s == "" || r.W() <= 0 || r.H() <= 0 {
		return
	}
	flags := dtNoPrefix
	switch mode {
	case TextCenter:
		flags |= dtCenter | dtVCenter | dtSingleLine | dtEndEllipsis
	case TextRight:
		flags |= dtRight | dtVCenter | dtSingleLine | dtEndEllipsis
	case TextWrap:
		flags |= dtWordBreak
	case TextLeft:
		flags |= dtLeft | dtVCenter | dtSingleLine | dtEndEllipsis
	}
	u, _ := utf16N(s)
	rc := r
	procDrawTextW.Call(d.h, uintptr(unsafe.Pointer(u)), ^uintptr(0),
		uintptr(unsafe.Pointer(&rc)), uintptr(flags))
}

// TextExtent returns the width/height of s rendered with the selected font.
func (d *DC) TextExtent(s string) (int32, int32) {
	if s == "" {
		return 0, 0
	}
	u, n := utf16N(s)
	var sz struct{ cx, cy int32 }
	procGetTextExtentPoint32W.Call(d.h, uintptr(unsafe.Pointer(u)), uintptr(n), uintptr(unsafe.Pointer(&sz)))
	return sz.cx, sz.cy
}

// Measure returns the size needed to draw s wrapped inside width.
func (d *DC) Measure(s string, width int32) (int32, int32) {
	if s == "" {
		return 0, 0
	}
	u, _ := utf16N(s)
	rc := Rect{0, 0, width, 0}
	procDrawTextW.Call(d.h, uintptr(unsafe.Pointer(u)), ^uintptr(0),
		uintptr(unsafe.Pointer(&rc)), uintptr(dtCalcRect|dtWordBreak|dtNoPrefix))
	return rc.W(), rc.H()
}

// Shapes ----------------------------------------------------------------------

// FillRect paints a solid rectangle.
func (d *DC) FillRect(r Rect, c Color) {
	b := NewBrush(c)
	if b == nil {
		return
	}
	defer b.Close()
	procFillRect.Call(d.h, uintptr(unsafe.Pointer(&r)), b.Handle())
}

// FrameRect strokes a 1px rectangle border.
func (d *DC) FrameRect(r Rect, c Color) {
	b := NewBrush(c)
	if b == nil {
		return
	}
	defer b.Close()
	procFrameRect.Call(d.h, uintptr(unsafe.Pointer(&r)), b.Handle())
}

// RoundRect fills and optionally strokes a rounded rectangle. radius is clamped
// to half the shorter side so a tiny control never draws inverted arcs.
func (d *DC) RoundRect(r Rect, fill Color, stroke Color, radius int32, strokeW int32) {
	w, h := r.W(), r.H()
	if w <= 0 || h <= 0 {
		return
	}
	if radius > w/2 {
		radius = w / 2
	}
	if radius > h/2 {
		radius = h / 2
	}
	if radius < 0 {
		radius = 0
	}
	logDraw("rrect", r, fill, stroke)

	// Both slots are filled explicitly on every call — including with the stock
	// NULL object for "no fill"/"no stroke". Skipping the NULL selection (or
	// doing it only once per DC) leaves whatever the previous shape used still
	// selected, which is how a hollow panel once inherited a red brush.
	br := NewBrushOrNull(fill)
	if br == nil {
		return
	}
	pen := NewPenOrNull(stroke, strokeW)
	if pen == nil {
		br.Close()
		return
	}
	procSelectObject.Call(d.h, br.Handle())
	procSelectObject.Call(d.h, pen.Handle())

	// Win32 strokes the exclusive right/bottom edge; pulling in by one keeps the
	// border fully inside the requested box.
	rr := Rect{r.L, r.T, r.R - 1, r.B - 1}
	procRoundRect.Call(d.h, uintptr(rr.L), uintptr(rr.T), uintptr(rr.R), uintptr(rr.B),
		uintptr(radius*2), uintptr(radius*2))

	// Deselect before deleting: GDI refuses to delete a selected object, so
	// doing it in the other order leaks the handle and leaves the DC pointing
	// at a stale one.
	d.resetObjects()
	pen.Close()
	br.Close()
}

// NewBrush builds a solid brush, or the stock NULL brush when c is NoFill.
// The NULL stock object is shared and must not be deleted, so the caller closes
// the result unconditionally and Close on a stock handle is a no-op.
func NewBrushOrNull(c Color) *Brush {
	if c == NoFill {
		if v, _, _ := procGetStockObject.Call(nullBrush); v != 0 {
			return &Brush{h: v, stock: true}
		}
		return nil
	}
	return NewBrush(c)
}

// NewPenOrNull builds a solid pen, or the stock NULL pen when the caller wants
// no border (width 0 or the NoFill colour).
func NewPenOrNull(c Color, width int32) *Pen {
	if width <= 0 || c == NoFill {
		if v, _, _ := procGetStockObject.Call(nullPen); v != 0 {
			return &Pen{h: v, stock: true}
		}
		return nil
	}
	return NewPen(c, width)
}

const (
	nullBrush = 5
	nullPen   = 8
)

var procGetStockObject = gdi32.NewProc("GetStockObject")

// RoundRectGradient paints a vertical gradient clipped to a rounded rectangle,
// optionally stroking its edge. The clip costs no per-pixel work: a region is
// selected into the DC for exactly one gradient call and then removed, because
// a lingering clip would silently mask every later draw on this DC.
func (d *DC) RoundRectGradient(r Rect, top, bottom Color, radius int32, stroke Color, strokeW int32) {
	w, h := r.W(), r.H()
	if w <= 0 || h <= 0 {
		return
	}
	if radius > w/2 {
		radius = w / 2
	}
	if radius > h/2 {
		radius = h / 2
	}
	if radius < 0 {
		radius = 0
	}
	rgn, _, _ := procCreateRoundRectRgn.Call(uintptr(r.L), uintptr(r.T), uintptr(r.R+1), uintptr(r.B+1),
		uintptr(radius*2), uintptr(radius*2))
	if rgn != 0 {
		procSelectClipRgn.Call(d.h, rgn)
		defer func() {
			procSelectClipRgn.Call(d.h, 0)
			procDeleteObject.Call(rgn)
		}()
	}
	d.VerticalGradient(r, top, bottom)
	if strokeW > 0 {
		// NoFill, not 0: a literal black fill would paint over the gradient we
		// just drew, turning every panel into a black card.
		d.RoundRect(r, NoFill, stroke, radius, strokeW)
	}
}

var (
	procCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	procSelectClipRgn      = gdi32.NewProc("SelectClipRgn")
)

// Line draws a 1px line.
func (d *DC) Line(x1, y1, x2, y2 int32, c Color, width int32) {
	p := NewPen(c, width)
	if p == nil {
		return
	}
	defer p.Close()
	prev := d.SelectTracked(p.Handle())
	var pt struct{ x, y int32 }
	procMoveToEx.Call(d.h, uintptr(x1), uintptr(y1), uintptr(unsafe.Pointer(&pt)))
	procLineTo.Call(d.h, uintptr(x2), uintptr(y2))
	if prev != 0 {
		procSelectObject.Call(d.h, prev)
	}
}

// SelectTracked selects an object and returns the previous one. Prefer
// (*DC).Select when the object can be tracked for the DC's lifetime.
func (d *DC) SelectTracked(obj uintptr) uintptr {
	if d.h == 0 || obj == 0 {
		return 0
	}
	prev, _, _ := procSelectObject.Call(d.h, obj)
	return prev
}

// Ellipse fills a circle inside r with colour c (the previous launcher called
// Ellipse without ever selecting its brush — the dot silently rendered in the
// DC's default white).
func (d *DC) Ellipse(r Rect, c Color) {
	b := NewBrush(c)
	if b == nil {
		return
	}
	p := NewPen(c, 1)
	if p == nil {
		b.Close()
		return
	}
	// Selected explicitly, then reset and released — the same contract as
	// RoundRect. Selecting via Select() and deleting while selected used to
	// leave the coloured brush in the DC for every later shape to inherit.
	procSelectObject.Call(d.h, b.Handle())
	procSelectObject.Call(d.h, p.Handle())
	logDraw("ellipse", r, c, 0)
	procEllipse.Call(d.h, uintptr(r.L), uintptr(r.T), uintptr(r.R), uintptr(r.B))
	d.resetObjects()
	p.Close()
	b.Close()
}

var procEllipse = gdi32.NewProc("Ellipse")

// StretchDIBits draws a raw BGRA buffer (top-down rows) scaled into r.
func (d *DC) StretchDIBits(r Rect, bgra []byte, srcW, srcH int32) {
	if len(bgra) == 0 || srcW <= 0 || srcH <= 0 || r.W() <= 0 || r.H() <= 0 {
		return
	}
	need := int(srcW) * int(srcH) * 4
	if len(bgra) < need {
		return
	}
	bi := &bitmapInfo{bmiHeader: bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    srcW,
		biHeight:   -srcH,
		biPlanes:   1,
		biBitCount: 32,
	}}
	procStretchDIBits.Call(d.h, uintptr(r.L), uintptr(r.T), uintptr(r.W()), uintptr(r.H()),
		0, 0, uintptr(srcW), uintptr(srcH), uintptr(unsafe.Pointer(&bgra[0])),
		uintptr(unsafe.Pointer(bi)), uintptr(DibRGBColors), uintptr(SrcCopy))
}

// ---------------------------------------------------------------- windows

// Window class/creation constants.
const (
	WSOverlapped       = 0x00CF0000
	WSOverlappedWindow = 0x00CF0000
	WSPopup            = 0x80000000
	WSVisible          = 0x10000000
	WSChild            = 0x40000000
	WSClipChildren     = 0x02000000
	WSClipSiblings     = 0x04000000
	WSThickFrame       = 0x00040000
	WSMaximizeBox      = 0x00010000
	WSMinimizeBox      = 0x00020000
	WSSysMenu          = 0x00080000
	WSSizeBox          = WSThickFrame
	WSCaption          = 0x00C00000

	WSExTopmost    = 0x00000008
	WSExToolWindow = 0x00000080
	WSExAppWindow  = 0x00040000
	WSExLayered    = 0x00080000
	WSExComposited = 0x02000000

	SWShow          = 5
	SWShowNormal    = 1
	SWShowMaximized = 3

	SrcCopy      = 0x00CC0020
	DibRGBColors = 0
	Transparent  = 1
	Opaque       = 2

	IDCArrow = 32512
	IDCHand  = 32649
	IDCCross = 32515
	IDCIBeam = 32513

	SMCXVirtualScreen = 78
	SMCYVirtualScreen = 79
	SMXVirtualScreen  = 76
	SMYVirtualScreen  = 77
	SMCXCaption       = 4
	SMCYCaption       = 23

	IDOK          = 1
	IDCANCEL      = 2
	MBIconError   = 0x00000010
	MBIconWarning = 0x00000030
	MBIconInfo    = 0x00000040

	WMNull          = 0x0000
	WMClose         = 0x0010
	WMDestroy       = 0x0002
	WMPaint         = 0x000F
	WMEraseBkgnd    = 0x0014
	WMTimer         = 0x0113
	WMMouseMove     = 0x0200
	WMLButtonDown   = 0x0201
	WMLButtonUp     = 0x0202
	WMLButtonDblClk = 0x0203
	WMRButtonDown   = 0x0204
	WMKeyDown       = 0x0100
	WMChar          = 0x0102
	WMGetMinMaxInfo = 0x0024
	WMSize          = 0x0005
	WMNcActivate    = 0x0086
	WMNcHitTest     = 0x0084
	WMSetCursor     = 0x0020
	WMMouseHover    = 0x02A1
	WMMouseLeave    = 0x02A3
	WMActivate      = 0x0006
	WMSetFocus      = 0x0007
	WMKillFocus     = 0x0008
	WMDropFiles     = 0x0233
	WMCommand       = 0x0111
	WMSysCommand    = 0x0112
	WMKeyDownLast   = 0x0108

	VKEscape = 0x1B
	VKDelete = 0x2E
)

type paintstruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     Rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

type minmaxinfo struct {
	ptReserved     Point
	ptMaxSize      Point
	ptMaxPosition  Point
	ptMinTrackSize Point
	ptMaxTrackSize Point
}

// MinMaxInfo is the view of lParam for WM_GETMINMAXINFO.
type MinMaxInfo struct {
	MinTrackW, MinTrackH int32
	MaxTrackW, MaxTrackH int32
}

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

// Msg mirrors MSG.
type Msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      Point
}

// Instance is this process' module handle.
func Instance() uintptr {
	h, _, _ := procGetModuleHandleW.Call(0)
	return h
}

// RegisterClass registers a window class with the default arrow cursor.
func RegisterClass(name string, wndProc uintptr, cursor int32) {
	wc := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   wndProc,
		hInstance:     Instance(),
		hCursor:       LoadCursor(cursor),
		hbrBackground: 0, // no background: every route paints into a buffer
		lpszClassName: UTF16Ptr(name),
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
}

// LoadCursor loads a stock cursor by resource id; 0 on failure.
func LoadCursor(id int32) uintptr {
	h, _, _ := procLoadCursorW.Call(0, uintptr(id))
	return h
}

// CreateWindow creates a window of a registered class.
func CreateWindow(class, title string, style, exStyle uintptr, x, y, w, h int32, parent uintptr) uintptr {
	hwnd, _, _ := procCreateWindowExW.Call(exStyle,
		uintptr(unsafe.Pointer(UTF16Ptr(class))),
		uintptr(unsafe.Pointer(UTF16Ptr(title))),
		style, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, 0, Instance(), 0)
	return hwnd
}

// ShowAndUpdate makes hwnd visible and forces a first paint.
func ShowAndUpdate(hwnd uintptr) {
	procShowWindow.Call(hwnd, SWShow)
	procUpdateWindow.Call(hwnd)
}

// Invalidate schedules a repaint of hwnd (erase=false keeps the old bits until
// the next paint to avoid flashing).
func Invalidate(hwnd uintptr) {
	procInvalidateRect.Call(hwnd, 0, 0)
}

// PostMessage delivers an asynchronous message.
func PostMessage(hwnd uintptr, msg uint32, wParam, lParam uintptr) {
	procPostMessageW.Call(hwnd, uintptr(msg), wParam, lParam)
}

// ClientRect returns hwnd's client area in client coordinates.
func ClientRect(hwnd uintptr) Rect {
	var r Rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// SetWindowPos moves/sizes a window.
func SetWindowPos(hwnd uintptr, x, y, w, h int32, flags uint32) {
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(flags))
}

// SetWindowText changes a window's title.
func SetWindowText(hwnd uintptr, s string) {
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(UTF16Ptr(s))))
}

// DestroyWindow destroys hwnd.
func DestroyWindow(hwnd uintptr) {
	if hwnd != 0 {
		procDestroyWindow.Call(hwnd)
	}
}

// MessageBox shows a modal dialog owned by hwnd.
func MessageBox(hwnd uintptr, text, caption string, flags uint32) int32 {
	r, _, _ := procMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(UTF16Ptr(text))),
		uintptr(unsafe.Pointer(UTF16Ptr(caption))), uintptr(flags))
	return int32(r)
}

// Screen returns the virtual desktop rect (all monitors).
func Screen() Rect {
	x, _, _ := procGetSystemMetrics.Call(SMXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(SMYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(SMCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(SMCYVirtualScreen)
	return Rect{int32(x), int32(y), int32(x) + int32(w), int32(y) + int32(h)}
}

// Capture grabs a screen rectangle in virtual-screen coordinates and returns
// both an RGBA image for decoding and the raw BGRA byte slice for preview.
// Coordinates outside any monitor are fine — BitBlt clips.
func Capture(r Rect) (*image.RGBA, []byte, error) {
	w, h := r.W(), r.H()
	if w <= 0 || h <= 0 {
		return nil, nil, fmt.Errorf("empty region")
	}
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screen)

	mem := NewCompatibleDC(screen)
	if mem == nil {
		return nil, nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer mem.Close()
	bmp := NewCompatibleBitmap(screen, w, h)
	if bmp == nil {
		return nil, nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer bmp.Close()
	mem.Select(bmp.Handle())
	procBitBlt.Call(mem.Handle(), 0, 0, uintptr(w), uintptr(h), screen, uintptr(r.L), uintptr(r.T), SrcCopy)

	buf := make([]byte, int(w)*int(h)*4)
	bi := bitmapInfo{bmiHeader: bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    w,
		biHeight:   -h, // top-down rows
		biPlanes:   1,
		biBitCount: 32,
	}}
	if n, _, _ := procGetDIBits.Call(mem.Handle(), bmp.Handle(), 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), DibRGBColors); n == 0 {
		return nil, nil, fmt.Errorf("GetDIBits failed")
	}
	rgba := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i, j := 0, 0; i+3 < len(buf); i, j = i+4, j+4 {
		rgba.Pix[j] = buf[i+2]   // R
		rgba.Pix[j+1] = buf[i+1] // G
		rgba.Pix[j+2] = buf[i]   // B
		rgba.Pix[j+3] = 0xFF
	}
	return rgba, buf, nil
}

// OpenURL opens a URL or file with the shell's default handler.
func OpenURL(hwnd uintptr, url string) error {
	r, _, _ := procShellExecuteW.Call(hwnd,
		uintptr(unsafe.Pointer(UTF16Ptr("open"))),
		uintptr(unsafe.Pointer(UTF16Ptr(url))), 0, 0, SWShow)
	// Values <= 32 are the documented error codes.
	if r <= 32 {
		return fmt.Errorf("ShellExecute failed (%d)", int(r))
	}
	return nil
}

// ---------------------------------------------------------------- save dialog

const (
	ofnOverwritePrompt = 0x00000002
	ofnPathMustExist   = 0x00000800
	ofnExplorer        = 0x00080000
	ofnNoChangeDir     = 0x00000008
)

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
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
}

// SaveFile shows the common save dialog and returns the chosen path. An empty
// string means the user cancelled.
func SaveFile(hwnd uintptr, suggested, filter string) string {
	if suggested == "" {
		suggested = "received.bin"
	}
	const maxPath = 4096
	buf := make([]uint16, maxPath)
	u, _ := windows.UTF16FromString(suggested)
	copy(buf, u)
	ofn := openfilename{
		lStructSize: uint32(unsafe.Sizeof(openfilename{})),
		hwndOwner:   hwnd,
		lpstrFilter: nulTerminatedList(filter),
		lpstrFile:   &buf[0],
		nMaxFile:    maxPath,
		lpstrDefExt: UTF16Ptr("bin"),
		flags:       ofnOverwritePrompt | ofnPathMustExist | ofnExplorer | ofnNoChangeDir,
	}
	if r, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return ""
	}
	return Windows16ToString(buf)
}

// Windows16ToString converts a NUL-terminated UTF-16 buffer to a Go string.
func Windows16ToString(buf []uint16) string {
	for i, v := range buf {
		if v == 0 {
			return windows.UTF16ToString(buf[:i])
		}
	}
	return windows.UTF16ToString(buf)
}

// nulTerminatedList builds "a\0b\0\0" from alternating name/pattern pairs.
func nulTerminatedList(items ...string) *uint16 {
	var u []uint16
	for _, s := range items {
		u16, err16 := syscall.UTF16FromString(s)
		if err16 != nil {
			continue
		}
		u = append(u, u16...)
	}
	u = append(u, 0)
	return &u[0]
}

// ---------------------------------------------------------------- misc

// UTF16Ptr converts s to a NUL-terminated UTF-16 pointer. The returned pointer
// stays valid for the lifetime of the process (Go does not move the allocation
// pinned by windows syscall conventions).
func UTF16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

// utf16N returns the UTF-16 data pointer and its code-unit count excluding the
// trailing NUL. TextOutW/GetTextExtentPoint32W take the count, which is NOT the
// string's UTF-8 byte length for non-ASCII text.
func utf16N(s string) (*uint16, int) {
	u, _ := syscall.UTF16FromString(s)
	if len(u) == 0 {
		return &[]uint16{0}[0], 0
	}
	return &u[0], len(u) - 1
}

// Get mesasge pump primitives, used by the app shell.

// GetMessage blocks for the next message; it returns false on WM_QUIT.
func GetMessage(m *Msg) (cont bool, err bool) {
	r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)
	if r == ^uintptr(0) {
		return false, true
	}
	return r != 0, false
}

// TranslateAndDispatch runs the standard pre-dispatch translation.
func TranslateAndDispatch(m *Msg) {
	procTranslateMessage.Call(uintptr(unsafe.Pointer(m)))
	procDispatchMessageW.Call(uintptr(unsafe.Pointer(m)))
}

// Peek drags messages out without blocking, for background repaint pumping.
func Peek(m *Msg) bool {
	r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0, 1 /*PM_REMOVE*/)
	return r != 0
}

// DefWindowProc provides default handling for unprocessed messages.
func DefWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return r
}

// PostQuitMessage terminates the message loop.
func PostQuitMessage(code int) {
	procPostQuitMessage.Call(uintptr(code))
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

type blendfunction struct {
	blendOp, blendFlags, sourceConstantAlpha, alphaFormat byte
}
