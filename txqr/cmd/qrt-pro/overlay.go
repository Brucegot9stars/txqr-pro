package main

import (
	"fmt"
	"github.com/divan/txqr/internal/scanner"
	"github.com/divan/txqr/internal/w32"
)

// shutdown releases everything the app owns. It runs once, after the message
// loop exits, and is also called on the close path so a hidden window failure
// cannot leave background goroutines writing to disk forever.
func (a *app) shutdown() {
	w32.KillTimer(a.hwnd, timerRefresh)
	_ = a.srv.Close()
	_ = a.scan.Close()
	a.theme.Close()
}

// ---------------------------------------------------------------- selection overlay

// beginSelect freezes the desktop and shows a full-window overlay to box the QR
// region in. Every running region is paused first: the overlay covers all
// monitors, so anything it scans would otherwise be a picture of itself.
func (a *app) beginSelect() {
	// HALTING before the snapshot means the freeze cannot race a live capture.
	a.resumeAfter = map[int]bool{}
	for _, s := range a.scan.Snapshots() {
		if s.Scanning {
			a.resumeAfter[s.ID] = true
		}
	}
	a.scan.StopAll()

	vs := w32.Screen()
	_, bgra, err := w32.Capture(vs)
	if err != nil {
		w32.MessageBox(a.hwnd, "屏幕捕获失败: "+err.Error(), "错误", w32.MBIconError)
		a.resumeAll()
		return
	}
	a.selBuf = bgra
	a.selW, a.selH = int(vs.W()), int(vs.H())
	a.dragging = false

	ex := uintptr(w32.WSExTopmost | w32.WSExToolWindow)
	hwnd := w32.CreateWindow(overlayClass, "框选二维码区域", uintptr(w32.WSPopup|w32.WSVisible), ex,
		vs.L, vs.T, vs.W(), vs.H(), 0)
	if hwnd == 0 {
		w32.MessageBox(a.hwnd, "创建选框失败", "错误", w32.MBIconError)
		a.resumeAll()
		return
	}
	a.overlayHwnd = hwnd
	w32.ShowAndUpdate(hwnd)
	w32.SetCapture(hwnd)
}

func (a *app) overlayProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case w32.WMPaint:
		a.paintOverlay()
		return 0
	case w32.WMEraseBkgnd:
		return 1
	case w32.WMLButtonDown:
		p := w32.LParamPoint(lParam)
		a.dragging = true
		a.selStart = p
		a.selCur = p
		w32.Invalidate(hwnd)
		return 0
	case w32.WMMouseMove:
		if a.dragging {
			a.selCur = w32.LParamPoint(lParam)
			w32.Invalidate(hwnd)
		}
		return 0
	case w32.WMLButtonUp:
		if a.dragging {
			a.dragging = false
			a.selCur = w32.LParamPoint(lParam)
			a.finishSelect()
		}
		return 0
	case w32.WMKeyDown:
		if wParam == w32.VKEscape {
			a.cancelSelect()
		}
		return 0
	case w32.WMClose:
		a.cancelSelect()
		return 0
	}
	return w32.DefWindowProc(hwnd, msg, wParam, lParam)
}

// finishSelect converts the drag into a virtual-screen rectangle and registers
// it as a scanning region, then resumes whatever was running before.
func (a *app) finishSelect() {
	hwnd := a.overlayHwnd
	a.overlayHwnd = 0
	if hwnd != 0 {
		w32.ReleaseCapture()
		w32.DestroyWindow(hwnd)
	}

	vs := w32.Screen()
	x0, y0 := a.selStart.X, a.selStart.Y
	x1, y1 := a.selCur.X, a.selCur.Y
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	// A stray click (or a drag too small to hold a QR code) cancels, as any
	// screenshot tool does.
	if x1-x0 < 8 || y1-y0 < 8 {
		a.selBuf = nil
		a.resumeAll()
		w32.Invalidate(a.hwnd)
		return
	}
	rect := w32.Rect{
		L: x0 + vs.L, T: y0 + vs.T,
		R: x1 + vs.L, B: y1 + vs.T,
	}
	a.selBuf = nil

	id := a.scan.AddRegion(scanner.Rect{L: rect.L, T: rect.T, R: rect.R, B: rect.B})
	if id > 0 {
		a.resumeAfter[id] = true
		// The new region is appended, so with enough regions it lands below the
		// fold. Reveal it immediately — otherwise the box the user just drew
		// gets no feedback at all.
		a.regions.scrollToEnd()
	}
	a.resumeAll()
	w32.Invalidate(a.hwnd)
}

// cancelSelect throws the selection away and restores the previous state.
func (a *app) cancelSelect() {
	if a.overlayHwnd != 0 {
		w32.ReleaseCapture()
		w32.DestroyWindow(a.overlayHwnd)
		a.overlayHwnd = 0
	}
	a.dragging = false
	a.selBuf = nil
	a.resumeAll()
	w32.Invalidate(a.hwnd)
}

