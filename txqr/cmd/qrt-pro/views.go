package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/divan/txqr/internal/scanner"
	"github.com/divan/txqr/internal/sender"
	"github.com/divan/txqr/internal/uikit"
	"github.com/divan/txqr/internal/w32"
)

// ---------------------------------------------------------------- sender view

func (a *app) drawSenderView(r w32.Rect) {
	u := a.theme.Unit
	body, _ := a.headerArea(r, "发送端", "在浏览器中选择文件，屏幕会循环播放二维码，接收端对准拍摄即可")

	stats := a.srv.Stats()
	top := w32.Rect{L: body.L, T: body.T, R: body.R, B: body.T + a.px(124)}
	a.drawSenderStatusCard(top, stats)

	row := w32.Rect{L: body.L, T: top.B + u*4, R: body.R, B: top.B + u*4 + a.px(92)}
	for i, card := range panelRow(row, 3, u*4) {
		switch i {
		case 0:
			a.statCard(card, "传输次数", fmt.Sprint(stats.Transfers), a.theme.Text)
		case 1:
			a.statCard(card, "累计发送", scanner.FmtBytes(stats.BytesSent), a.theme.Text)
		case 2:
			a.statCard(card, "活动会话", fmt.Sprint(stats.ActiveSession), a.theme.Accent)
		}
	}

	listTop := row.B + u*4
	list := w32.Rect{L: body.L, T: listTop, R: body.R, B: body.B}
	a.drawSenderHistory(list)
}

// drawSenderStatusCard shows whether the service is up, where to reach it, and
// the two actions a user actually wants (start/stop and open the page).
func (a *app) drawSenderStatusCard(r w32.Rect, stats sender.Stats) {
	a.ui.Panel(r)
	u := a.theme.Unit
	inner := r.Inset(u*5, u*4)

	running := stats.Listening
	label := "已停止"
	color := a.theme.Danger
	if running {
		label = "运行中"
		color = a.theme.Success
	}
	pillR := w32.Rect{L: inner.L, T: inner.T, R: inner.L + a.ui.PillWidth(label) + u*2, B: inner.T + a.px(24)}
	a.ui.Pill(pillR, label, color)

	addr := stats.Addr
	if !running {
		addr = "未监听"
	}
	urlR := w32.Rect{L: inner.L, T: pillR.B + u*3, R: inner.R - a.px(230), B: pillR.B + u*3 + a.px(32)}
	a.ui.Text(urlR, a.ui.Ellipsize("本机 "+addr, uikit.FontTitle, urlR.W()), uikit.FontTitle, a.theme.Text, uikit.AlignLeft)

	// Right-hand actions: the toggle doubles as the primary control, and stays
	// reachable even before the service exists. The pair is vertically centred
	// so the card reads as one row rather than a top-heavy stack.
	btnW := a.px(112)
	btnH := a.px(36)
	toggleLabel := "启动服务"
	toggleStyle := uikit.ButtonPrimary
	if running {
		toggleLabel = "停止服务"
		toggleStyle = uikit.ButtonDanger
	}
	stackH := btnH*2 + u
	stackTop := inner.T + (inner.H()-stackH)/2
	toggleR := w32.Rect{L: inner.R - btnW, T: stackTop, R: inner.R, B: stackTop + btnH}
	a.ui.Button(idSenderToggle, toggleR, toggleLabel, toggleStyle, false)
	openR := w32.Rect{L: toggleR.L, T: toggleR.B + u, R: toggleR.R, B: toggleR.B + u + btnH}
	a.ui.Button(idSenderOpenPage, openR, "打开发送页面", uikit.ButtonNeutral, !running)

	// LAN addresses are informational; the phone scans the screen, but another
	// machine can drive the sender over HTTP.
	lanR := w32.Rect{L: inner.L, T: urlR.B + u, R: inner.R - a.px(230), B: inner.B}
	if running {
		urls := a.srv.LANURLs()
		txt := "仅本机访问"
		if len(urls) > 0 {
			txt = "局域网: " + strings.Join(urls, "  ")
		}
		a.ui.Text(lanR, a.ui.Ellipsize(txt, uikit.FontCaption, lanR.W()), uikit.FontCaption, a.theme.TextDim, uikit.AlignLeft)
	}
}

