// Command txqr-launcher is a tiny Windows tray-less GUI for starting and
// stopping the QRT sender and desktop receiver processes that live next to it
// in the same folder. Pure Win32, no third-party GUI dependency.
//
//		go build -ldflags "-s -w -H windowsgui" ./cmd/txqr-launcher
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procGetModuleFileNameW = kernel32.NewProc("GetModuleFileNameW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procSetTimer         = user32.NewProc("SetTimer")
	procKillTimer        = user32.NewProc("KillTimer")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")

	procShellExecuteW = shell32.NewProc("ShellExecuteW")

	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procTerminateProcess         = kernel32.NewProc("TerminateProcess")
	procCloseHandle              = kernel32.NewProc("CloseHandle")

	procCreateCompatibleDC     = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateCompatibleDC")
	procDeleteDC               = windows.NewLazySystemDLL("gdi32.dll").NewProc("DeleteDC")
	procCreateCompatibleBitmap = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateCompatibleBitmap")
	procSelectObject           = windows.NewLazySystemDLL("gdi32.dll").NewProc("SelectObject")
	procDeleteObject           = windows.NewLazySystemDLL("gdi32.dll").NewProc("DeleteObject")
	procBitBlt                 = windows.NewLazySystemDLL("gdi32.dll").NewProc("BitBlt")
	procCreateSolidBrush       = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateSolidBrush")
	procFillRect               = user32.NewProc("FillRect")
	procFrameRect              = user32.NewProc("FrameRect")
	procSetTextColor           = windows.NewLazySystemDLL("gdi32.dll").NewProc("SetTextColor")
	procSetBkMode              = windows.NewLazySystemDLL("gdi32.dll").NewProc("SetBkMode")
	procTextOutW               = windows.NewLazySystemDLL("gdi32.dll").NewProc("TextOutW")
	procGetTextExtentPoint32W  = windows.NewLazySystemDLL("gdi32.dll").NewProc("GetTextExtentPoint32W")
	procGetClientRect          = user32.NewProc("GetClientRect")
)

const (
	th32csSnapProcess = 0x2
	processTerminate  = 0x0001
	swShow            = 5
	wsOverlapped      = 0x00CF0000
	wsVisible         = 0x10000000
	srcCopy           = 0x00CC0020
	blackBrush        = 4

	wmPaint       = 0x000F
	wmEraseBkgnd  = 0x0014
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmLButtonDown = 0x0201
	wmTimer       = 0x0113
	timerRefresh  = 1
)

const (
	idxSenderStart = 0
	idxSenderStop  = 1
	idxRecvStart   = 2
	idxRecvStop    = 3
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

type rect struct{ left, top, right, bottom int32 }

type paintstruct struct {
	hdc        uintptr
	fErase     int32
	rcPaint    rect
	fRestore   int32
	fIncUpdate int32
	rgbReserved [32]byte
}

type processEntry32 struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [260]uint16
}

type launcher struct {
	hwnd    uintptr
	hinst   uintptr
	dir     string
	btns    [4]rect
	sender  bool // running
	receiver bool
}

func procCall(p *windows.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

func utf16N(s string) (*uint16, int) {
	u, _ := syscall.UTF16FromString(s)
	return &u[0], len(u) - 1
}

func main() {
	runtime.LockOSThread()
	procSetProcessDPIAware.Call()

	inst := procCall(procGetModuleHandleW, 0)
	if inst == 0 {
		return
	}
	a := &launcher{hinst: inst}

	exe, err := os.Executable()
	if err == nil {
		a.dir = filepath.Dir(exe)
	} else {
		var buf [1024]uint16
		n := procCall(procGetModuleFileNameW, 0, uintptr(unsafe.Pointer(&buf[0])), 1024)
		if n > 0 {
			a.dir = filepath.Dir(windows.UTF16ToString(buf[:n]))
		}
	}

	wc := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   syscall.NewCallback(a.wndProc),
		hInstance:     inst,
		hbrBackground: blackBrush,
		lpszClassName: utf16Ptr("QRTLauncher"),
	}
	procCall(procRegisterClassExW, uintptr(unsafe.Pointer(&wc)))

	hwnd := procCall(procCreateWindowExW, 0,
		uintptr(unsafe.Pointer(utf16Ptr("QRTLauncher"))),
		uintptr(unsafe.Pointer(utf16Ptr("QRT 启动器"))),
		wsOverlapped|wsVisible, 200, 160, 460, 240, 0, 0, inst, 0)
	if hwnd == 0 {
		return
	}
	a.hwnd = hwnd
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	procSetTimer.Call(hwnd, timerRefresh, 1000, 0)

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

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func (a *launcher) wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps paintstruct
		hdc := procCall(procBeginPaint, hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			a.paint(hdc)
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case wmEraseBkgnd:
		return 1
	case wmLButtonDown:
		x, y := pointFromLParam(lParam)
		if i := a.hit(x, y); i >= 0 {
			a.onButton(i)
		}
		return 0
	case wmTimer:
		if wParam == timerRefresh {
			a.refresh()
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, timerRefresh)
		procPostQuitMessage.Call(0)
		return 0
	}
	return procCall(procDefWindowProcW, hwnd, msg, wParam, lParam)
}

