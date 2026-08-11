// Command txqr-launcher is a tiny Windows tray-less GUI for starting and
// stopping the QRT sender and desktop receiver processes that live next to it
// in the same folder. Pure Win32, no third-party GUI dependency.
//
//		go build -ldflags "-s -w -H windowsgui" ./cmd/txqr-launcher
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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
	procPostMessageW     = user32.NewProc("PostMessageW")
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
	procEllipse                = windows.NewLazySystemDLL("gdi32.dll").NewProc("Ellipse")
)

const (
	th32csSnapProcess = 0x2
	processTerminate  = 0x0001
	swShow            = 5
	wsOverlapped      = 0x00CF0000
	wsVisible         = 0x10000000
	srcCopy           = 0x00CC0020
	blackBrush        = 4

	wmPaint         = 0x000F
	wmEraseBkgnd    = 0x0014
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmMouseUp       = 0x0202
	wmTimer         = 0x0113
	wmGetMinMaxInfo = 0x0024
	timerRefresh    = 1
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

type point struct{ x, y int32 }

type minmaxinfo struct {
	ptReserved    point
	ptMaxSize     point
	ptMaxPosition point
	ptMinTrackSize point
	ptMaxTrackSize point
}

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
	pressed int // button index currently pressed (for press feedback)

	mu            sync.Mutex // guards the process/port snapshot
	sender        bool
	receiver      bool
	senderPID     uint32
	receiverPID   uint32
	senderPorts   []int // cached by refresh(); paint never spawns netstat
	receiverPorts []int
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
		wsOverlapped|wsVisible, 200, 140, 480, 300, 0, 0, inst, 0)
	if hwnd == 0 {
		return
	}
	a.hwnd = hwnd
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	procSetTimer.Call(hwnd, timerRefresh, 2000, 0)

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

func (a *launcher) wndProc(hwnd, msg, wParam uintptr, lParam unsafe.Pointer) uintptr {
	switch msg {
	case wmPaint:
		var ps paintstruct
		hdc := procCall(procBeginPaint, hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			a.paintBuffered(hdc)
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case wmEraseBkgnd:
		return 1
	case wmLButtonDown:
		x, y := pointFromLParam(lParam)
		if i := a.hit(x, y); i >= 0 {
			a.pressed = i
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmMouseUp:
		x, y := pointFromLParam(lParam)
		i := a.hit(x, y)
		if a.pressed >= 0 && i == a.pressed {
			a.onButton(i)
		}
		if a.pressed >= 0 {
			a.pressed = -1
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmTimer:
		if wParam == timerRefresh {
			a.refresh()
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmGetMinMaxInfo:
		mm := (*minmaxinfo)(lParam)
		mm.ptMinTrackSize = point{x: 480, y: 280}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, timerRefresh)
		procPostQuitMessage.Call(0)
		return 0
	}
	return procCall(procDefWindowProcW, hwnd, msg, wParam, uintptr(lParam))
}

func pointFromLParam(l unsafe.Pointer) (int, int) {
	v := uintptr(l)
	return int(int16(v & 0xFFFF)), int(int16(v >> 16))
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
	// Refresh off the UI thread so clicking never blocks on netstat; the
	// next paint picks the new snapshot up when it's ready.
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.refresh()
		procPostMessageW.Call(a.hwnd, wmTimer, timerRefresh, 0)
	}()
	procInvalidateRect.Call(a.hwnd, 0, 0)
}

func (a *launcher) refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sender, a.senderPID = exeRunning("qrt-pro-sender")
	a.receiver, a.receiverPID = exeRunning("qrt-pro-receiver")
	if a.sender {
		a.senderPorts = listenPorts(a.senderPID)
	} else {
		a.senderPorts = nil
	}
	if a.receiver {
		a.receiverPorts = listenPorts(a.receiverPID)
	} else {
		a.receiverPorts = nil
	}
}

// exeRunning reports whether any process's module name starts with prefix, and
// returns the first matching PID.
func exeRunning(prefix string) (bool, uint32) {
	snap := procCall(procCreateToolhelp32Snapshot, th32csSnapProcess, 0)
	if snap == uintptr(^uintptr(0)) {
		return false, 0
	}
	defer procCloseHandle.Call(snap)
	var pe processEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if procCall(procProcess32FirstW, snap, uintptr(unsafe.Pointer(&pe))) == 0 {
		return false, 0
	}
	for {
		name := windows.UTF16ToString(pe.szExeFile[:])
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return true, pe.th32ProcessID
		}
		if procCall(procProcess32NextW, snap, uintptr(unsafe.Pointer(&pe))) == 0 {
			return false, 0
		}
	}
}

