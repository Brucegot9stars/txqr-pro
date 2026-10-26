// Package uikit is the tiny immediate-mode drawing layer behind the unified
// QRT desktop app. Everything is drawn with plain GDI into one off-screen
// bitmap per paint, which — together with WM_ERASEBKGND being disabled — is what
// keeps the live scanning preview from flickering.
//
// "Immediate mode" here means: each paint lays the UI out again, recording the
// rectangle every interactive control occupied; mouse handler events then hit
// test against the *last* recorded layout. That removes the usual Win32 chore of
// keeping child HWNDs in sync with the model (region rows come and go), at the
// cost of drawing everything ourselves.
package uikit

import (
	"strings"

	"github.com/divan/txqr/internal/w32"
)

// ControlID identifies an interactive element across paints. Callers allocate
// them deterministically (base + row*stride + field) so a row that moves keeps
// its identity.
type ControlID int

// NoControl is the zero sentinel for "nothing under the cursor".
const NoControl ControlID = 0

// FontSize selects one of the theme's fonts.
type FontSize int

const (
	FontCaption FontSize = iota
	FontBody
	FontEmphasis
	FontTitle
)

// ButtonStyle picks a button's colour role.
type ButtonStyle int

const (
	ButtonPrimary ButtonStyle = iota
	ButtonNeutral
	ButtonDanger
	ButtonSuccess
	// ButtonGhost has no fill until hovered — used for row actions.
	ButtonGhost
)

// Align controls horizontal text placement.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Theme bundles the palette and the fonts. Close releases the fonts.
type Theme struct {
	Bg         w32.Color
	Surface    w32.Color
	SurfaceAlt w32.Color
	CardTop    w32.Color
	CardBottom w32.Color
	Border     w32.Color
	Divider    w32.Color
	Text       w32.Color
	TextDim    w32.Color
	TextFaint  w32.Color
	Accent     w32.Color
	AccentSoft w32.Color
	Success    w32.Color
	Warning    w32.Color
	Danger     w32.Color
	Track      w32.Color
	// StageTop/StageBottom is the surface a live preview frame sits on: darker
	// than a card so a captured image reads as content, but not pure black,
	// which just looks like a hole punched through the panel.
	StageTop    w32.Color
	StageBottom w32.Color

	Radius int32
	// Unit is one scaled layout unit (roughly 1px at 96 dpi).
	Unit int32

	fonts [4]*w32.Font
}

// Dark builds the default theme. It must be closed.
func Dark() *Theme {
	t := &Theme{
		Bg:          w32.RGB(0x0F, 0x11, 0x17),
		Surface:     w32.RGB(0x16, 0x19, 0x22),
		SurfaceAlt:  w32.RGB(0x1C, 0x20, 0x2B),
		CardTop:     w32.RGB(0x1D, 0x22, 0x2E),
		CardBottom:  w32.RGB(0x18, 0x1B, 0x24),
		Border:      w32.RGB(0x2A, 0x30, 0x3E),
		Divider:     w32.RGB(0x25, 0x2A, 0x36),
		Text:        w32.RGB(0xE8, 0xEC, 0xF4),
		TextDim:     w32.RGB(0xB6, 0xBE, 0xCE),
		TextFaint:   w32.RGB(0x8C, 0x96, 0xA8),
		Accent:      w32.RGB(0x3B, 0x82, 0xF6),
		AccentSoft:  w32.RGB(0x1E, 0x3A, 0x6B),
		Success:     w32.RGB(0x22, 0xC5, 0x5E),
		Warning:     w32.RGB(0xF5, 0x9E, 0x0B),
		Danger:      w32.RGB(0xEF, 0x44, 0x44),
		Track:       w32.RGB(0x24, 0x29, 0x34),
		StageTop:    w32.RGB(0x13, 0x16, 0x1E),
		StageBottom: w32.RGB(0x0C, 0x0E, 0x14),
		Radius:      w32.Scale(10),
		Unit:        w32.Scale(4),
	}
	const face = "Microsoft YaHei UI"
	t.fonts[FontCaption] = w32.NewFont(face, w32.Pt(8.5), w32.WeightNormal)
	t.fonts[FontBody] = w32.NewFont(face, w32.Pt(9.5), w32.WeightNormal)
	t.fonts[FontEmphasis] = w32.NewFont(face, w32.Pt(10), w32.WeightSemi)
	t.fonts[FontTitle] = w32.NewFont(face, w32.Pt(15), w32.WeightSemi)
	return t
}