// drawSteps renders a numbered guide list, used for empty states so a fresh
// screen explains itself instead of showing a blank panel.
func (a *app) drawSteps(r w32.Rect, steps []string) {
	u := a.theme.Unit
	y := r.T
	numW := a.px(26)
	for i, s := range steps {
		if y+a.px(34) > r.B {
			break
		}
		badge := w32.Rect{L: r.L, T: y, R: r.L + numW, B: y + a.px(24)}
		a.ui.Flat(badge, a.theme.AccentSoft)
		a.ui.Text(badge, fmt.Sprint(i+1), uikit.FontCaption, a.theme.Text, uikit.AlignCenter)
		a.ui.Text(w32.Rect{L: r.L + numW + u*2, T: y, R: r.R, B: y + a.px(24)},
			a.ui.Ellipsize(s, uikit.FontBody, r.W()-numW-u*2), uikit.FontBody, a.theme.TextDim, uikit.AlignLeft)
		y += a.px(32)
	}
}

func (a *app) drawSenderHistory(r w32.Rect) {
	a.ui.Panel(r)
	u := a.theme.Unit
	inner := r.Inset(u*5, u*4)
	a.ui.Text(w32.Rect{L: inner.L, T: inner.T, R: inner.R, B: inner.T + a.px(22)},
		"最近发送", uikit.FontEmphasis, a.theme.Text, uikit.AlignLeft)

	evts := a.srv.Events(20)
	top := inner.T + a.px(30)
	if len(evts) == 0 {
		// Empty state that teaches instead of sitting blank: the three steps are
		// exactly what a first-time user has to do.
		a.drawSteps(w32.Rect{L: inner.L, T: top, R: inner.R, B: inner.B}, []string{
			"点上方「启动服务」，本机开始监听 9000 端口",
			"点「打开发送页面」，浏览器会打开二维码轮播界面",
			"在网页里选择文件，接收端对准屏幕持续拍摄即可",
		})
		return
	}
	pitch := a.px(30)
	for i, e := range evts {
		rowTop := top + int32(i)*pitch
		row := w32.Rect{L: inner.L, T: rowTop, R: inner.R, B: rowTop + pitch}
		if row.B > inner.B {
			break
		}
		if i%2 == 1 {
			a.ui.Flat(row, w32.Mix(a.theme.Surface, a.theme.Text, 0.03))
		}
		timeW := a.px(96)
		sizeW := a.px(84)
		modeW := a.px(64)
		framesW := a.px(60)
		nameW := inner.W() - timeW - sizeW - modeW - framesW - u*4
		if nameW < a.px(120) {
			nameW = a.px(120)
		}
		x := row.L + u
		a.ui.Text(w32.Rect{L: x, T: row.T, R: x + timeW, B: row.B}, e.Time.Format("15:04:05"), uikit.FontCaption, a.theme.TextFaint, uikit.AlignLeft)
		x += timeW
		name := e.Name
		if name == "" {
			name = "(未命名)"
		}
		a.ui.Text(w32.Rect{L: x, T: row.T, R: x + nameW, B: row.B}, a.ui.Ellipsize(name, uikit.FontBody, nameW), uikit.FontBody, a.theme.Text, uikit.AlignLeft)
		x += nameW + u
		a.ui.Text(w32.Rect{L: x, T: row.T, R: x + sizeW, B: row.B}, scanner.FmtBytes(int64(e.Size)), uikit.FontCaption, a.theme.TextDim, uikit.AlignLeft)
		x += sizeW
		a.ui.Text(w32.Rect{L: x, T: row.T, R: x + modeW, B: row.B}, e.Mode, uikit.FontCaption, a.theme.Accent, uikit.AlignLeft)
		x += modeW
		a.ui.Text(w32.Rect{L: x, T: row.T, R: x + framesW, B: row.B}, fmt.Sprintf("%d 帧", e.Frames), uikit.FontCaption, a.theme.TextFaint, uikit.AlignLeft)
	}
}

// ---------------------------------------------------------------- receiver view

