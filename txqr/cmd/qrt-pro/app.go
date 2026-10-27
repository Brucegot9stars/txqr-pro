package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/divan/txqr/internal/scanner"
	"github.com/divan/txqr/internal/sender"
	"github.com/divan/txqr/internal/uikit"
	"github.com/divan/txqr/internal/w32"
)

const (
	// wmAppRefresh is posted by worker threads to ask for a repaint. Posting is
	// the only safe way back to the UI: touching widgets from a goroutine would
	// race with the message pump.
	wmAppRefresh = 0x0400 + 1
	timerRefresh = 1
	refreshMs    = 500
)

// paintChecked limits the QRT_PAINT_CHECK diagnostic to the first paint.
var paintChecked atomic.Bool

// tracePath/traced record the first messages the window receives when
// QRT_PAINT_CHECK is set, so a run with no visible desktop can still show
// whether the pump delivered WM_PAINT at all.
var (
	tracePath string
	traced    atomic.Int64
)

func traceMsg(hwnd uintptr, msg uint32) {
	if tracePath == "" || traced.Add(1) > 200 {
		return
	}
	if f, err := os.OpenFile(tracePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "msg 0x%04X hwnd=%v\n", msg, hwnd)
		f.Close()
	}
}

// Navigation ids. They are fixed because the nav rail never changes.
const (
	idNavSender uikit.ControlID = 100 + iota
	idNavReceiver
	idNavFiles
)

// Sender view ids.
const (
	idSenderToggle uikit.ControlID = 200 + iota
	idSenderOpenPage
)

// Receiver view ids.
const (
	idAddRegion uikit.ControlID = 300 + iota
	idStartAll
	idStopAll
	idClearRegions
	idReopenFolder
	idToggleFails
	idToggleProgressMode
)

// Region rows use their own numeric space so adding or removing a row never
// invalidates toolbar ids.
const (
	regionRowBase uikit.ControlID = 1000
	regionStride  uikit.ControlID = 16
	rowStart      uikit.ControlID = 0
	rowStop       uikit.ControlID = 1
	rowRemove     uikit.ControlID = 2
	rowSaveAs     uikit.ControlID = 3
)

type app struct {
	hwnd        uintptr
	overlayHwnd uintptr
	hinst       uintptr

	theme *uikit.Theme
	ui    *uikit.Ctx

	tab int // 0 sender, 1 receiver, 2 files

	srv  *sender.Server
	scan *scanner.Manager

	mu      sync.Mutex
	status  string
	lastErr string
	started time.Time

	// regions/files are the scroll state of the two list views. They live on
	// the UI thread only (paint records the viewport, wheel/drag consumes it).
	regions scrollList
	files   scrollList

	// senderBusy guards the start/stop network operation, which runs off the
	// message thread: binding a wildcard address can take seconds on some
	// Windows setups (interface enumeration / firewall), and blocking the
	// message loop freezes the whole window.
	senderBusy bool

	// Selection overlay state. It lives on the UI thread only, so no lock.
	selBuf   []byte
	selW     int
	selH     int
	selStart w32.Point
	selCur   w32.Point
	dragging bool
	// resumeAfter holds regions that were scanning when the overlay opened, so
	// adding a box restores multi-monitor streams instead of leaving them dead.
	resumeAfter map[int]bool

	closed bool
	sizing bool
}

func newApp(inst uintptr) *app {
	theme := uikit.Dark()
	a := &app{
		hinst:       inst,
		theme:       theme,
		ui:          uikit.NewCtx(theme),
		started:     time.Now(),
		srv:         sender.New(),
		resumeAfter: map[int]bool{},
	}
	a.scan = scanner.New(nil, scanner.Options{
		OnUpdate: func() { w32.PostMessage(a.hwnd, wmAppRefresh, 0, 0) },
		OnSaved: func(f scanner.SavedFile) {
			a.mu.Lock()
			if f.Errored {
				a.lastErr = "保存失败: " + f.Err
			} else {
				a.status = "已保存 " + f.Name
			}
			a.mu.Unlock()
			w32.PostMessage(a.hwnd, wmAppRefresh, 0, 0)
		},
	})
	return a
}