// listenPorts returns the TCP listen ports owned by pid, parsed from
// `netstat -ano -p tcp` (covers both IPv4 and IPv6 — Go's ":9000" binds IPv6).
// CREATE_NO_WINDOW keeps the console netstat spawns from flashing a black box
// over this GUI app.
func listenPorts(pid uint32) []int {
	if pid == 0 {
		return nil
	}
	cmd := exec.Command("netstat", "-ano", "-p", "tcp")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	pidStr := strconv.Itoa(int(pid))
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[3] != "LISTENING" || f[4] != pidStr {
			continue
		}
		local := f[1] // e.g. "0.0.0.0:9000" or "[::]:9000"
		if i := strings.LastIndexByte(local, ':'); i >= 0 {
			if p, err := strconv.Atoi(local[i+1:]); err == nil {
				ports = append(ports, p)
			}
		}
	}
	return ports
}

// exeMeta extracts the version (from the filename, e.g. v1.3.0) and build
// time (file modification time) of an exe for display.
func exeMeta(exe string) (version, buildTime string) {
	base := filepath.Base(exe)
	if m := versionRe.FindString(base); m != "" {
		version = m
	}
	if st, err := os.Stat(exe); err == nil {
		buildTime = st.ModTime().Format("2006-01-02 15:04")
	}
	return version, buildTime
}

var versionRe = regexp.MustCompile(`v\d+\.\d+(\.\d+)?`)

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

func (a *launcher) paintBuffered(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := int(rc.right), int(rc.bottom)
	if w <= 0 || h <= 0 {
		return
	}
	mem := procCall(procCreateCompatibleDC, hdc)
	if mem == 0 {
		return
	}
	defer procDeleteDC.Call(mem)
	bmp := procCall(procCreateCompatibleBitmap, hdc, uintptr(w), uintptr(h))
	if bmp == 0 {
		return
	}
	defer procDeleteObject.Call(bmp)
	old := procCall(procSelectObject, mem, bmp)
	a.paint(mem)
	procBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, uintptr(srcCopy))
	procSelectObject.Call(mem, old)
}

func (a *launcher) paint(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := int(rc.right), int(rc.bottom)

	// gradient background
	for y := 0; y < h; y += 4 {
		t := float64(y) / float64(max(1, h))
		br := procCall(procCreateSolidBrush, uintptr(lerpColor(rgb(31, 35, 48), rgb(12, 13, 19), t)))
		fillRect(hdc, 0, y, w, y+4, br)
		procDeleteObject.Call(br)
	}

	setBkMode(hdc, 1) // transparent

	// centered title + separator
	title, tn := utf16N("QRT 进程启动器")
	tw, _ := textExtent(hdc, title, tn)
	setTextColor(hdc, rgb(240, 242, 248))
	textOutW(hdc, (w-int(tw))/2, 18, title, tn)
	brSep := procCall(procCreateSolidBrush, uintptr(rgb(70, 92, 150)))
	fillRect(hdc, 24, 50, w-24, 52, brSep)
	procDeleteObject.Call(brSep)

	rows := []struct {
		label   string
		exe     string
		running bool
		pid     uint32
		ports   []int
	}{
		{"发送端 (txqr-web)", a.senderExe(), a.sender, a.senderPID, a.senderPorts},
		{"桌面接收端 (框选扫描)", a.receiverExe(), a.receiver, a.receiverPID, a.receiverPorts},
	}
	// snapshot under the lock so the async refresh can't tear a row
	a.mu.Lock()
	rowData := []struct {
		label, exe string
		running    bool
		pid        uint32
		ports      []int
	}{
		{rows[0].label, a.senderExe(), a.sender, a.senderPID, a.senderPorts},
		{rows[1].label, a.receiverExe(), a.receiver, a.receiverPID, a.receiverPorts},
	}
	a.mu.Unlock()

	rowY := 66
	for ri, row := range rowData {
		rowY = a.drawRow(hdc, w, rowY, ri, row.label, row.exe, row.running, row.pid, row.ports)
	}

	// hint, centered near the bottom
	hint, hn := utf16N("提示：停止只匹配本工具进程；launcher 自身不受影响。")
	hw, _ := textExtent(hdc, hint, hn)
	setTextColor(hdc, rgb(130, 140, 160))
	textOutW(hdc, (w-int(hw))/2, h-28, hint, hn)
}