func (a *app) drawReceiverView(r w32.Rect) {
	u := a.theme.Unit
	body, _ := a.headerArea(r, "接收端", "框住屏幕上的二维码区域持续拍摄，多屏可各扫一路")

	// Toolbar
	toolbar := w32.Rect{L: body.L, T: body.T, R: body.R, B: body.T + a.px(52)}
	hasRegions := a.scan.RegionCount() > 0
	buttons := []struct {
		id    uikit.ControlID
		label string
		style uikit.ButtonStyle
		disab bool
	}{
		{idAddRegion, "添加区域", uikit.ButtonPrimary, false},
		{idStartAll, "开始全部", uikit.ButtonSuccess, !hasRegions},
		{idStopAll, "停止全部", uikit.ButtonNeutral, !a.scan.AnyScanning() && !hasRegions},
		{idClearRegions, "清空区域", uikit.ButtonGhost, !hasRegions},
		{idReopenFolder, "打开接收目录", uikit.ButtonGhost, false},
	}
	btnW := a.px(128)
	gap := u * 3
	x := toolbar.L
	for _, b := range buttons {
		br := w32.Rect{L: x, T: toolbar.T, R: x + btnW, B: toolbar.B}
		a.ui.Button(b.id, br, b.label, b.style, b.disab)
		x += btnW + gap
	}

	// Toggles on their own line keeps the toolbar from wrapping on narrow layouts.
	toggleRow := w32.Rect{L: body.L, T: toolbar.B + u*3, R: body.L + a.px(430), B: toolbar.B + u*3 + a.px(36)}
	cols := uikit.Grid(toggleRow, 2, u*3)
	failsLabel := "失败帧: 关"
	if a.scan.SaveFails() {
		failsLabel = "失败帧: 开"
	}
	a.ui.Toggle(idToggleFails, cols[0], failsLabel, a.scan.SaveFails())
	pmLabel := "进度: 帧数"
	if a.scan.ProgressMode() {
		pmLabel = "进度: 块数"
	}
	a.ui.Toggle(idToggleProgressMode, cols[1], pmLabel, a.scan.ProgressMode())
	note := w32.Rect{L: toggleRow.R + u*3, T: toggleRow.T, R: body.R, B: toggleRow.B}
	a.ui.Text(note, a.ui.Ellipsize("失败帧=保存解码失败的画面 · 进度=按块数或帧数", uikit.FontCaption, note.W()),
		uikit.FontCaption, a.theme.TextFaint, uikit.AlignLeft)

	// Below the toolbar: region list on the left, preview plus counters right.
	rest := w32.Rect{L: body.L, T: toggleRow.B + u*4, R: body.R, B: body.B}
	listW := int32(float64(rest.W()) * 0.58)
	if listW < a.px(320) {
		listW = a.px(320)
	}
	listR := w32.Rect{L: rest.L, T: rest.T, R: rest.L + listW, B: rest.B}
	asideR := w32.Rect{L: rest.L + listW + u*4, T: rest.T, R: rest.R, B: rest.B}
	a.drawRegionList(listR)
	a.drawReceiverAside(asideR)
}

func (a *app) drawRegionList(r w32.Rect) {
	a.ui.Panel(r)
	u := a.theme.Unit
	inner := r.Inset(u*5, u*4)
	a.ui.Text(w32.Rect{L: inner.L, T: inner.T, R: inner.R - a.px(60), B: inner.T + a.px(22)},
		"扫描区域", uikit.FontEmphasis, a.theme.Text, uikit.AlignLeft)
	count := a.scan.RegionCount()
	a.ui.Text(w32.Rect{L: inner.R - a.px(60), T: inner.T, R: inner.R, B: inner.T + a.px(22)},
		fmt.Sprintf("%d 个", count), uikit.FontCaption, a.theme.TextFaint, uikit.AlignRight)

	snaps := a.scan.Snapshots()
	top := inner.T + a.px(30)
	if len(snaps) == 0 {
		a.drawSteps(w32.Rect{L: inner.L, T: top, R: inner.R, B: inner.B}, []string{
			"点「添加区域」，屏幕会冻结并弹出框选层",
			"在二维码周围拖拽一个框，松开后即成为一个扫描区域",
			"多显示器时可为每块屏各添加一个区域",
		})
		return
	}
	// The list scrolls: more than three regions simply do not fit, and before
	// this the extra rows were silently clipped with no way to reach them.
	viewport := w32.Rect{L: inner.L, T: top, R: inner.R - a.px(10), B: inner.B}
	pitch, rowH := a.regionRowMetrics()
	a.regions.layout(viewport, int32(len(snaps))*pitch)

	a.ui.PushClip(viewport)
	for i, s := range snaps {
		rowTop := viewport.T + int32(i)*pitch - a.regions.offset
		row := w32.Rect{L: viewport.L, T: rowTop, R: viewport.R, B: rowTop + rowH}
		if !uikit.TrimToViewport(row, viewport) {
			continue // fully scrolled out: skip the whole row
		}
		a.drawRegionRow(row, i, s)
	}
	a.ui.PopClip()
	a.drawScrollbar(&a.regions)
}