// createWindows registers both window classes and creates the main window.
func (a *app) createWindows() bool {
	w32.RegisterClass(mainClass, cb(a.wndProc), w32.IDCArrow)
	w32.RegisterClass(overlayClass, cb(a.overlayProc), w32.IDCCross)

	style := uintptr(w32.WSOverlappedWindow|w32.WSVisible|w32.WSClipChildren) & ^uintptr(w32.WSMaximizeBox)
	hwnd := w32.CreateWindow(mainClass, "QRT Pro · 光传输工具箱", style, 0,
		w32.Scale(120), w32.Scale(80), w32.Scale(1080), w32.Scale(720), 0)
	if hwnd == 0 {
		w32.MessageBox(0, "创建主窗口失败", "QRT Pro", w32.MBIconError)
		return false
	}
	a.hwnd = hwnd
	w32.SetTimer(hwnd, timerRefresh, refreshMs)
	return true
}

// cb adapts a Go method to the WNDPROC signature Win32 callbacks require.
func cb(fn func(uintptr, uint32, uintptr, uintptr) uintptr) uintptr {
	return syscall.NewCallback(fn)
}

// ---------------------------------------------------------------- painting

func (a *app) paintMain() {
	dc, end := w32.PaintDC(a.hwnd)
	if dc == nil {
		return
	}
	defer end()
	rc := w32.ClientRect(a.hwnd)
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

	a.drawInto(mem, rc.W(), rc.H())

	// Blit first, restore second. Restore puts the DC's original 1x1 placeholder
	// bitmap back, and BitBlt copies whatever bitmap is currently selected —
	// restoring first blitted that placeholder and left the window blank.
	// Blit first, restore second. Restore puts the DC's original 1x1 placeholder
	// bitmap back, and BitBlt copies whatever bitmap is currently selected —
	// restoring first blitted that placeholder and left the window blank.
	dc.Blit(0, 0, rc.W(), rc.H(), mem, 0, 0)
	// Restore before bmp.Close runs (defers are LIFO, so Close goes first from
	// here): GDI refuses to delete an object still selected into a DC.
	mem.Restore()
	if p := os.Getenv("QRT_PAINT_CHECK"); p != "" {
		a.paintCheck(p, dc, rc)
	}
}

// paintCheck samples the window DC right after a blit and appends the colours to
// path. A GUI app has no console, so on a locked or headless machine this is the
// only way to prove that pixels actually reached the window rather than only
// reaching the off-screen buffer.
func (a *app) paintCheck(path string, dc *w32.DC, rc w32.Rect) {
	if !paintChecked.CompareAndSwap(false, true) {
		return
	}
	pts := []w32.Point{
		{X: 2, Y: 2},
		{X: rc.W() / 2, Y: rc.H() / 2},
		{X: rc.W() - 3, Y: rc.H() - 3},
		{X: a.px(60), Y: a.px(120)},
		{X: rc.W() / 2, Y: a.px(12)},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "client %dx%d\n", rc.W(), rc.H())
	for _, p := range pts {
		if p.X >= rc.W() {
			p.X = rc.W() - 1
		}
		if p.Y >= rc.H() {
			p.Y = rc.H() - 1
		}
		c, ok := dc.Pixel(p.X, p.Y)
		if !ok {
			fmt.Fprintf(&b, "  (%d,%d) = <GetPixel failed>\n", p.X, p.Y)
			continue
		}
		v := uint32(c)
		fmt.Fprintf(&b, "  (%d,%d) = rgb(%d,%d,%d)\n", p.X, p.Y, v&0xFF, (v>>8)&0xFF, (v>>16)&0xFF)
	}
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.WriteString(b.String())
		f.Close()
	}
}

// drawInto renders the whole UI into a memory DC. Both WM_PAINT and the
// -snapshot export go through it, so a screenshot is the same code path the user
// sees rather than a parallel layout that can drift.
func (a *app) drawInto(dc *w32.DC, w, h int32) {
	a.ui.BeginPaint(dc, w, h)
	a.drawShell()
	a.ui.EndPaint()
}