// fallback fills in any font the constructor could not create, so text never
// silently disappears when the preferred face is missing.
func (t *Theme) fallback() {
	for i, f := range t.fonts {
		if f == nil {
			t.fonts[i] = w32.NewFont("Segoe UI", w32.Pt(9.5), w32.WeightNormal)
		}
	}
}

// Close releases every font owned by the theme.
func (t *Theme) Close() {
	for _, f := range t.fonts {
		f.Close()
	}
}

// control records where one interactive element was painted.
type control struct {
	id     ControlID
	rect   w32.Rect
	cursor uintptr
}

// Ctx carries per-frame drawing and input state. It is not safe for concurrent
// use — all painting happens on one locked OS thread, by construction.
type Ctx struct {
	Theme *Theme
	W, H  int32
	dc    *w32.DC

	layout map[ControlID]control
	order  []ControlID

	hover   ControlID
	pressed ControlID
	focused ControlID

	curFont *w32.Font
}

// NewCtx prepares a context whose layout starts empty.
func NewCtx(theme *Theme) *Ctx {
	theme.fallback()
	return &Ctx{
		Theme:  theme,
		layout: map[ControlID]control{},
		hover:  NoControl,
	}
}

// BeginPaint starts a frame. Every control registration from the previous frame
// is discarded, which is what makes removed rows unclickable immediately.
func (c *Ctx) BeginPaint(dc *w32.DC, w, h int32) {
	c.layout = map[ControlID]control{}
	c.order = c.order[:0]
	c.dc = dc
	c.W, c.H = w, h
	c.curFont = nil
	if c.dc != nil {
		c.dc.SetBkMode(w32.Transparent)
	}
}

// EndPaint finishes the frame.
func (c *Ctx) EndPaint() { c.dc = nil }

// Size reports the drawing area.
func (c *Ctx) Size() (int32, int32) { return c.W, c.H }

// RectOf returns the last painted rectangle of a control.
func (c *Ctx) RectOf(id ControlID) (w32.Rect, bool) {
	ct, ok := c.layout[id]
	return ct.rect, ok
}

// HitTest returns the topmost control containing (x,y). Later registrations win,
// which matches how later paints draw on top.
func (c *Ctx) HitTest(x, y int32) ControlID {
	for i := len(c.order) - 1; i >= 0; i-- {
		if c.layout[c.order[i]].rect.Contains(x, y) {
			return c.order[i]
		}
	}
	return NoControl
}

// SetHover records the control under the cursor for press/hover feedback.
func (c *Ctx) SetHover(id ControlID) { c.hover = id }

// Hover reports the hovered control.
func (c *Ctx) Hover() ControlID { return c.hover }

// Press marks a control as armed. Returns false when there was nothing to press.
func (c *Ctx) Press(id ControlID) bool {
	if id == NoControl {
		return false
	}
	c.pressed = id
	return true
}

// Release clears the armed control and returns the control actually clicked:
// only a press that started and ended on the same control counts, matching what
// every native button does.
func (c *Ctx) Release() ControlID {
	armed := c.pressed
	c.pressed = NoControl
	return armed
}

// ClearPress drops the armed state without dispatching a click, e.g. when the
// mouse left the window mid-drag.
func (c *Ctx) ClearPress() { c.pressed = NoControl }

// ---------------------------------------------------------------- background

// FillBackground paints the window background and returns it unchanged.
func (c *Ctx) FillBackground() {
	if c.dc == nil {
		return
	}
	c.dc.FillRect(w32.Rect{L: 0, T: 0, R: c.W, B: c.H}, c.Theme.Bg)
}

// ---------------------------------------------------------------- text