// regionRowMetrics returns the row height and the distance between rows. They
// are derived from the same line heights the row actually draws, so the pitch
// cannot drift away from the content and make rows overlap.
func (a *app) regionRowMetrics() (pitch, rowH int32) {
	u := a.theme.Unit
	rowH = u*6 + a.px(22) + a.px(14) + a.px(16) + a.px(28) + u*6
	return rowH + u*2, rowH
}

// drawRegionRow is the heart of the receiver UI: identity, live state, progress,
// and per-row actions in one card.
func (a *app) drawRegionRow(r w32.Rect, idx int, s scanner.Snapshot) {
	a.ui.Panel(r)
	u := a.theme.Unit
	pad := u * 3
	base := regionRowBase + uikit.ControlID(idx)*regionStride

	label, pillColor := a.statusPill(s)
	pillW := a.ui.PillWidth(label)
	titleR := w32.Rect{L: r.L + pad, T: r.T + pad, R: r.R - pad, B: r.T + pad + a.px(22)}
	a.ui.Text(w32.Rect{L: titleR.L, T: titleR.T, R: titleR.R - pillW - u*2, B: titleR.B},
		a.ui.Ellipsize(s.Label, uikit.FontEmphasis, titleR.W()-pillW-u*2), uikit.FontEmphasis, a.theme.Text, uikit.AlignLeft)
	a.ui.Pill(w32.Rect{L: titleR.R - pillW, T: titleR.T, R: titleR.R, B: titleR.T + a.px(22)}, label, pillColor)

	// Bar, status text and actions each own a full-width line. Sharing the
	// status line with the buttons left it ~120px wide and every row ended in
	// an ellipsis; stacking them costs vertical space but keeps the text whole.
	barTop := titleR.B + u*2
	barR := w32.Rect{L: r.L + pad, T: barTop, R: r.R - pad, B: barTop + a.px(14)}
	barColor := a.theme.Accent
	if s.Done {
		barColor = a.theme.Success
	}
	a.ui.Progress(barR, s.Progress, barColor)

	detailTop := barR.B + u*2
	detail := s.Status
	if detail == "" {
		detail = "—"
	}
	detailColor := a.theme.TextDim
	// Surface the newest diagnostic reason right where the user is looking.
	// Without it the row says "0/64 块" forever with no hint that the frames are
	// duplicates or that no QR is being found at all.
	if s.LastIssue != "" && !s.Done {
		detail += " · " + s.LastIssue
		detailColor = a.theme.Warning
	}
	a.ui.Text(w32.Rect{L: r.L + pad, T: detailTop, R: r.R - pad, B: detailTop + a.px(16)},
		a.ui.Ellipsize(detail, uikit.FontCaption, r.W()-pad*2), uikit.FontCaption, detailColor, uikit.AlignLeft)

	// Row actions stay visible even when idle, so the flow never dead-ends.
	actions := []struct {
		field uikit.ControlID
		label string
		style uikit.ButtonStyle
		disab bool
	}{
		{rowSaveAs, "另存为", uikit.ButtonGhost, !s.Done},
		{rowRemove, "移除", uikit.ButtonGhost, false},
		{rowStop, "停止", uikit.ButtonGhost, !s.Scanning},
		{rowStart, "启动", uikit.ButtonNeutral, s.Scanning || s.Done},
	}
	btnH := a.px(28)
	btnW := a.px(70)
	gap := u * 2
	stripW := int32(len(actions))*(btnW+gap) - gap
	if stripW > r.W()-pad*2 {
		btnW = (r.W() - pad*2 - gap*(int32(len(actions))-1)) / int32(len(actions))
		if btnW < a.px(40) {
			btnW = a.px(40)
		}
		stripW = int32(len(actions))*(btnW+gap) - gap
	}
	actionTop := detailTop + a.px(16) + u*2
	x := r.R - pad - btnW
	for _, act := range actions {
		br := w32.Rect{L: x, T: actionTop, R: x + btnW, B: actionTop + btnH}
		a.ui.Button(base+act.field, br, act.label, act.style, act.disab)
		x -= btnW + gap
	}
}