// Snapshot renders the UI off-screen and writes it to path as PNG. Used for
// documentation screenshots and for verifying layout without a visible desktop.
func (a *app) Snapshot(path string, w, h int32) error {
	screen := w32.ScreenDC()
	if screen == 0 {
		return errors.New("无法获取屏幕 DC")
	}
	defer w32.ReleaseDC(screen)

	mem := w32.NewCompatibleDC(screen)
	if mem == nil {
		return errors.New("无法创建内存 DC")
	}
	defer mem.Close()
	bmp := w32.NewCompatibleBitmap(screen, w, h)
	if bmp == nil {
		return errors.New("无法创建位图")
	}
	defer bmp.Close()
	mem.Select(bmp.Handle())

	a.drawInto(mem, w, h)

	bits := w32.ReadBits(mem, bmp, w, h)
	mem.Restore()
	if bits == nil {
		return errors.New("无法回读像素")
	}
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i, j := 0, 0; i+3 < len(bits); i, j = i+4, j+4 {
		img.Pix[j] = bits[i+2]
		img.Pix[j+1] = bits[i+1]
		img.Pix[j+2] = bits[i]
		img.Pix[j+3] = 0xFF
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// drawShell paints the nav rail, the header and whichever view is active.
func (a *app) drawShell() {
	a.ui.FillBackground()
	sidebar, rest := a.splitShell()
	a.drawNav(sidebar)
	a.drawContent(rest)
}

// splitShell returns the nav rail rect and the remaining content rect.
func (a *app) splitShell() (w32.Rect, w32.Rect) {
	w, h := a.ui.Size()
	sw := a.px(176)
	return w32.Rect{L: 0, T: 0, R: sw, B: h}, w32.Rect{L: sw, T: 0, R: w, B: h}
}

func (a *app) drawNav(r w32.Rect) {
	a.ui.Flat(w32.Rect{L: r.L, T: r.T, R: r.R - w32.Scale(1), B: r.B}, a.theme.Surface)
	a.ui.Divider(w32.Rect{L: r.R - 1, T: r.T, R: r.R, B: r.B})

	u := a.theme.Unit
	// Brand block
	title := "QRT Pro"
	sub := "收发一体·一步到位"
	brandTop := r.T + a.px(22)
	a.ui.Text(w32.Rect{L: r.L + u*4, T: brandTop, R: r.R - u*2, B: brandTop + a.px(26)}, title, uikit.FontTitle, a.theme.Text, uikit.AlignLeft)
	a.ui.Text(w32.Rect{L: r.L + u*4, T: brandTop + a.px(28), R: r.R - u*2, B: brandTop + a.px(46)}, sub, uikit.FontCaption, a.theme.TextDim, uikit.AlignLeft)

	y := brandTop + a.px(62)
	for i, item := range []struct {
		id    uikit.ControlID
		glyph string
		label string
	}{
		{idNavSender, "↑", "发送端"},
		{idNavReceiver, "↓", "接收端"},
		{idNavFiles, "≡", "已收文件"},
	} {
		itemR := w32.Rect{L: r.L + u*2, T: y, R: r.R - u*2, B: y + a.px(42)}
		a.ui.NavItem(item.id, itemR, item.glyph, item.label, a.tab == i)
		y += a.px(48)
	}

	// Footer: a compact runtime readout instead of empty space.
	a.mu.Lock()
	status := a.status
	lastErr := a.lastErr
	a.mu.Unlock()
	infoY := r.B - a.px(72)
	a.ui.Divider(w32.Rect{L: r.L + u*2, T: infoY, R: r.R - u*2, B: infoY})
	txt := "就绪"
	if status != "" {
		txt = status
	}
	if lastErr != "" {
		txt = lastErr
	}
	box := w32.Rect{L: r.L + u*3, T: infoY + u*3, R: r.R - u*2, B: r.B - u*2}
	a.ui.Text(box, a.ui.Ellipsize(txt, uikit.FontCaption, box.W()), uikit.FontCaption,
		colorFor(lastErr != "", a.theme.Danger, a.theme.Text), uikit.AlignLeft)
}

func colorFor(cond bool, on, off w32.Color) w32.Color {
	if cond {
		return on
	}
	return off
}

// drawContent routes to the active view.
func (a *app) drawContent(r w32.Rect) {
	switch a.tab {
	case 0:
		a.drawSenderView(r)
	case 1:
		a.drawReceiverView(r)
	default:
		a.drawFilesView(r)
	}
}

// cardArea returns the working rect inside the content area (leaving a header
// band and symmetric padding) and the y where the header text ended.
func (a *app) headerArea(r w32.Rect, title, sub string) (w32.Rect, int32) {
	u := a.theme.Unit
	pad := u * 5
	hdr := w32.Rect{L: r.L + pad, T: r.T + u*5, R: r.R - pad, B: r.T + a.px(74)}
	a.ui.Text(w32.Rect{L: hdr.L, T: hdr.T, R: hdr.R, B: hdr.T + a.px(30)}, title, uikit.FontTitle, a.theme.Text, uikit.AlignLeft)
	a.ui.Text(w32.Rect{L: hdr.L, T: hdr.T + a.px(32), R: hdr.R, B: hdr.T + a.px(52)}, sub, uikit.FontBody, a.theme.TextDim, uikit.AlignLeft)
	body := w32.Rect{L: hdr.L, T: hdr.B, R: hdr.R, B: r.B - pad}
	return body, hdr.B
}

// panelRow lays out count equal-height panels stacked vertically with pitch.
func panelRow(base w32.Rect, count int, gap int32) []w32.Rect {
	out := make([]w32.Rect, 0, count)
	total := base.W() - gap*int32(count-1)
	w := total / int32(count)
	for i := 0; i < count; i++ {
		l := base.L + int32(i)*(w+gap)
		out = append(out, w32.Rect{L: l, T: base.T, R: l + w, B: base.B})
	}
	return out
}

// statCard renders one metric: a dim caption above a large value.
func (a *app) statCard(r w32.Rect, caption, value string, accent w32.Color) {
	a.ui.Panel(r)
	u := a.theme.Unit
	a.ui.Text(w32.Rect{L: r.L + u*4, T: r.T + u*4, R: r.R - u*4, B: r.T + u*8}, caption, uikit.FontCaption, a.theme.TextFaint, uikit.AlignLeft)
	a.ui.Text(w32.Rect{L: r.L + u*4, T: r.T + u*9, R: r.R - u*4, B: r.B - u*3}, value, uikit.FontTitle, accent, uikit.AlignLeft)
}

// ---------------------------------------------------------------- input

func (a *app) onMouseMove(p w32.Point) {
	if a.dragLists(p) {
		w32.Invalidate(a.hwnd)
	}
	id := a.ui.HitTest(p.X, p.Y)
	if id != a.ui.Hover() {
		a.ui.SetHover(id)
		w32.Invalidate(a.hwnd)
	}
}

func (a *app) onPress(p w32.Point) {
	id := a.ui.HitTest(p.X, p.Y)
	a.ui.Press(id)
	// Pressing the empty part of a list starts a scroll drag. Controls win,
	// so the buttons inside a row stay clickable.
	if id == uikit.NoControl {
		a.beginListDrag(p)
	}
	w32.Invalidate(a.hwnd)
}

func (a *app) onRelease(p w32.Point) {
	a.endListDrag()
	hit := a.ui.HitTest(p.X, p.Y)
	armed := a.ui.Release()
	// Only a press that began and ended on the same control is a click; this is
	// what keeps drag-out accepted by every native button.
	if armed != uikit.NoControl && armed == hit {
		a.handleClick(armed)
	}
	w32.Invalidate(a.hwnd)
}

func (a *app) applyCursor() {
	c := a.ui.CursorFor(a.ui.Hover())
	if c == 0 {
		c = w32.LoadCursor(w32.IDCArrow)
	}
	w32.SetCursor(c)
}

func (a *app) onTimer() {
	// The timer only refreshes derived readouts (connection counts, live speed);
	// the paint itself is cheap because everything is one blit.
	w32.Invalidate(a.hwnd)
}

func (a *app) handleClick(id uikit.ControlID) {
	switch id {
	case idNavSender:
		a.tab = 0
	case idNavReceiver:
		a.tab = 1
	case idNavFiles:
		a.tab = 2
	case idSenderToggle:
		a.toggleSender()
	case idSenderOpenPage:
		a.openSenderPage()
	case idAddRegion:
		a.beginSelect()
	case idStartAll:
		a.startAllRegions()
	case idStopAll:
		a.scan.StopAll()
	case idClearRegions:
		a.scan.Clear()
	case idReopenFolder:
		a.openFolder(a.scan.SaveDir())
	case idToggleFails:
		a.scan.SetSaveFails(!a.scan.SaveFails())
	case idToggleProgressMode:
		a.scan.SetProgressMode(!a.scan.ProgressMode())
	default:
		a.handleRegionClick(id)
	}
}

// handleRegionClick resolves list-row controls, whose ids are computed as
// base + index*stride + field.
func (a *app) handleRegionClick(id uikit.ControlID) {
	if id < regionRowBase {
		return
	}
	d := id - regionRowBase
	idx := int(d / regionStride)
	field := d % regionStride
	snaps := a.scan.Snapshots()
	if idx >= len(snaps) {
		return
	}
	region := snaps[idx]
	switch field {
	case rowStart:
		a.startRegion(region.ID)
	case rowStop:
		a.scan.Stop(region.ID)
	case rowRemove:
		a.scan.RemoveRegion(region.ID)
	case rowSaveAs:
		a.saveRegionAs(region.ID)
	}
}

// ---------------------------------------------------------------- actions

// toggleSender starts or stops the sender service. Both directions run off the
// message thread: net.Listen on a wildcard address can take seconds here
// (observed 3-30 s depending on adapter/firewall state), and doing that inline
// froze the window so completely that clicks queued up behind it and the
// button looked dead. The busy flag drops re-entrant clicks instead of
// queueing a start immediately followed by a stop.
func (a *app) toggleSender() {
	a.mu.Lock()
	if a.senderBusy {
		a.mu.Unlock()
		return
	}
	a.senderBusy = true
	a.mu.Unlock()

	stop := a.srv.Stats().Listening
	a.setStatus("发送服务处理中…")
	go func() {
		if stop {
			a.stopSender()
		} else {
			a.startSender()
		}
		a.mu.Lock()
		a.senderBusy = false
		a.mu.Unlock()
		w32.PostMessage(a.hwnd, wmAppRefresh, 0, 0)
	}()
}

// startSender binds :9000, falling back to any free port so a port already
// taken by another program never leaves the user with a dead button.
func (a *app) startSender() {
	if err := a.srv.Start(":9000"); err != nil {
		if err2 := a.srv.Start(":0"); err2 != nil {
			a.setError("发送服务启动失败: " + err.Error())
			return
		}
		a.setStatus("端口 9000 被占用，已自动改用 " + a.srv.Addr())
		return
	}
	a.setStatus("发送服务已启动")
}

func (a *app) stopSender() {
	if err := a.srv.Close(); err != nil {
		a.setError("发送服务停止失败: " + err.Error())
		return
	}
	// Never report success on a port that is still bound: that is exactly how
	// the old sync.Once bug hid itself (status said "已停止" while the page
	// kept answering).
	if a.srv.Stats().Listening {
		a.setError("发送服务停止失败：端口仍在监听")
		return
	}
	a.setStatus("发送服务已停止")
}

func (a *app) openSenderPage() {
	url := a.srv.LocalURL()
	if url == "" {
		a.setError("发送服务尚未启动")
		return
	}
	if err := w32.OpenURL(a.hwnd, url); err != nil {
		a.setError("无法打开浏览器: " + err.Error())
		return
	}
	a.setStatus("已在浏览器中打开发送页面")
}

func (a *app) openFolder(dir string) {
	if dir == "" {
		return
	}
	if err := w32.OpenURL(a.hwnd, filepath.Clean(dir)); err != nil {
		a.setError("无法打开目录: " + err.Error())
		return
	}
}

func (a *app) startAllRegions() {
	if a.scan.RegionCount() == 0 {
		w32.MessageBox(a.hwnd, "请先点「添加区域」框住屏幕上的二维码", "提示", w32.MBIconInfo)
		return
	}
	if err := a.scan.StartAll(); err != nil {
		a.setError("启动失败: " + err.Error())
	}
}

func (a *app) startRegion(id int) {
	if err := a.scan.Start(id); err != nil {
		a.setError("启动失败: " + err.Error())
	}
}

func (a *app) saveRegionAs(id int) {
	data, name, ok := a.scan.Result(id)
	if !ok || len(data) == 0 {
		w32.MessageBox(a.hwnd, "该区域还没有完成的传输", "提示", w32.MBIconInfo)
		return
	}
	path := w32.SaveFile(a.hwnd, scanner.SanitizeFileName(name, "received.bin"), "所有文件")
	if path == "" {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		a.setError("保存失败: " + err.Error())
		return
	}
	a.setStatus("已保存到 " + path)
}

func (a *app) setStatus(s string) {
	a.mu.Lock()
	a.status = s
	a.lastErr = ""
	a.mu.Unlock()
}

func (a *app) setError(s string) {
	a.mu.Lock()
	a.lastErr = s
	a.mu.Unlock()
}

// state helpers shared by the views.

func (a *app) px(v float64) int32 { return w32.Scale(v) }
