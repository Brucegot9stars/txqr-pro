package main

import (
	"testing"

	"github.com/divan/txqr/internal/w32"
)

// vp is a viewport of 300px height at the origin of a 400-wide column.
var vp = w32.Rect{L: 0, T: 100, R: 400, B: 400}

func TestScrollListClampsToContent(t *testing.T) {
	var s scrollList
	s.layout(vp, 1000) // 1000px of rows in a 300px viewport
	if got := s.maxOffset(); got != 700 {
		t.Fatalf("maxOffset = %d, want 700", got)
	}

	if !s.scrollBy(200) || s.offset != 200 {
		t.Fatalf("scrollBy(200) -> offset %d, want 200", s.offset)
	}
	// Past the end clamps instead of leaving a gap under the last row.
	s.scrollBy(10_000)
	if s.offset != 700 {
		t.Fatalf("offset after overscroll = %d, want 700", s.offset)
	}
	// And past the top clamps at zero.
	s.scrollBy(-10_000)
	if s.offset != 0 {
		t.Fatalf("offset after underscroll = %d, want 0", s.offset)
	}
}

func TestScrollListShortContentDoesNotScroll(t *testing.T) {
	var s scrollList
	s.layout(vp, 120) // fits entirely
	if s.maxOffset() != 0 {
		t.Fatalf("maxOffset = %d for content that fits, want 0", s.maxOffset())
	}
	if s.scrollBy(100) {
		t.Error("scrollBy reported a change although there is nothing to scroll")
	}
	if s.beginDrag(w32.Point{X: 10, Y: 200}) {
		t.Error("beginDrag accepted a drag on a list that cannot scroll")
	}
}

func TestScrollListWheelDirectionAndHighResolution(t *testing.T) {
	const row = 100

	// Windows reports a positive delta for "wheel rotated away from the user",
	// which must reveal *earlier* content (the offset shrinks). Starting from
	// the middle makes the direction observable in both directions.
	newAtMiddle := func() scrollList {
		var s scrollList
		s.layout(vp, 2000)
		s.offset = 500
		return s
	}

	s := newAtMiddle()
	if !s.wheel(120, row) || s.offset != 400 {
		t.Fatalf("wheel(+120) -> offset %d, want 400 (scrolled up)", s.offset)
	}
	if !s.wheel(-120, row) || s.offset != 500 {
		t.Fatalf("wheel(-120) -> offset %d, want 500 (scrolled down)", s.offset)
	}
	if !s.wheel(-360, row) || s.offset != 800 {
		t.Fatalf("wheel(-360) -> offset %d, want 800 (three notches down)", s.offset)
	}

	// Touchpads and hi-res wheels send fractions of a detent; dropping those
	// made scrolling feel dead on that hardware, so any non-zero delta steps.
	s = newAtMiddle()
	if !s.wheel(-30, row) || s.offset != 600 {
		t.Fatalf("wheel(-30) -> offset %d, want 600", s.offset)
	}
	if s.wheel(0, row) {
		t.Error("wheel(0) reported a change")
	}

	// At the top a forward roll has nowhere to go and must not report a change.
	var top scrollList
	top.layout(vp, 2000)
	if top.wheel(120, row) {
		t.Error("wheel at the top reported a change")
	}
}

func TestScrollListDragFollowsPointer(t *testing.T) {
	var s scrollList
	s.layout(vp, 1000)

	// A press outside the viewport must not start a drag.
	if s.beginDrag(w32.Point{X: 10, Y: 20}) {
		t.Fatal("beginDrag accepted a point outside the viewport")
	}

	if !s.beginDrag(w32.Point{X: 10, Y: 300}) {
		t.Fatal("beginDrag rejected a point inside the viewport")
	}
	// Dragging up by 120px scrolls down by 120px.
	if !s.dragTo(w32.Point{X: 10, Y: 180}) || s.offset != 120 {
		t.Fatalf("dragTo(up 120) -> offset %d, want 120", s.offset)
	}
	// Dragging further down than the start never goes negative.
	s.dragTo(w32.Point{X: 10, Y: 900})
	if s.offset != 0 {
		t.Fatalf("dragTo(down) -> offset %d, want 0", s.offset)
	}
	// The drag tracks the pointer from where it started, not incrementally, so
	// shaking the mouse cannot accumulate error.
	s.dragTo(w32.Point{X: 10, Y: 0})
	if s.offset != 300 {
		t.Fatalf("dragTo(top) -> offset %d, want 300", s.offset)
	}
	if !s.endDrag() {
		t.Error("endDrag did not report the drag it just ended")
	}
	if s.dragTo(w32.Point{X: 10, Y: 0}) {
		t.Error("dragTo moved the list after the drag ended")
	}
}

func TestScrollListLayoutReclampsAfterShrink(t *testing.T) {
	var s scrollList
	s.layout(vp, 2000)
	s.scrollBy(1000)
	if s.offset != 1000 {
		t.Fatalf("offset = %d, want 1000", s.offset)
	}
	// Regions removed: the content is now shorter than the current offset.
	s.layout(vp, 500)
	if s.offset != 200 {
		t.Fatalf("offset after shrink = %d, want 200 (maxOffset)", s.offset)
	}
}

// TestScrollListPinBottom pins the behaviour behind "add a region and see it":
// the new row is appended, so the list has to jump to the end on the next
// layout — the offset is only known then, because it depends on the viewport
// height the paint pass supplies.
func TestScrollListPinBottom(t *testing.T) {
	var s scrollList
	s.layout(vp, 1000)
	s.scrollToEnd()
	s.layout(vp, 1400) // a region was added
	if s.offset != s.maxOffset() || s.offset != 1100 {
		t.Fatalf("offset = %d, want %d (end of content)", s.offset, s.maxOffset())
	}
	if s.pinBottom {
		t.Error("pinBottom was not consumed by layout")
	}
	// A later layout must not keep jumping to the end.
	s.layout(vp, 1400)
	if s.offset != 1100 {
		t.Fatalf("offset after plain layout = %d, want 1100", s.offset)
	}
}
