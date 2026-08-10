package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmAppRefresh = 0x0400 + 1
	wmAppDone    = 0x0400 + 2
)

const (
	wmPaint       = 0x000F
	wmEraseBkgnd  = 0x0014
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmMouseMove   = 0x0200
	wmKeyDown     = 0x0100

	vkEscape = 0x1B
	swShow   = 5

	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsVisible          = 0x10000000
	wsExTopmost        = 0x00000008
	wsExToolwindow     = 0x00000080

	hwndTopmost = uintptr(0xffffffff)

	swpShowWindow = 0x0040

	idcCross = 32515

	smXVirtualScreen = 76
	smYVirtualScreen = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79

	ofnOverwritePrompt = 0x0002
	ofnPathMustExist   = 0x00000800

	blackBrush = 4
)

func rgb(r, g, b uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

// scanRegion is one user-selected screen rectangle being scanned
// independently — the receiver supports several at once, one per monitor.
type scanRegion struct {
	rect     rect
	st       decodeState
	scanning bool
	serial   int
	done     bool
	label    string
}

type app struct {
	hwnd    uintptr
	hinst   uintptr
	btns    [5]rect
	btnText [5]string

	regions []*scanRegion

	mu       sync.Mutex
	latest   []byte // BGRA preview of the most recent captured frame
	latestW  int
	latestH  int
	preview  bitmapInfo

	overlayHwnd uintptr
	selecting   bool
	selBuf      []byte
	selW        int
	selH        int
	selInfo     bitmapInfo
	selStart    point
	selCur      point
	dragging    bool
}

func main() {
	runtime.LockOSThread()
	// System-level DPI awareness, NOT per-monitor: under per-monitor V2 the
	// selection overlay's client coords are physical pixels while
	// GetSystemMetrics' virtual screen is logical, mixing the two shifts the
	// captured region (you'd scan part of the QR). System DPI keeps screen
	// coordinates, BitBlt and GetSystemMetrics all in one coordinate space.
	procSetProcessDPIAware.Call()

	inst := getModuleHandle(nil)
	if inst == 0 {
		messageBox(0, "GetModuleHandle failed", "txqr-desktop", 0)
		return
	}
	a := &app{hinst: inst, btnText: [5]string{"添加区域", "开始全部", "停止全部", "清空", "保存"}}

	// main window class
	wc := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		style:         0,
		lpfnWndProc:   syscall.NewCallback(a.wndProc),
		hInstance:     inst,
		hCursor:       procCall(procLoadCursorW, 0, idcCross),
		hbrBackground: blackBrush,
		lpszClassName: utf16("QRTDesktopMain"),
	}
	if procCall(procRegisterClassExW, uintptr(unsafe.Pointer(&wc))) == 0 {
		messageBox(0, "RegisterClassEx failed", "txqr-desktop", 0)
		return
	}
	// overlay window class
	wc2 := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		style:         0,
		lpfnWndProc:   syscall.NewCallback(a.overlayProc),
		hInstance:     inst,
		hCursor:       procCall(procLoadCursorW, 0, idcCross),
		hbrBackground: blackBrush,
		lpszClassName: utf16("QRTDesktopSelect"),
	}
	if procCall(procRegisterClassExW, uintptr(unsafe.Pointer(&wc2))) == 0 {
		messageBox(0, "RegisterClassEx failed", "txqr-desktop", 0)
		return
	}

	hwnd := procCall(procCreateWindowExW, 0, uintptr(unsafe.Pointer(utf16("QRTDesktopMain"))),
		uintptr(unsafe.Pointer(utf16("QRT 桌面接收器"))),
		wsOverlappedWindow|wsVisible, 120, 90, 820, 640, 0, 0, inst, 0)
	if hwnd == 0 {
		messageBox(0, "CreateWindowEx failed", "txqr-desktop", 0)
		return
	}
	a.hwnd = hwnd
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)

	var m msg
	for {
		r := procCall(procGetMessageW, uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (a *app) wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmPaint:
		paintBuffered(hwnd, func(h uintptr) { a.paintMain(h) })
		return 0
	case wmEraseBkgnd:
		return 1
	case wmLButtonDown:
		p := pointFromLParam(lParam)
		if idx := a.hitButton(p); idx >= 0 {
			a.onButton(idx)
			return 0
		}
	case wmAppRefresh:
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmAppDone:
		a.saveDialog()
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		for _, r := range a.regions {
			a.stopRegion(r)
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	return procCall(procDefWindowProcW, hwnd, msg, wParam, lParam)
}

func pointFromLParam(lParam uintptr) point {
	return point{x: int32(int16(lParam & 0xFFFF)), y: int32(int16(lParam >> 16))}
}

func (a *app) onButton(idx int) {
	switch idx {
	case 0: // 添加区域
		a.startSelect()
	case 1: // 开始全部
		if len(a.regions) == 0 {
			messageBox(a.hwnd, "请先框选至少一个二维码区域", "提示", 0)
			return
		}
		for _, r := range a.regions {
			if !r.scanning && !r.done {
				a.startRegion(r)
			}
		}
	case 2: // 停止全部
		for _, r := range a.regions {
			a.stopRegion(r)
		}
	case 3: // 清空
		for _, r := range a.regions {
			a.stopRegion(r)
		}
		a.regions = nil
		a.mu.Lock()
		a.latest = nil
		a.mu.Unlock()
		procInvalidateRect.Call(a.hwnd, 0, 0)
	case 4: // 保存
		a.saveDialog()
	}
}

func (a *app) hitButton(p point) int {
	for i, r := range a.btns {
		if int(p.x) >= int(r.left) && int(p.x) < int(r.right) && int(p.y) >= int(r.top) && int(p.y) < int(r.bottom) {
			return i
		}
	}
	return -1
}

func (a *app) paintMain(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&rc)))
	cx := int(rc.right)
	cy := int(rc.bottom)

	// black backdrop
	brBg := createSolidBrush(rgb(16, 16, 20))
	defer deleteObject(brBg)
	fillRect(hdc, &rect{0, 0, int32(cx), int32(cy)}, brBg)

	// buttons
	brBtn := createSolidBrush(rgb(47, 61, 92))
	brBtnOn := createSolidBrush(rgb(38, 130, 60))
	brFrame := createSolidBrush(rgb(120, 140, 180))
	defer deleteObject(brBtn)
	defer deleteObject(brBtnOn)
	defer deleteObject(brFrame)

	setBkMode(hdc, transparent)
	setTextColor(hdc, rgb(235, 238, 245))
	x := 12
	for i, label := range a.btnText {
		r := rect{int32(x), 10, int32(x + 96), 44}
		a.btns[i] = r
		if i == 1 && a.anyScanning() {
			fillRect(hdc, &r, brBtnOn)
		} else {
			fillRect(hdc, &r, brBtn)
		}
		frameRect(hdc, &r, brFrame)
		t, tn := utf16N(label)
		tw, th := textExtent(hdc, t, tn)
		textOutW(hdc, x+(96-int(tw))/2, 10+(34-int(th))/2, t, tn)
		x += 108
	}
	// per-region status lines + a combined progress bar for the active one
	brTrack := createSolidBrush(rgb(40, 40, 48))
	brFill := createSolidBrush(rgb(47, 129, 247))
	brFillOn := createSolidBrush(rgb(38, 130, 60))
	defer deleteObject(brTrack)
	defer deleteObject(brFill)
	defer deleteObject(brFillOn)

	lineY := 56
	for _, r := range a.regions {
		p, st, done := r.st.snapshot()
		state := "扫描中"
		switch {
		case done:
			state = "完成"
		case !r.scanning:
			state = "已停止"
		}
		line := fmt.Sprintf("%s · %s · %s", r.label, state, st)
		ls, ln := utf16N(line)
		textOutW(hdc, 12, lineY, ls, ln)
		// small progress bar per region
		pr := rect{12, int32(lineY + 20), int32(cx - 12), int32(lineY + 34)}
		fillRect(hdc, &pr, brTrack)
		if p > 0 {
			fw := int32(int(pr.right-pr.left) * p / 100)
			if fw >= 1 {
				brush := brFill
				if done {
					brush = brFillOn
				}
				fillRect(hdc, &rect{pr.left, pr.top, pr.left + fw, pr.bottom}, brush)
			}
		}
		lineY += 42
	}

	// hint
	hint := "点击「添加区域」并框住屏幕上的二维码 — 支持多屏，每屏可各扫一路"
	if len(a.regions) == 0 {
		hint = "尚未添加扫描区域 — 点击「添加区域」框住屏幕上的二维码"
	}
	hs, hn := utf16N(hint)
	textOutW(hdc, 12, lineY+4, hs, hn)

	// preview area
	pv := rect{12, int32(lineY + 28), int32(cx - 12), int32(cy - 12)}
	brPV := createSolidBrush(rgb(0, 0, 0))
	fillRect(hdc, &pv, brPV)
	a.mu.Lock()
	lw, lh, lbuf := a.latestW, a.latestH, a.latest
	info := a.preview
	a.mu.Unlock()
	if lbuf != nil && lw > 0 && lh > 0 {
		// fit into pv preserving aspect
		dw, dh := int(pv.right-pv.left), int(pv.bottom-pv.top)
		scale := 1
		if lw > 0 && lh > 0 {
			s1, s2 := dw*100/lw, dh*100/lh
			if s2 < s1 {
				s1 = s2
			}
			scale = s1
			if scale < 1 {
				scale = 1
			}
		}
		ndw, ndh := lw*scale/100, lh*scale/100
		ox := int(pv.left) + (dw-ndw)/2
		oy := int(pv.top) + (dh-ndh)/2
		stretchDIBits(hdc, ox, oy, ndw, ndh, 0, 0, lw, lh, uintptr(unsafe.Pointer(&lbuf[0])), &info)
	}
	deleteObject(brPV)
}

