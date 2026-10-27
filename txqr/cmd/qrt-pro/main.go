// Command qrt-pro is the unified QRT desktop application: one executable that
// hosts both the web sender (an HTTP service + browser carousel) and the screen
// receiver (multi-region QR scanning) behind a single window.
//
// Previous releases shipped three binaries — 序列化工具.exe (sender),
// qrt-pro-receiver-desktop-*.exe (receiver) and qrt-pro-launcher.exe (a process
// manager for the other two). They were separate processes talking only through
// process names, which is where the naming bugs came from. Running everything
// in one process removes that coupling: stopping a scan is a method call, not a
// process tree walk, and there is exactly one message loop to keep alive.
//
//	go build -ldflags "-s -w -H windowsgui" ./cmd/qrt-pro
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/divan/txqr/internal/scanner"
	"github.com/divan/txqr/internal/w32"
)

const (
	// Class names identify the two window classes this app registers.
	mainClass    = "QRTPRoMain"
	overlayClass = "QRTPRoOverlay"
)

func main() {
	// The window procedure must always run on the same OS thread: every Win32
	// call below assumes thread affinity (timers, capture, callbacks).
	runtime.LockOSThread()
	w32.InitDPI()

	// Headless rendering mode: draws the UI off-screen and writes a PNG, which
	// is how screenshots get produced for docs (and how layout regressions are
	// checked on a machine with no visible desktop).
	snapshot := flag.String("snapshot", "", "把当前界面渲染到指定 PNG 后退出")
	tab := flag.Int("tab", 0, "渲染哪个页面：0 发送端，1 接收端，2 已收文件")
	demo := flag.Bool("demo", false, "加两个示例区域，便于出图")
	width := flag.Int("w", 1080, "渲染宽度（像素，含窗口边框）")
	height := flag.Int("h", 720, "渲染高度（像素，含窗口边框）")
	flag.Parse()

	if *snapshot != "" {
		if err := renderSnapshot(*snapshot, *tab, *demo, int32(*width)-16, int32(*height)-39); err != nil {
			fmt.Fprintln(os.Stderr, "snapshot:", err)
			os.Exit(1)
		}
		return
	}

	tracePath = os.Getenv("QRT_PAINT_CHECK")

	inst := w32.Instance()
	if inst == 0 {
		w32.MessageBox(0, "无法获取模块句柄", "QRT", w32.MBIconError)
		return
	}

	a := newApp(inst)
	if !a.createWindows() {
		return
	}
	w32.ShowAndUpdate(a.hwnd)
	a.run()
}

// renderSnapshot builds a throwaway app (no window), optionally seeds example
// rows, renders one tab and writes it to path.
func renderSnapshot(path string, tab int, demo bool, w, h int32) error {
	a := newApp(w32.Instance())
	defer a.shutdown()
	if tab >= 0 && tab <= 2 {
		a.tab = tab
	}
	if demo {
		// Five regions on purpose: the list only fits three, so the demo also
		// exercises the scroll viewport and its scrollbar.
		for _, r := range []scanner.Rect{
			{L: 320, T: 260, R: 720, B: 610},
			{L: 1180, T: 300, R: 1560, B: 640},
			{L: 100, T: 800, R: 520, B: 1040},
			{L: 780, T: 720, R: 1180, B: 980},
			{L: 1600, T: 640, R: 1880, B: 900},
		} {
			a.scan.AddRegion(r)
		}
	}
	a.mu.Lock()
	a.status = "就绪 · 示例数据"
	a.mu.Unlock()
	return a.Snapshot(path, w, h)
}

// run pumps messages until WM_QUIT, then releases every resource the app owns.
func (a *app) run() {
	var m w32.Msg
	for {
		cont, bad := w32.GetMessage(&m)
		if !cont {
			break
		}
		if bad {
			// GetMessage failed (invalid window handle). Bail out rather than
			// spinning forever, but still clean up.
			break
		}
		w32.TranslateAndDispatch(&m)
	}
	a.shutdown()
}

// wndProc is the main window's message router. Handlers stay thin: anything
// touching model state lives in app or its views.
func (a *app) wndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	traceMsg(hwnd, msg)
	switch msg {
	case w32.WMPaint:
		a.paintMain()
		return 0
	case w32.WMEraseBkgnd:
		// Everything is drawn into an off-screen buffer, so letting DefWindowProc
		// erase first would only introduce the flicker we are avoiding.
		return 1
	case w32.WMTimer:
		a.onTimer()
		return 0
	case w32.WMMouseMove:
		a.onMouseMove(w32.LParamPoint(lParam))
		return 0
	case w32.WMLButtonDown:
		a.onPress(w32.LParamPoint(lParam))
		return 0
	case w32.WMLButtonUp:
		a.onRelease(w32.LParamPoint(lParam))
		return 0
	case w32.WMMouseWheel:
		// WM_MOUSEWHEEL reports *screen* coordinates, unlike the button
		// messages, so it has to be mapped before the hit test.
		a.onWheel(w32.WheelDelta(wParam), w32.ScreenToClient(hwnd, w32.LParamPoint(lParam)))
		return 0
	case w32.WMSetCursor:
		a.applyCursor()
		return 1
	case w32.WMSize:
		a.enforceMinSize()
		w32.Invalidate(hwnd)
		return 0
	case wmAppRefresh:
		w32.Invalidate(hwnd)
		return 0
	case w32.WMClose:
		w32.DestroyWindow(hwnd)
		return 0
	case w32.WMDestroy:
		w32.PostQuitMessage(0)
		return 0
	}
	return w32.DefWindowProc(hwnd, msg, wParam, lParam)
}

// enforceMinSize keeps the layout usable below a minimum client size. It is
// implemented by snapping the outer window back instead of handling
// WM_GETMINMAXINFO, because that requires reinterpreting an lParam pointer the
// Go GC does not track (and `go vet` rightly flags as unsafe).
func (a *app) enforceMinSize() {
	if a.sizing {
		return // the SetWindowPos below re-enters WM_SIZE exactly once.
	}
	a.sizing = true
	defer func() { a.sizing = false }()

	outer := w32.WindowRect(a.hwnd)
	inner := w32.ClientRect(a.hwnd)
	frameW, frameH := outer.W()-inner.W(), outer.H()-inner.H()
	if frameW < 0 {
		frameW = 0
	}
	if frameH < 0 {
		frameH = 0
	}
	minW, minH := w32.Scale(880), w32.Scale(600)
	if inner.W() >= minW && inner.H() >= minH {
		return
	}
	w, h := minW, minH
	if inner.W() > w {
		w = inner.W()
	}
	if inner.H() > h {
		h = inner.H()
	}
	w32.SetWindowPos(a.hwnd, outer.L, outer.T, w+frameW, h+frameH, 0)
}