// drawRow lays out one process line centered horizontally, returns the next
// row's y. Every piece (label, buttons, status) has reserved spacing so none
// of them overlap the others or the detail line below.
func (a *launcher) drawRow(hdc uintptr, w, y, idx int, label, exe string, running bool, pid uint32, ports []int) int {
	const btnW, btnH = 88, 32
	l, ln := utf16N(label)
	lw, lh := textExtent(hdc, l, ln)

	st := "已停止"
	if running {
		st = "运行中"
	}
	ss, sn := utf16N(st)
	sw, _ := textExtent(hdc, ss, sn)

	groupW := int(lw) + 22 + btnW + 10 + btnW + 22 + 14 + int(sw)
	x0 := (w - groupW) / 2
	if x0 < 16 {
		x0 = 16
	}

	setTextColor(hdc, rgb(210, 218, 230))
	textOutW(hdc, x0, y+int(lh/2)-8, l, ln)

	startX := x0 + int(lw) + 22
	stopX := startX + btnW + 10
	a.btns[idx*2] = rect{int32(startX), int32(y), int32(startX + btnW), int32(y + btnH)}
	a.btns[idx*2+1] = rect{int32(stopX), int32(y), int32(stopX + btnW), int32(y + btnH)}
	a.drawButton(hdc, a.btns[idx*2], "启动", idx*2, rgb(58, 104, 200))
	stopColor := uint32(rgb(88, 90, 104))
	if running {
		stopColor = rgb(44, 148, 82)
	}
	a.drawButton(hdc, a.btns[idx*2+1], "停止", idx*2+1, stopColor)

	// status: colored dot + text, vertically centered on the buttons
	dotX := stopX + btnW + 22
	dotColor := uint32(rgb(150, 150, 160))
	if running {
		dotColor = rgb(80, 220, 120)
	}
	brDot := procCall(procCreateSolidBrush, uintptr(dotColor))
	procEllipse.Call(hdc, uintptr(dotX), uintptr(y+btnH/2-4), uintptr(dotX+8), uintptr(y+btnH/2+4))
	procDeleteObject.Call(brDot)
	setTextColor(hdc, dotColor)
	textOutW(hdc, dotX+14, y+btnH/2-8, ss, sn)

	// detail line, centered, on its own reserved row so nothing overlaps
	var detail string
	if running {
		ver, bt := exeMeta(exe)
		portStr := "—"
		if len(ports) > 0 {
			portStr = ":" + itoa(ports[0])
		}
		detail = fmt.Sprintf("PID %d · %s · %s · 构建 %s", pid, portStr, ver, bt)
	} else {
		detail = "—"
	}
	ds, dn := utf16N(detail)
	dw, _ := textExtent(hdc, ds, dn)
	setTextColor(hdc, rgb(150, 160, 182))
	textOutW(hdc, (w-int(dw))/2, y+btnH+8, ds, dn)

	return y + btnH + 34
}

func (a *launcher) drawButton(hdc uintptr, r rect, label string, idx int, base uint32) {
	fill := base
	if a.pressed == idx {
		fill = lighten(base, 0.28) // clear press feedback
	}
	br := procCall(procCreateSolidBrush, uintptr(fill))
	fillRect(hdc, int(r.left), int(r.top), int(r.right), int(r.bottom), br)
	procDeleteObject.Call(br)
	brB := procCall(procCreateSolidBrush, uintptr(lighten(base, 0.45)))
	frameRect(hdc, int(r.left), int(r.top), int(r.right), int(r.bottom), brB)
	procDeleteObject.Call(brB)
	s, n := utf16N(label)
	sw, sh := textExtent(hdc, s, n)
	setTextColor(hdc, rgb(255, 255, 255))
	textOutW(hdc, int(r.left)+(int(r.right-r.left)-int(sw))/2, int(r.top)+(int(r.bottom-r.top)-int(sh))/2, s, n)
}

func lerpColor(c1, c2 uint32, t float64) uint32 {
	r1, g1, b1 := int(c1&0xFF), int((c1>>8)&0xFF), int((c1>>16)&0xFF)
	r2, g2, b2 := int(c2&0xFF), int((c2>>8)&0xFF), int((c2>>16)&0xFF)
	blend := func(a, b int) int { return a + int(float64(b-a)*t) }
	return uint32(blend(r1, r2)) | uint32(blend(g1, g2))<<8 | uint32(blend(b1, b2))<<16
}

func lighten(c uint32, f float64) uint32 {
	clamp := func(v int) int {
		if v > 255 {
			return 255
		}
		return v
	}
	r := int(c&0xFF) + int(255*f)
	g := int((c>>8)&0xFF) + int(255*f)
	b := int((c>>16)&0xFF) + int(255*f)
	return uint32(clamp(r)) | uint32(clamp(g))<<8 | uint32(clamp(b))<<16
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
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