func (a *app) anyScanning() bool {
	for _, r := range a.regions {
		if r.scanning {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ capture loop

func (a *app) startRegion(r *scanRegion) {
	if r.scanning || r.done {
		return
	}
	r.st.reset()
	r.scanning = true
	r.serial++
	gen := r.serial
	go a.runCapture(r, gen)
}

func (a *app) stopRegion(r *scanRegion) {
	if !r.scanning {
		return
	}
	r.scanning = false
	r.serial++
}

func (a *app) runCapture(r *scanRegion, gen int) {
	for {
		if !r.scanning || gen != r.serial {
			return
		}
		rc := r.rect
		if rc.right <= rc.left || rc.bottom <= rc.top {
			return
		}
		start := time.Now()
		rgba, bgra, err := captureRegion(int(rc.left), int(rc.top), int(rc.right-rc.left), int(rc.bottom-rc.top))
		if err == nil {
			w, h := int(rc.right-rc.left), int(rc.bottom-rc.top)
			a.mu.Lock()
			a.latest = bgra
			a.latestW = w
			a.latestH = h
			a.preview = bitmapInfo{bmiHeader: bitmapInfoHeader{
				biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
				biWidth:    int32(w),
				biHeight:   -int32(h),
				biPlanes:   1,
				biBitCount: 32,
			}}
			a.mu.Unlock()
			done := r.st.feed(rgba)
			procPostMessageW.Call(a.hwnd, wmAppRefresh, 0, 0)
			if done {
				r.done = true
				a.stopRegion(r)
				procPostMessageW.Call(a.hwnd, wmAppDone, 0, 0)
				return
			}
		}
		elapsed := time.Since(start)
		if elapsed < 30*time.Millisecond {
			time.Sleep(30*time.Millisecond - elapsed)
		}
	}
}

// ------------------------------------------------------------ selection overlay

func virtualScreen() (int, int, int, int) {
	x := int(procCall(procGetSystemMetrics, smXVirtualScreen))
	y := int(procCall(procGetSystemMetrics, smYVirtualScreen))
	w := int(procCall(procGetSystemMetrics, smCxVirtualScreen))
	h := int(procCall(procGetSystemMetrics, smCyVirtualScreen))
	return x, y, w, h
}

func (a *app) startSelect() {
	for _, r := range a.regions {
		a.stopRegion(r)
	}
	vx, vy, vw, vh := virtualScreen()
	_, buf, err := captureRegion(vx, vy, vw, vh)
	if err != nil {
		messageBox(a.hwnd, "屏幕捕获失败: "+err.Error(), "错误", 0)
		return
	}
	a.selBuf = buf
	a.selW, a.selH = vw, vh
	a.selInfo = bitmapInfo{bmiHeader: bitmapInfoHeader{
		biSize:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:    int32(vw),
		biHeight:   -int32(vh),
		biPlanes:   1,
		biBitCount: 32,
	}}
	a.selecting = true
	a.dragging = false

	hwnd := procCall(procCreateWindowExW, wsExTopmost|wsExToolwindow,
		uintptr(unsafe.Pointer(utf16("QRTDesktopSelect"))),
		uintptr(unsafe.Pointer(utf16("框选二维码区域"))),
		wsPopup|wsVisible, uintptr(vx), uintptr(vy), uintptr(vw), uintptr(vh), 0, 0, a.hinst, 0)
	if hwnd == 0 {
		messageBox(a.hwnd, "创建选框失败", "错误", 0)
		a.selecting = false
		return
	}
	a.overlayHwnd = hwnd
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
}

func (a *app) overlayProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmPaint:
		paintBuffered(hwnd, func(h uintptr) { a.paintOverlay(h) })
		return 0
	case wmEraseBkgnd:
		return 1
	case wmLButtonDown:
		a.dragging = true
		a.selStart = pointFromLParam(lParam)
		a.selCur = a.selStart
		procSetCapture.Call(hwnd)
		return 0
	case wmMouseMove:
		if a.dragging {
			a.selCur = pointFromLParam(lParam)
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmLButtonUp:
		if a.dragging {
			a.dragging = false
			procReleaseCapture.Call()
			a.selCur = pointFromLParam(lParam)
			a.finishSelect()
		}
		return 0
	case wmKeyDown:
		if wParam == vkEscape {
			a.cancelSelect()
		}
		return 0
	case wmClose:
		a.cancelSelect()
		return 0
	}
	return procCall(procDefWindowProcW, hwnd, msg, wParam, lParam)
}

func (a *app) finishSelect() {
	vx, vy, _, _ := virtualScreen()
	x0, y0 := a.selStart.x, a.selStart.y
	x1, y1 := a.selCur.x, a.selCur.y
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	if x1-x0 < 8 || y1-y0 < 8 {
		a.cancelSelect()
		return
	}
	rc := rect{
		int32(x0 + int32(vx)), int32(y0 + int32(vy)), int32(x1 + int32(vx)), int32(y1 + int32(vy)),
	}
	procDestroyWindow.Call(a.overlayHwnd)
	a.overlayHwnd = 0
	a.selecting = false

	r := &scanRegion{
		rect:  rc,
		label: fmt.Sprintf("区域 %d · %dx%d", len(a.regions)+1, int(rc.right-rc.left), int(rc.bottom-rc.top)),
	}
	a.regions = append(a.regions, r)
	procInvalidateRect.Call(a.hwnd, 0, 0)
	// selection paused every region (the fullscreen overlay blocks captures);
	// resume every unfinished one so multi-monitor streams keep flowing.
	for _, rr := range a.regions {
		a.startRegion(rr)
	}
}

func (a *app) cancelSelect() {
	if a.overlayHwnd != 0 {
		procDestroyWindow.Call(a.overlayHwnd)
		a.overlayHwnd = 0
	}
	a.selecting = false
	a.dragging = false
}

func (a *app) paintOverlay(hdc uintptr) {
	if a.selBuf == nil {
		return
	}
	stretchDIBits(hdc, 0, 0, a.selW, a.selH, 0, 0, a.selW, a.selH,
		uintptr(unsafe.Pointer(&a.selBuf[0])), &a.selInfo)

	brDim := createSolidBrush(rgb(28, 28, 34))
	brSel := createSolidBrush(rgb(0, 220, 120))
	defer deleteObject(brDim)
	defer deleteObject(brSel)

	if !a.dragging {
		setBkMode(hdc, transparent)
		setTextColor(hdc, rgb(255, 255, 255))
		hintText := "拖拽框选二维码区域，Esc 取消"
		hint, hn := utf16N(hintText)
		textOutW(hdc, 24, 24, hint, hn)
	}

	var sx0, sy0, sx1, sy1 int32
	if a.dragging {
		sx0, sy0 = int32(a.selStart.x), int32(a.selStart.y)
		sx1, sy1 = int32(a.selCur.x), int32(a.selCur.y)
		if sx0 > sx1 {
			sx0, sx1 = sx1, sx0
		}
		if sy0 > sy1 {
			sy0, sy1 = sy1, sy0
		}
		// dim outside
		fillRect(hdc, &rect{0, 0, int32(a.selW), sy0}, brDim)
		fillRect(hdc, &rect{0, sy1, int32(a.selW), int32(a.selH)}, brDim)
		fillRect(hdc, &rect{0, sy0, sx0, sy1}, brDim)
		fillRect(hdc, &rect{sx1, sy0, int32(a.selW), sy1}, brDim)
		frameRect(hdc, &rect{sx0, sy0, sx1, sy1}, brSel)
	}
}

// ------------------------------------------------------------ save

func (a *app) saveDialog() {
	var result []byte
	var name string
	done := false
	for _, r := range a.regions {
		r.st.mu.Lock()
		if r.st.completed {
			result, name, done = r.st.result, r.st.fileName, true
			r.st.mu.Unlock()
			break
		}
		r.st.mu.Unlock()
	}
	if !done || result == nil {
		messageBox(a.hwnd, "还没有完成的传输", "提示", 0)
		return
	}
	suggested := name
	if suggested == "" {
		suggested = fmt.Sprintf("received_%d.bin", time.Now().Unix())
	}

	var buf [1024]uint16
	copy(buf[:], utf16Buf(suggested))
	ofn := openfilename{
		lStructSize: uint32(unsafe.Sizeof(openfilename{})),
		hwndOwner:   a.hwnd,
		lpstrFilter: fileFilter("所有文件", "*.*"),
		lpstrFile:   &buf[0],
		nMaxFile:    1024,
		lpstrDefExt: utf16("bin"),
		Flags:       ofnOverwritePrompt | ofnPathMustExist,
	}
	if !getSaveFileName(&ofn) {
		return
	}
	path := windows.UTF16ToString(buf[:])
	if err := os.WriteFile(path, result, 0o644); err != nil {
		messageBox(a.hwnd, "保存失败: "+err.Error(), "错误", 0)
		return
	}
	messageBox(a.hwnd, "已保存: "+path, "完成", 0)
}

func utf16Buf(s string) []uint16 {
	p, _ := syscall.UTF16FromString(s)
	return p
}

// fileFilter builds a "name\0pattern\0\0" double-NUL-terminated UTF-16 filter.
func fileFilter(name, pattern string) *uint16 {
	var u []uint16
	for _, part := range []string{name, pattern} {
		u = append(u, utf16Buf(part)...)
		u = append(u, 0)
	}
	u = append(u, 0)
	return &u[0]
}