// drawReceiverAside shows the preview and aggregate counters.
func (a *app) drawReceiverAside(r w32.Rect) {
	u := a.theme.Unit
	previewH := int32(float64(r.H()) * 0.52)
	if previewH < a.px(180) {
		previewH = a.px(180)
	}
	previewR := w32.Rect{L: r.L, T: r.T, R: r.R, B: r.T + previewH}
	a.drawPreviewCard(previewR)

	counterTop := previewR.B + u*4
	counters := w32.Rect{L: r.L, T: counterTop, R: r.R, B: counterTop + a.px(92)}
	snaps := a.scan.Snapshots()
	scanning := 0
	var totalBytes int64
	var speed int64
	done := 0
	for _, s := range snaps {
		if s.Scanning {
			scanning++
		}
		if s.Done {
			done++
		}
		totalBytes += s.Bytes
		speed += s.Speed
	}
	for i, c := range panelRow(counters, 2, u*4) {
		switch i {
		case 0:
			a.statCard(c, "扫描中 / 已完成", fmt.Sprintf("%d / %d", scanning, done), a.theme.Text)
		case 1:
			a.statCard(c, "实时速度", scanner.FmtBytes(speed)+"/s", a.theme.Accent)
		}
	}

	infoTop := counters.B + u*4
	info := w32.Rect{L: r.L, T: infoTop, R: r.R, B: r.B}
	a.ui.Panel(info)
	inner := info.Inset(u*4, u*4)
	lines := []string{
		"有效数据量: " + scanner.FmtBytes(totalBytes),
		"运行时长: " + scanner.FmtElapsed(time.Since(a.started)),
		"接收目录: " + a.scan.SaveDir(),
	}
	y := inner.T
	for _, ln := range lines {
		a.ui.Text(w32.Rect{L: inner.L, T: y, R: inner.R, B: y + a.px(20)},
			a.ui.Ellipsize(ln, uikit.FontCaption, inner.W()), uikit.FontCaption, a.theme.TextDim, uikit.AlignLeft)
		y += a.px(22)
	}
}

func (a *app) drawPreviewCard(r w32.Rect) {
	a.ui.Panel(r)
	u := a.theme.Unit
	inner := r.Inset(u*4, u*4)
	a.ui.Text(w32.Rect{L: inner.L, T: inner.T, R: inner.R, B: inner.T + a.px(22)},
		"实时预览", uikit.FontEmphasis, a.theme.Text, uikit.AlignLeft)

	stage := w32.Rect{L: inner.L, T: inner.T + a.px(28), R: inner.R, B: inner.B}
	a.ui.Stage(stage)
	buf, w, h, ok := a.scan.Preview()
	if !ok {
		a.ui.Text(stage, "尚无画面\n启动扫描后显示最近一帧", uikit.FontBody, a.theme.TextDim, uikit.AlignCenter)
		return
	}
	if a.ui == nil {
		return
	}
	// Fit preserving aspect, centred — the capture size is whatever the user
	// boxed, so stretching it would make QR geometry unreadable as feedback.
	dw, dh := stage.W()-u*2, stage.H()-u*2
	if dw <= 0 || dh <= 0 {
		return
	}
	scale := float64(dw) / float64(w)
	if sy := float64(dh) / float64(h); sy < scale {
		scale = sy
	}
	nw := int32(float64(w) * scale)
	nh := int32(float64(h) * scale)
	if nw < 1 || nh < 1 {
		return
	}
	draw := w32.Rect{
		L: stage.L + (dw-nw)/2, T: stage.T + (dh-nh)/2,
		R: stage.L + (dw-nw)/2 + nw, B: stage.T + (dh-nh)/2 + nh,
	}
	a.ui.DIBits(draw, buf, int32(w), int32(h))
}

// ---------------------------------------------------------------- files view