func pointFromLParam(l uintptr) (int, int) {
	return int(int16(l & 0xFFFF)), int(int16(l >> 16))
}

func (a *launcher) hit(x, y int) int {
	for i, r := range a.btns {
		if x >= int(r.left) && x < int(r.right) && y >= int(r.top) && y < int(r.bottom) {
			return i
		}
	}
	return -1
}

func (a *launcher) senderExe() string  { return filepath.Join(a.dir, "qrt-pro-sender-windows-amd64-v1.3.0.exe") }
func (a *launcher) receiverExe() string { return filepath.Join(a.dir, "qrt-pro-receiver-desktop-windows-amd64-v1.3.0.exe") }

func (a *launcher) onButton(i int) {
	switch i {
	case idxSenderStart:
		startProcess(a.hwnd, a.senderExe(), a.dir)
	case idxSenderStop:
		stopProcess("qrt-pro-sender")
	case idxRecvStart:
		startProcess(a.hwnd, a.receiverExe(), a.dir)
	case idxRecvStop:
		stopProcess("qrt-pro-receiver")
	}
	a.refresh()
	procInvalidateRect.Call(a.hwnd, 0, 0)
}

func (a *launcher) refresh() {
	a.sender = exeRunning("qrt-pro-sender")
	a.receiver = exeRunning("qrt-pro-receiver")
}

// exeRunning reports whether any process's module name starts with prefix.
func exeRunning(prefix string) bool {
	snap := procCall(procCreateToolhelp32Snapshot, th32csSnapProcess, 0)
	if snap == uintptr(^uintptr(0)) {
		return false
	}
	defer procCloseHandle.Call(snap)
	var pe processEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if procCall(procProcess32FirstW, snap, uintptr(unsafe.Pointer(&pe))) == 0 {
		return false
	}
	for {
		name := windows.UTF16ToString(pe.szExeFile[:])
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return true
		}
		if procCall(procProcess32NextW, snap, uintptr(unsafe.Pointer(&pe))) == 0 {
			return false
		}
	}
}

// startProcess launches exe with ShellExecute (GUI subsystem, no console).
func startProcess(hwnd uintptr, exe, dir string) {
	procShellExecuteW.Call(hwnd,
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr(exe))),
		0,
		uintptr(unsafe.Pointer(utf16Ptr(dir))),
		swShow)
}

// stopProcess terminates every process whose module name starts with prefix.
func stopProcess(prefix string) {
	snap := procCall(procCreateToolhelp32Snapshot, th32csSnapProcess, 0)
	if snap == uintptr(^uintptr(0)) {
		return
	}
	defer procCloseHandle.Call(snap)
	var pe processEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if procCall(procProcess32FirstW, snap, uintptr(unsafe.Pointer(&pe))) == 0 {
		return
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(pe.szExeFile[:]))
		if strings.HasPrefix(name, prefix) {
			h := procCall(procOpenProcess, processTerminate, 0, uintptr(pe.th32ProcessID))
			if h != 0 {
				procTerminateProcess.Call(h, 1)
				procCloseHandle.Call(h)
			}
		}
		if procCall(procProcess32NextW, snap, uintptr(unsafe.Pointer(&pe))) == 0 {
			return
		}
	}
}