// resumeAll restarts every region that was running when selection began.
func (a *app) resumeAll() {
	for id := range a.resumeAfter {
		if err := a.scan.Start(id); err != nil {
			a.setError("恢复扫描失败: " + err.Error())
		}
	}
	a.resumeAfter = map[int]bool{}
}

// paintOverlay draws the frozen desktop with everything outside the selection
// dimmed, so aiming the box is as easy as in any screenshot tool.
func (a *app) paintOverlay() {
	hwnd := a.overlayHwnd
	if hwnd == 0 {
		return
	}
	dc, end := w32.PaintDC(hwnd)
	if dc == nil {
		return
	}
	defer end()
	rc := w32.ClientRect(hwnd)
	if rc.W() <= 0 || rc.H() <= 0 {
		return
	}
	mem := w32.NewCompatibleDC(dc.Handle())
	if mem == nil {
		return
	}
	defer mem.Close()
	bmp := w32.NewCompatibleBitmap(dc.Handle(), rc.W(), rc.H())
	if bmp == nil {
		return
	}
	defer bmp.Close()
	mem.Select(bmp.Handle())

	a.drawScene(mem, rc)

	// Blit before restoring: Restore re-selects the DC's 1x1 placeholder bitmap,
	// and BitBlt copies whatever is selected, so restoring first blits nothing.
	dc.Blit(0, 0, rc.W(), rc.H(), mem, 0, 0)
	mem.Restore()
}

// drawScene paints the overlay content into a memory DC.
func (a *app) drawScene(dc *w32.DC, rc w32.Rect) {
	if a.selBuf != nil {
		dc.StretchDIBits(rc, a.selBuf, int32(a.selW), int32(a.selH))
	}
	if !a.dragging {
		hint := "拖拽框选二维码区域，Esc 取消"
		w, _ := dc.TextExtent(hint)
		// Dark plate behind the hint keeps it legible over bright desktops.
		dc.RoundRect(w32.Rect{L: 24, T: 24, R: 24 + w + 24, B: 24 + 36}, w32.RGB(0, 0, 0), 0, 8, 0)
		dc.SetTextColor(w32.RGB(255, 255, 255))
		dc.Text(w32.Rect{L: 32, T: 24, R: 32 + w, B: 60}, hint, w32.TextCenter)
		return
	}
	x0, y0 := a.selStart.X, a.selStart.Y
	x1, y1 := a.selCur.X, a.selCur.Y
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	a.dimOutside(dc, x0, y0, x1, y1)

	// Bright 2px selection border drawn as four bands (Ellipse/plumbing not
	// needed, and a real frame is easier to see than a line).
	edge := w32.RGB(0x00, 0xDC, 0x78)
	dc.FillRect(w32.Rect{L: x0, T: y0, R: x1, B: y0 + 2}, edge)
	dc.FillRect(w32.Rect{L: x0, T: y1 - 2, R: x1, B: y1}, edge)
	dc.FillRect(w32.Rect{L: x0, T: y0, R: x0 + 2, B: y1}, edge)
	dc.FillRect(w32.Rect{L: x1 - 2, T: y0, R: x1, B: y1}, edge)

	label := fmt.Sprintf("%d × %d", x1-x0, y1-y0)
	lw, lh := dc.TextExtent(label)
	w := lw + 16
	h := lh + 8
	top := y0 - h - 6
	if top < 4 {
		top = y0 + 6
	}
	dc.RoundRect(w32.Rect{L: x0, T: top, R: x0 + w, B: top + h}, w32.RGB(0x10, 0x14, 0x1C), edge, 6, 1)
	dc.SetTextColor(w32.RGB(255, 255, 255))
	dc.Text(w32.Rect{L: x0 + 8, T: top, R: x0 + w - 8, B: top + h}, label, w32.TextCenter)
}

// dimOutside darkens the four bands around the selection with a translucent
// black layer, brightening nothing so the user can still aim accurately.
func (a *app) dimOutside(dc *w32.DC, x0, y0, x1, y1 int32) {
	w := int32(a.selW)
	h := int32(a.selH)
	if w <= 0 || h <= 0 {
		return
	}
	src := w32.NewCompatibleDC(dc.Handle())
	if src == nil {
		return
	}
	defer src.Close()
	bmp := w32.NewCompatibleBitmap(dc.Handle(), w, h)
	if bmp == nil {
		return
	}
	defer bmp.Close()
	src.Select(bmp.Handle())
	src.FillRect(w32.Rect{L: 0, T: 0, R: w, B: h}, w32.RGB(0, 0, 0))

	const dim = 115
	dc.Alpha(0, 0, w, y0, src, 0, 0, w, y0, dim)
	dc.Alpha(0, y1, w, h-y1, src, 0, y1, w, h-y1, dim)
	dc.Alpha(0, y0, x0, y1-y0, src, 0, y0, x0, y1-y0, dim)
	dc.Alpha(x1, y0, w-x1, y1-y0, src, x1, y0, w-x1, y1-y0, dim)
	src.Restore()
}