func (a *app) drawFilesView(r w32.Rect) {
	u := a.theme.Unit
	body, _ := a.headerArea(r, "已收文件", "接收完成的文件会自动写入接收目录，失败会在此标红")

	files := a.scan.SavedFiles()
	head := w32.Rect{L: body.L, T: body.T, R: body.R, B: body.T + a.px(52)}
	a.ui.Panel(head)
	hInner := head.Inset(u*5, u*3)
	a.ui.Text(w32.Rect{L: hInner.L, T: hInner.T, R: hInner.R - a.px(200), B: hInner.B},
		fmt.Sprintf("共 %d 个文件", len(files)), uikit.FontEmphasis, a.theme.Text, uikit.AlignLeft)
	dirBtn := w32.Rect{L: hInner.R - a.px(180), T: hInner.T, R: hInner.R, B: hInner.B}
	a.ui.Button(idReopenFolder, dirBtn, "打开接收目录", uikit.ButtonNeutral, false)

	listTop := head.B + u*4
	list := w32.Rect{L: body.L, T: listTop, R: body.R, B: body.B}
	a.ui.Panel(list)
	inner := list.Inset(u*5, u*4)
	if len(files) == 0 {
		a.drawSteps(w32.Rect{L: inner.L, T: inner.T, R: inner.R, B: inner.B}, []string{
			"接收端每完成一次传输就会把文件写入接收目录",
			"同名文件会自动加序号，不会覆盖上一次接收的结果",
			"写入失败的记录会在此以红色标出失败原因",
		})
		return
	}
	pitch := a.px(56)
	// Same treatment as the region list: a long transfer history has to be
	// reachable, not clipped.
	viewport := w32.Rect{L: inner.L, T: inner.T, R: inner.R - a.px(10), B: inner.B}
	a.files.layout(viewport, int32(len(files))*pitch)

	a.ui.PushClip(viewport)
	for i, f := range files {
		rowTop := viewport.T + int32(i)*pitch - a.files.offset
		row := w32.Rect{L: viewport.L, T: rowTop, R: viewport.R, B: rowTop + pitch - u}
		if !uikit.TrimToViewport(row, viewport) {
			continue
		}
		a.drawFileRow(row, f)
	}
	a.ui.PopClip()
	a.drawScrollbar(&a.files)
}

func (a *app) drawFileRow(r w32.Rect, f scanner.SavedFile) {
	a.ui.Flat(r, w32.Mix(a.theme.CardTop, a.theme.Text, 0.04))
	u := a.theme.Unit
	pad := u * 3
	tone := a.theme.Text
	glyph := "✓"
	gc := a.theme.Success
	if f.Errored {
		tone = a.theme.TextDim
		glyph = "!"
		gc = a.theme.Danger
	}
	a.ui.Text(w32.Rect{L: r.L + pad, T: r.T, R: r.L + pad + a.px(24), B: r.B}, glyph, uikit.FontEmphasis, gc, uikit.AlignCenter)

	name := f.Name
	if name == "" {
		name = f.Path
	}
	a.ui.Text(w32.Rect{L: r.L + pad + a.px(28), T: r.T, R: r.R - a.px(180), B: r.T + a.px(26)},
		a.ui.Ellipsize(name, uikit.FontBody, r.W()-a.px(208)), uikit.FontBody, tone, uikit.AlignLeft)
	a.ui.Text(w32.Rect{L: r.R - a.px(176), T: r.T, R: r.R - pad, B: r.T + a.px(26)},
		f.Time.Format("15:04:05"), uikit.FontCaption, a.theme.TextFaint, uikit.AlignRight)

	detail := fmt.Sprintf("%s · %s", scanner.FmtBytes(int64(f.Size)), f.Path)
	if f.Errored {
		detail = "写入失败: " + f.Err
	}
	a.ui.Text(w32.Rect{L: r.L + pad + a.px(28), T: r.T + a.px(26), R: r.R - pad, B: r.B},
		a.ui.Ellipsize(detail, uikit.FontCaption, r.W()-a.px(56)), uikit.FontCaption,
		colorFor(f.Errored, a.theme.Danger, a.theme.TextFaint), uikit.AlignLeft)
}

// statusPill maps a region snapshot onto a badge label and colour.
func (a *app) statusPill(s scanner.Snapshot) (string, w32.Color) {
	switch {
	case s.Done:
		return "已完成", a.theme.Success
	case s.Scanning:
		return "扫描中", a.theme.Accent
	default:
		return "已停止", a.theme.TextFaint
	}
}

// ---------------------------------------------------------------- diagnostics