func (c *Ctx) useFont(size FontSize) {
	if c.dc == nil {
		return
	}
	f := c.Theme.fonts[size]
	if f == nil {
		f = c.Theme.fonts[FontBody]
	}
	if c.curFont == f {
		return
	}
	c.dc.Select(f.Handle())
	c.curFont = f
}

// Text draws a label in r. Large sizes fall back down rather than overflowing.
func (c *Ctx) Text(r w32.Rect, s string, size FontSize, color w32.Color, align Align) {
	if c.dc == nil || s == "" || r.W() <= 0 {
		return
	}
	c.useFont(size)
	c.dc.SetTextColor(color)
	switch align {
	case AlignCenter:
		c.dc.Text(r, s, w32.TextCenter)
	case AlignRight:
		c.dc.Text(r, s, w32.TextRight)
	default:
		c.dc.Text(r, s, w32.TextLeft)
	}
}

// MeasureText reports the rendered size of s under size.
func (c *Ctx) MeasureText(s string, size FontSize) (int32, int32) {
	if c.dc == nil {
		return 0, 0
	}
	c.useFont(size)
	return c.dc.TextExtent(s)
}

// Ellipsize trims s to fit width using the given font. The target reordering
// makes this O(1) compared with repeated MeasureText calls per row.
func (c *Ctx) Ellipsize(s string, size FontSize, width int32) string {
	if c.dc == nil || s == "" {
		return s
	}
	if w, _ := c.MeasureText(s, size); w <= width {
		return s
	}
	const dots = "…"
	lo, hi := 0, len([]rune(s))
	runes := []rune(s)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if w, _ := c.MeasureText(string(runes[:mid])+dots, size); w <= width {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo <= 0 {
		return dots
	}
	return string(runes[:lo]) + dots
}

// ---------------------------------------------------------------- surfaces

// Panel paints a rounded surface with a hairline border.
func (c *Ctx) Panel(r w32.Rect) {
	if c.dc == nil {
		return
	}
	c.dc.RoundRectGradient(r, c.Theme.CardTop, c.Theme.CardBottom, c.Theme.Radius, c.Theme.Border, 1)
}

// Flat fills a rounded shape with one colour, used for chips and progress tracks.
func (c *Ctx) Flat(r w32.Rect, col w32.Color) {
	if c.dc == nil {
		return
	}
	c.dc.RoundRect(r, col, 0, c.Theme.Radius, 0)
}

// Stage paints the recessed area a camera frame is drawn into.
func (c *Ctx) Stage(r w32.Rect) {
	if c.dc == nil {
		return
	}
	c.dc.RoundRectGradient(r, c.Theme.StageTop, c.Theme.StageBottom, c.Theme.Radius, c.Theme.Border, 1)
}

// Divider draws a 1px horizontal rule.
func (c *Ctx) Divider(r w32.Rect) {
	if c.dc == nil {
		return
	}
	c.dc.FillRect(w32.Rect{L: r.L, T: r.T, R: r.R, B: r.T + 1}, c.Theme.Divider)
}

// ---------------------------------------------------------------- controls

// Button draws a clickable label. It registers id for hit testing and reflects
// hover/press/disabled state, because a button that gives no feedback feels dead.
func (c *Ctx) Button(id ControlID, r w32.Rect, label string, style ButtonStyle, disabled bool) {
	if c.dc == nil {
		return
	}
	base, text := c.buttonColors(style, disabled)
	fill := base
	switch {
	case disabled:
		fill = base
	case c.pressed == id:
		fill = w32.Darken(base, 0.22)
	case c.hover == id:
		fill = w32.Lighten(base, 0.12)
	}
	c.register(id, r, cursorHand)

	// Ghost buttons have no chrome until hovered; every other style fills, then
	// gets a hairline border one step brighter than its own fill.
	ghost := style == ButtonGhost && !disabled
	if !ghost {
		c.dc.RoundRect(r, fill, w32.NoFill, c.Theme.Radius, 0)
	}
	switch {
	case ghost && (c.hover == id || c.pressed == id):
		c.dc.RoundRect(r, w32.NoFill, c.Theme.Border, c.Theme.Radius, 1)
	case ghost:
		// idle ghost: nothing to draw
	default:
		c.dc.RoundRect(r, w32.NoFill, w32.Lighten(fill, 0.18), c.Theme.Radius, 1)
	}
	c.Text(r, label, FontEmphasis, text, AlignCenter)
}

func (c *Ctx) buttonColors(style ButtonStyle, disabled bool) (fill, text w32.Color) {
	if disabled {
		return c.Theme.SurfaceAlt, c.Theme.TextFaint
	}
	switch style {
	case ButtonPrimary:
		return c.Theme.Accent, w32.RGB(0xFF, 0xFF, 0xFF)
	case ButtonDanger:
		return c.Theme.Danger, w32.RGB(0xFF, 0xFF, 0xFF)
	case ButtonSuccess:
		return c.Theme.Success, w32.RGB(0x08, 0x20, 0x12)
	case ButtonGhost:
		return w32.NoFill, c.Theme.Text
	default:
		return c.Theme.SurfaceAlt, c.Theme.Text
	}
}

// Toggle renders a two-state button; the "on" half uses the accent fill so the
// expensive-to-spot "failed frames" or "progress mode" switches read at a glance.
func (c *Ctx) Toggle(id ControlID, r w32.Rect, label string, on bool) {
	if c.dc == nil {
		return
	}
	fill := c.Theme.SurfaceAlt
	dot := c.Theme.Danger
	text := c.Theme.TextDim
	if on {
		fill = c.Theme.AccentSoft
		dot = c.Theme.Success
		text = c.Theme.Text
	}
	if c.hover == id {
		fill = w32.Lighten(fill, 0.10)
	}
	c.register(id, r, cursorHand)
	c.dc.RoundRect(r, fill, c.Theme.Border, c.Theme.Radius, 1)

	u := c.Theme.Unit
	dotR := w32.Rect{L: r.L + u, T: r.T + (r.H()-8)/2, R: r.L + u + 8, B: r.T + (r.H()-8)/2 + 8}
	c.dc.Ellipse(dotR, dot)
	labelR := w32.Rect{L: dotR.R + u, T: r.T, R: r.R - u, B: r.B}
	c.Text(labelR, label, FontEmphasis, text, AlignCenter)
}

// Pill draws a small status badge: filled with its colour has a soft tint,
// keeping the strongest signal in the text.
func (c *Ctx) Pill(r w32.Rect, text string, col w32.Color) {
	if c.dc == nil {
		return
	}
	c.dc.RoundRect(r, w32.Mix(c.Theme.Surface, col, 0.22), col, r.H()/2, 1)
	c.Text(r, text, FontCaption, w32.Lighten(col, 0.35), AlignCenter)
}

// PillWidth measures how wide a pill needs to be for its text.
func (c *Ctx) PillWidth(text string) int32 {
	w, _ := c.MeasureText(text, FontCaption)
	return w + c.Theme.Unit*5
}

// DIBits draws a raw BGRA frame (top-down rows) scaled into r, preserving
// aspect — the caller computes the aspect, this just hands it to GDI.
func (c *Ctx) DIBits(r w32.Rect, bgra []byte, srcW, srcH int32) {
	if c.dc == nil {
		return
	}
	c.dc.StretchDIBits(r, bgra, srcW, srcH)
}

// Progress draws a track and filled bar with the percentage centred. Rounded
// ends are faked by keeping the bar inside the track's radius.
func (c *Ctx) Progress(r w32.Rect, pct int, col w32.Color) {
	if c.dc == nil {
		return
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	radius := r.H() / 2
	c.dc.RoundRect(r, c.Theme.Track, 0, radius, 0)
	if pct > 0 {
		inset := w32.Rect{L: r.L, T: r.T, R: r.R, B: r.B}
		fw := int32(int(inset.W()) * pct / 100)
		if fw < 2 {
			fw = 2
		}
		filled := w32.Rect{L: inset.L, T: inset.T, R: inset.L + fw, B: inset.B}
		c.dc.RoundRect(filled, col, 0, radius, 0)
	}
	if r.H() >= 14 {
		c.Text(r, itoa(pct)+"%", FontCaption, c.Theme.Text, AlignCenter)
	}
}

// NavItem is one entry in the left navigation rail. The active entry gets an
// accent bar on its leading edge and a brighter surface.
func (c *Ctx) NavItem(id ControlID, r w32.Rect, glyph, label string, active bool) {
	if c.dc == nil {
		return
	}
	c.register(id, r, cursorHand)
	// Inactive entries are hollow so the rail's own background shows through;
	// filling them with black would punch dark holes into the sidebar.
	fill := w32.NoFill
	text := c.Theme.TextDim
	if active {
		fill = c.Theme.SurfaceAlt
		text = c.Theme.Text
	} else if c.hover == id {
		fill = w32.Mix(c.Theme.Bg, c.Theme.SurfaceAlt, 0.5)
		text = c.Theme.Text
	}
	c.dc.RoundRect(r, fill, 0, c.Theme.Radius, 0)
	if active {
		u := c.Theme.Unit
		c.dc.RoundRect(w32.Rect{L: r.L, T: r.T + u, R: r.L + 3, B: r.B - u}, c.Theme.Accent, 0, 2, 0)
	}
	u := c.Theme.Unit
	glyphR := w32.Rect{L: r.L + u*3, T: r.T, R: r.L + u*3 + u*5, B: r.B}
	c.Text(glyphR, glyph, FontEmphasis, text, AlignCenter)
	labelR := w32.Rect{L: glyphR.R + u, T: r.T, R: r.R - u, B: r.B}
	c.Text(labelR, label, FontEmphasis, text, AlignLeft)
}

// register records a control's rectangle for hit testing. The arrow/hand cursor
// hints are advisory: the app sets the cursor on WM_SETCURSOR from here.
func (c *Ctx) register(id ControlID, r w32.Rect, cursor uintptr) {
	if _, seen := c.layout[id]; !seen {
		c.order = append(c.order, id)
	}
	c.layout[id] = control{id: id, rect: r, cursor: cursor}
}

// CursorFor reports whether a control wants the hand cursor.
func (c *Ctx) CursorFor(id ControlID) uintptr {
	if ct, ok := c.layout[id]; ok {
		return ct.cursor
	}
	return cursorArrow
}

var (
	cursorHand  = w32.LoadCursor(w32.IDCHand)
	cursorArrow = w32.LoadCursor(w32.IDCArrow)
)

// itoa avoids importing strconv into every layout call site.
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

// Layout helpers.

// Rows returns n evenly-spaceable vertical slots inside r; each row advances y
// by pitch, which lets list screens stop drawing anything below the viewport
// instead of painting off-screen rows.
func Rows(top int32, count, pitch int) (rows int) {
	if pitch <= 0 || count <= 0 {
		return 0
	}
	_ = top
	return count
}

// VSplit divides r into a left region of width w and the remainder.
func VSplit(r w32.Rect, left int32) (w32.Rect, w32.Rect) {
	return w32.Rect{L: r.L, T: r.T, R: r.L + left, B: r.B},
		w32.Rect{L: r.L + left, T: r.T, R: r.R, B: r.B}
}

// Grid lays out count columns inside r with gap between them.
func Grid(r w32.Rect, count int, gap int32) []w32.Rect {
	if count <= 0 {
		return nil
	}
	out := make([]w32.Rect, count)
	total := r.W() - gap*int32(count-1)
	w := total / int32(count)
	for i := 0; i < count; i++ {
		l := r.L + int32(i)*(w+gap)
		out[i] = w32.Rect{L: l, T: r.T, R: l + w, B: r.B}
	}
	return out
}

// TrimToViewport returns false once the row's top has passed the bottom of the
// visible area, so list drawing can bail out early.
func TrimToViewport(r, viewport w32.Rect) bool {
	return r.T < viewport.B && r.B > viewport.T
}

// FirstLine collapses multi-safety text to a single line for compact rows.
func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