func (a *launcher) paint(hdc uintptr) {
	brBg := procCall(procCreateSolidBrush, uintptr(rgb(18, 18, 24)))
	fillRect(hdc, 0, 0, 10000, 10000, brBg)
	procDeleteObject.Call(brBg)

	brBtn := procCall(procCreateSolidBrush, uintptr(rgb(47, 61, 92)))
	brOn := procCall(procCreateSolidBrush, uintptr(rgb(38, 130, 60)))
	brOff := procCall(procCreateSolidBrush, uintptr(rgb(90, 46, 46)))
	brFrame := procCall(procCreateSolidBrush, uintptr(rgb(120, 140, 180)))
	defer procDeleteObject.Call(brBtn)
	defer procDeleteObject.Call(brOn)
	defer procDeleteObject.Call(brOff)
	defer procDeleteObject.Call(brFrame)

	setBkMode(hdc, 1) // transparent
	setTextColor(hdc, rgb(235, 238, 245))

	title, tn := utf16N("QRT 进程启动器")
	textOutW(hdc, 14, 10, title, tn)

	rows := []struct {
		label   string
		running bool
		rowY    int
	}{
		{"发送端 (txqr-web · :9000)", a.sender, 44},
		{"桌面接收端 (框选扫描)", a.receiver, 92},
	}
	for ri, row := range rows {
		// label
		l, ln := utf16N(row.label)
		textOutW(hdc, 14, row.rowY+6, l, ln)

		// start button
		bs := rect{int32(230), int32(row.rowY), int32(312), int32(row.rowY + 32)}
		// stop button
		bp := rect{int32(322), int32(row.rowY), int32(404), int32(row.rowY + 32)}
		a.btns[ri*2] = bs
		a.btns[ri*2+1] = bp

		fillRect(hdc, int(bs.left), int(bs.top), int(bs.right), int(bs.bottom), brBtn)
		frameRect(hdc, int(bs.left), int(bs.top), int(bs.right), int(bs.bottom), brFrame)
		sl, sln := utf16N("启动")
		tw, th := textExtent(hdc, sl, sln)
		textOutW(hdc, int(bs.left)+(82-int(tw))/2, int(bs.top)+(32-int(th))/2, sl, sln)

		stopBrush := brBtn
		if row.running {
			stopBrush = brOn
		}
		fillRect(hdc, int(bp.left), int(bp.top), int(bp.right), int(bp.bottom), stopBrush)
		frameRect(hdc, int(bp.left), int(bp.top), int(bp.right), int(bp.bottom), brFrame)
		pl, pln := utf16N("停止")
		tw, th = textExtent(hdc, pl, pln)
		textOutW(hdc, int(bp.left)+(82-int(tw))/2, int(bp.top)+(32-int(th))/2, pl, pln)

		// status
		st := "已停止"
		stColor := rgb(150, 150, 160)
		if row.running {
			st = "● 运行中"
			stColor = rgb(80, 220, 120)
		}
		setTextColor(hdc, stColor)
		sl2, sln2 := utf16N(st)
		textOutW(hdc, 416, row.rowY+7, sl2, sln2)
		setTextColor(hdc, rgb(235, 238, 245))
	}

	hint, hn := utf16N("提示：停止只匹配本工具的进程；launcher 自身不会被停止。")
	setTextColor(hdc, rgb(130, 130, 150))
	textOutW(hdc, 14, 150, hint, hn)
}

// ---- tiny GDI shims with plain int coords so paint() stays readable ----

func rgb(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

func fillRect(hdc uintptr, x1, y1, x2, y2 int, br uintptr) {
	rc := rect{int32(x1), int32(y1), int32(x2), int32(y2)}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
}

func frameRect(hdc uintptr, x1, y1, x2, y2 int, br uintptr) {
	rc := rect{int32(x1), int32(y1), int32(x2), int32(y2)}
	procFrameRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
}

func setBkMode(hdc uintptr, mode int) { procSetBkMode.Call(hdc, uintptr(mode)) }

func setTextColor(hdc uintptr, c uint32) { procSetTextColor.Call(hdc, uintptr(c)) }

func textOutW(hdc uintptr, x, y int, s *uint16, n int) {
	procTextOutW.Call(hdc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(s)), uintptr(n))
}

func textExtent(hdc uintptr, s *uint16, n int) (int32, int32) {
	var sz struct{ cx, cy int32 }
	procGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(s)), uintptr(n), uintptr(unsafe.Pointer(&sz)))
	return sz.cx, sz.cy
}