// drawDiagView renders the receiver's audit log. The point of this tab is to
// answer "why does it fail" without leaving the app: a failure counter cannot
// tell "no QR on screen" from "QR found but wrong protocol" from "frames arrive
// but they are all duplicates".
func (a *app) drawDiagView(r w32.Rect) {
	u := a.theme.Unit
	body, _ := a.headerArea(r, "诊断日志",
		"逐条记录解码失败、帧头异常与停滞；传不动时先看这里，再看区域行上的提示")

	path := a.scan.AuditPath()
	fileErr := a.scan.AuditFileError()

	info := w32.Rect{L: body.L, T: body.T, R: body.R, B: body.T + a.px(56)}
	a.ui.Panel(info)
	inner := info.Inset(u*5, u*3)
	line := "日志文件：" + path
	if path == "" {
		line = "日志未落盘"
		if fileErr != "" {
			line += "（" + fileErr + "）"
		}
		line += "，仅本页显示最近记录"
	}
	a.ui.Text(w32.Rect{L: inner.L, T: inner.T, R: inner.R - a.px(190), B: inner.B},
		a.ui.Ellipsize(line, uikit.FontBody, inner.W()-a.px(190)),
		uikit.FontBody, colorFor(fileErr != "", a.theme.Danger, a.theme.TextDim), uikit.AlignLeft)
	a.ui.Button(idOpenLogDir, w32.Rect{L: inner.R - a.px(180), T: inner.T, R: inner.R, B: inner.B},
		"打开日志目录", uikit.ButtonNeutral, path == "")

	list := w32.Rect{L: body.L, T: info.B + u*4, R: body.R, B: body.B}
	a.ui.Panel(list)
	li := list.Inset(u*5, u*4)

	entries := a.scan.AuditEntries(200)
	if len(entries) == 0 {
		a.drawSteps(w32.Rect{L: li.L, T: li.T, R: li.R, B: li.B}, []string{
			"还没有记录。开始一次传输后，这里会逐条出现解码与喷泉层面的结果",
			"「二维码未检出」= 框选位置/对焦问题；「载荷不可识别」= 屏幕上的码不是本接收端支持的协议",
			"「长时间没有新块」= 帧在来但都是重复的，通常是发送端暂停或区域拍到了静止画面",
		})
		return
	}

	// Newest first: the reason a transfer is stuck right now matters more than
	// what happened five minutes ago.
	pitch := a.px(46)
	a.diag.layout(w32.Rect{L: li.L, T: li.T, R: li.R - a.px(10), B: li.B}, int32(len(entries))*pitch)

	a.ui.PushClip(a.diag.viewport)
	for i := 0; i < len(entries); i++ {
		e := entries[len(entries)-1-i]
		rowTop := a.diag.viewport.T + int32(i)*pitch - a.diag.offset
		row := w32.Rect{L: a.diag.viewport.L, T: rowTop, R: a.diag.viewport.R, B: rowTop + pitch - u}
		if !uikit.TrimToViewport(row, a.diag.viewport) {
			continue
		}
		a.drawDiagRow(row, e)
	}
	a.ui.PopClip()
	a.drawScrollbar(&a.diag)
}

// drawDiagRow renders one audit entry: timestamp and region on the left, the
// short label in the level's colour, then the detail line underneath.
func (a *app) drawDiagRow(r w32.Rect, e scanner.AuditEntry) {
	u := a.theme.Unit
	a.ui.Flat(r, w32.Mix(a.theme.CardTop, a.theme.Text, 0.03))
	pad := u * 3

	tone := a.theme.TextDim
	switch e.Level {
	case scanner.AuditError:
		tone = a.theme.Danger
	case scanner.AuditWarn:
		tone = a.theme.Warning
	}

	meta := e.At.Format("15:04:05")
	if e.Region > 0 {
		meta += fmt.Sprintf("  区域%d", e.Region)
	} else {
		meta += "  全局"
	}
	metaW := a.px(112)
	a.ui.Text(w32.Rect{L: r.L + pad, T: r.T + a.px(5), R: r.L + pad + metaW, B: r.T + a.px(21)},
		meta, uikit.FontCaption, a.theme.TextFaint, uikit.AlignLeft)

	label := e.Label
	if e.Repeat > 1 {
		label = fmt.Sprintf("%s ×%d", label, e.Repeat)
	}
	a.ui.Text(w32.Rect{L: r.L + pad + metaW, T: r.T + a.px(4), R: r.R - pad, B: r.T + a.px(22)},
		a.ui.Ellipsize(label, uikit.FontEmphasis, r.W()-pad*2-metaW),
		uikit.FontEmphasis, tone, uikit.AlignLeft)

	a.ui.Text(w32.Rect{L: r.L + pad + metaW, T: r.T + a.px(22), R: r.R - pad, B: r.B},
		a.ui.Ellipsize(e.Detail, uikit.FontCaption, r.W()-pad*2-metaW),
		uikit.FontCaption, a.theme.TextDim, uikit.AlignLeft)
}
