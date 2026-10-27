package main

import (
	"github.com/divan/txqr/internal/w32"
)

// scrollList is the vertical scroll state of one list viewport.
//
// Everything here runs on the UI thread: the paint pass calls layout() to
// record the viewport and the content height, and the wheel/drag handlers read
// the same fields. No lock is needed, and no state is shared with workers.
type scrollList struct {
	offset   int32    // pixels scrolled past the top
	viewport w32.Rect // last painted viewport, in client coordinates
	content  int32    // total height of all rows

	// pinBottom asks the next layout to jump to the end. Adding a region has to
	// reveal the row that was just created, which is at the bottom by
	// definition — that is the whole reason this list scrolls at all.
	pinBottom bool

	dragging bool
	dragY    int32
	dragFrom int32
}

// maxOffset is how far the content can be scrolled before its end lines up with
// the bottom of the viewport.
func (s *scrollList) maxOffset() int32 {
	m := s.content - s.viewport.H()
	if m < 0 {
		return 0
	}
	return m
}

// clamp keeps the offset inside [0, maxOffset].
func (s *scrollList) clamp() {
	if max := s.maxOffset(); s.offset > max {
		s.offset = max
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// layout records the geometry of this frame and re-clamps the offset. It runs
// during paint, so a shrinking list (a region removed) can never leave the view
// scrolled past its new end.
func (s *scrollList) layout(viewport w32.Rect, content int32) {
	s.viewport = viewport
	s.content = content
	s.clamp()
	if s.pinBottom {
		s.offset = s.maxOffset()
		s.pinBottom = false
	}
}

// scrollBy moves the offset and reports whether anything changed.
func (s *scrollList) scrollBy(d int32) bool {
	before := s.offset
	s.offset += d
	s.clamp()
	return s.offset != before
}

// wheel scrolls by whole rows. oneNotch is the distance for a single detent.
func (s *scrollList) wheel(delta, oneNotch int32) bool {
	notches := delta / 120
	if notches == 0 {
		// Hi-res wheels and touchpads report values below one detent; treat
		// them as a single step in the reported direction instead of dropping
		// the event (which made those devices feel dead).
		if delta > 0 {
			notches = 1
		} else if delta < 0 {
			notches = -1
		} else {
			return false
		}
	}
	return s.scrollBy(-notches * oneNotch)
}

// beginDrag starts a scroll drag if p is inside the viewport.
func (s *scrollList) beginDrag(p w32.Point) bool {
	if !s.viewport.Contains(p.X, p.Y) || s.maxOffset() == 0 {
		return false
	}
	s.dragging = true
	s.dragY = p.Y
	s.dragFrom = s.offset
	return true
}

// dragTo follows the pointer: dragging up (smaller Y) scrolls down.
func (s *scrollList) dragTo(p w32.Point) bool {
	if !s.dragging {
		return false
	}
	return s.scrollBy(s.dragFrom + (s.dragY - p.Y) - s.offset)
}

// endDrag finishes a drag. It reports whether a drag was actually in progress
// so the caller can tell a scroll gesture from a click.
func (s *scrollList) endDrag() bool {
	was := s.dragging
	s.dragging = false
	return was
}

// scrollToEnd reveals the last row.
func (s *scrollList) scrollToEnd() { s.pinBottom = true }

// drawScrollbar paints a slim thumb on the right edge of the viewport, but only
// when there is something to scroll: a permanently visible bar on a list that
// fits is noise, and its absence is what told users the list was not scrollable.
func (a *app) drawScrollbar(s *scrollList) {
	if s.maxOffset() <= 0 {
		return
	}
	vp := s.viewport
	pad := a.px(4)
	w := a.px(6)
	track := w32.Rect{
		L: vp.R - w - pad, T: vp.T + pad,
		R: vp.R - pad, B: vp.B - pad,
	}
	if track.W() <= 0 || track.H() <= 0 {
		return
	}
	a.ui.Flat(track, w32.Mix(a.theme.Track, a.theme.Text, 0.10))

	thumbH := track.H() * vp.H() / s.content
	if min := a.px(24); thumbH < min {
		thumbH = min
	}
	if thumbH > track.H() {
		thumbH = track.H()
	}
	travel := track.H() - thumbH
	var top int32
	if max := s.maxOffset(); max > 0 {
		top = track.T + travel*s.offset/max
	} else {
		top = track.T
	}
	thumb := w32.Rect{L: track.L, T: top, R: track.R, B: top + thumbH}
	a.ui.Flat(thumb, w32.Mix(a.theme.Track, a.theme.Text, 0.45))
}

// onWheel routes a mouse wheel event to the list under the pointer. The list is
// chosen by tab because the pointer position alone cannot tell which of two
// stacked views the user means.
func (a *app) onWheel(delta int32, p w32.Point) {
	if delta == 0 {
		return
	}
	var changed bool
	switch a.tab {
	case 1:
		// regionRowMetrics returns (pitch, rowH); a notch scrolls one full pitch
		// so consecutive wheel steps land on the same row boundary.
		pitch, _ := a.regionRowMetrics()
		if a.regions.viewport.Contains(p.X, p.Y) {
			changed = a.regions.wheel(delta, pitch)
		}
	case 2:
		if a.files.viewport.Contains(p.X, p.Y) {
			changed = a.files.wheel(delta, a.px(56))
		}
	case 3:
		if a.diag.viewport.Contains(p.X, p.Y) {
			changed = a.diag.wheel(delta, a.px(46))
		}
	}
	if changed {
		w32.Invalidate(a.hwnd)
	}
}

// beginListDrag starts a drag only when the press missed every control, so the
// buttons inside a row keep priority over the scroll gesture.
func (a *app) beginListDrag(p w32.Point) {
	switch a.tab {
	case 1:
		a.regions.beginDrag(p)
	case 2:
		a.files.beginDrag(p)
	case 3:
		a.diag.beginDrag(p)
	}
}

// dragLists follows the pointer while a drag is active and reports whether the
// view needs a repaint.
func (a *app) dragLists(p w32.Point) bool {
	switch a.tab {
	case 1:
		return a.regions.dragTo(p)
	case 2:
		return a.files.dragTo(p)
	case 3:
		return a.diag.dragTo(p)
	}
	return false
}

// endListDrag closes any drag in progress.
func (a *app) endListDrag() {
	a.regions.endDrag()
	a.files.endDrag()
	a.diag.endDrag()
}
