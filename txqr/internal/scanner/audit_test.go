package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clock is a manual clock so the sampling window can be tested without sleeping
// three real seconds per assertion.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestAudit(t *testing.T) (*Audit, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "receiver.log")
	a := OpenAudit(path)
	t.Cleanup(a.Close)
	clk := &clock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	a.now = clk.now
	return a, clk, path
}

// TestAuditFoldsRepeatsIntoOneLine is the property that makes this usable at
// 30 fps: the first occurrence is logged at once (that is the one a user
// needs), the storm behind it becomes a single line carrying a count.
func TestAuditFoldsRepeatsIntoOneLine(t *testing.T) {
	a, clk, _ := newTestAudit(t)

	a.Note(1, AuditWarn, "qr.notfound", "第一次")
	if got := len(a.Entries(0)); got != 1 {
		t.Fatalf("first occurrence produced %d entries, want 1", got)
	}

	// 60 more failures inside the flush window: no new lines, just a count.
	for i := 0; i < 60; i++ {
		clk.add(10 * time.Millisecond)
		a.Note(1, AuditWarn, "qr.notfound", "又一次")
	}
	if got := len(a.Entries(0)); got != 1 {
		t.Fatalf("storm produced %d entries, want 1 (folded)", got)
	}

	// Past the window one line is emitted, carrying how many were folded.
	clk.add(auditFlush + time.Second)
	a.Note(1, AuditWarn, "qr.notfound", "仍然失败")
	entries := a.Entries(0)
	if len(entries) != 2 {
		t.Fatalf("got %d entries after the flush window, want 2", len(entries))
	}
	// 60 folded during the storm + this one = 61. The first line already
	// accounted for occurrence #1, so 62 occurrences produce 2 lines.
	if entries[1].Repeat != 61 {
		t.Errorf("repeat count = %d, want 61 (60 folded + this one)", entries[1].Repeat)
	}
	if entries[1].Detail != "仍然失败" {
		t.Errorf("detail = %q, want the newest one", entries[1].Detail)
	}
}

// TestAuditAlwaysIsNeverFolded guards the rare events: a session switch or a
// completion must not be swallowed because it happened twice in a row.
func TestAuditAlwaysIsNeverFolded(t *testing.T) {
	a, _, _ := newTestAudit(t)
	a.Always(2, AuditInfo, "frame.session-new", "a")
	a.Always(2, AuditInfo, "frame.session-new", "b")
	if got := len(a.Entries(0)); got != 2 {
		t.Fatalf("got %d entries, want 2 (Always must not sample)", got)
	}
}

// TestAuditRegionsAreIndependent: a storm in one region must not mute another,
// which would hide the very comparison the log exists for.
func TestAuditRegionsAreIndependent(t *testing.T) {
	a, _, _ := newTestAudit(t)
	a.Note(1, AuditWarn, "qr.notfound", "r1")
	a.Note(2, AuditWarn, "qr.notfound", "r2")
	if got := len(a.Entries(0)); got != 2 {
		t.Fatalf("got %d entries, want one per region", got)
	}
}

func TestAuditRingIsBoundedAndNewestLast(t *testing.T) {
	a, _, _ := newTestAudit(t)
	for i := 0; i < auditRingMax+50; i++ {
		a.Always(1, AuditInfo, "frame.name", "x")
	}
	entries := a.Entries(0)
	if len(entries) != auditRingMax {
		t.Fatalf("ring holds %d entries, want %d", len(entries), auditRingMax)
	}
	if got := a.Entries(5); len(got) != 5 {
		t.Fatalf("Entries(5) returned %d", len(got))
	}
}

func TestAuditLastForRegion(t *testing.T) {
	a, _, _ := newTestAudit(t)
	a.Always(1, AuditInfo, "region.start", "one")
	a.Always(2, AuditWarn, "qr.notfound", "two")
	if e := a.LastFor(1); e.Reason != "region.start" {
		t.Errorf("LastFor(1) = %q, want region.start", e.Reason)
	}
	if e := a.LastFor(3); e.Reason != "" {
		t.Errorf("LastFor(3) = %q, want zero", e.Reason)
	}
}

// TestAuditWritesReadableLines pins the on-disk format: one line per entry,
// timestamp first, region and reason present. A log a user pastes must be
// readable without the app.
func TestAuditWritesReadableLines(t *testing.T) {
	a, _, path := newTestAudit(t)
	a.Always(3, AuditError, "save.error", "写入失败：磁盘已满")
	a.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(data)
	for _, want := range []string{"2026-10-27 12:00:00", "ERROR", "区域3", "写入文件失败", "磁盘已满"} {
		if !strings.Contains(text, want) {
			t.Errorf("log line missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "\n") != 1 {
		t.Errorf("expected exactly one line, got:\n%s", text)
	}
}

// TestAuditRotates keeps the log from growing without bound while preserving
// one previous generation.
func TestAuditRotates(t *testing.T) {
	a, _, path := newTestAudit(t)
	a.maxBytes = 200
	for i := 0; i < 40; i++ {
		a.Always(1, AuditInfo, "frame.name", strings.Repeat("x", 60))
	}
	a.Close()

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() > 1000 {
		t.Fatalf("live log not reset after rotation: size=%v err=%v", st.Size(), err)
	}
}

// TestAuditUnwritablePathStillWorks: the receiver must keep running (and keep
// answering "why") when the log cannot be created.
func TestAuditUnwritablePathStillWorks(t *testing.T) {
	// A file where the parent directory should be: MkdirAll fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := OpenAudit(filepath.Join(blocker, "logs", "receiver.log"))
	defer a.Close()

	if a.Path() != "" {
		t.Errorf("Path() = %q, want empty when logging is off", a.Path())
	}
	if a.FileError() == "" {
		t.Error("FileError() empty, want the reason")
	}
	a.Note(1, AuditWarn, "qr.notfound", "still works")
	if got := len(a.Entries(0)); got != 1 {
		t.Fatalf("in-memory ring empty (%d entries) when the file is unavailable", got)
	}
}

func TestHexAndPrintablePrefix(t *testing.T) {
	b := []byte{'a', 0x00, 0xFF, 'b', 'c'}
	if got := hexPrefix(b, 3); got != "6100ff…" {
		t.Errorf("hexPrefix = %q", got)
	}
	if got := printablePrefix(b, 3); got != `a\x00\xff…` {
		t.Errorf("printablePrefix = %q", got)
	}
	if got := oneLine("a\nb\r\nc"); got != "a b  c" && got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
}

// ---------------------------------------------------------------- pipeline

func newAuditedState(t *testing.T) (*decodeState, *Audit, *clock) {
	t.Helper()
	a, clk, _ := newTestAudit(t)
	return &decodeState{id: 1, audit: a}, a, clk
}

func reasons(a *Audit) []string {
	var out []string
	for _, e := range a.Entries(0) {
		out = append(out, e.Reason)
	}
	return out
}

func has(t *testing.T, a *Audit, reason string) {
	t.Helper()
	for _, e := range a.Entries(0) {
		if e.Reason == reason {
			return
		}
	}
	t.Fatalf("log has no %q entry; got %v", reason, reasons(a))
}

// TestFrameFromAnotherSessionIsRejectedThenAdopted covers the "it worked, then
// I sent a second file and nothing ever happened again" case: the first header
// locks the decoder, and frames built for a different total cannot contribute.
// They are dropped (instead of poisoning the decode) until the run of foreign
// headers is long enough to conclude the sender really switched files.
func TestFrameFromAnotherSessionIsRejectedThenAdopted(t *testing.T) {
	st, audit, _ := newAuditedState(t)

	first := "0/1024/4096|" + strings.Repeat("a", 16)
	if st.feedPayload([]byte(first)) {
		t.Fatal("a single frame must not complete a transfer")
	}
	if st.txqrTotal != 4096 || st.txqrChunk != 1024 || st.txqrNeed != 4 {
		t.Fatalf("session = chunk %d total %d need %d, want 1024/4096/4",
			st.txqrChunk, st.txqrTotal, st.txqrNeed)
	}
	has(t, audit, "frame.session-new")

	// A frame from a different file: same region, different total.
	foreign := "0/1024/65536|" + strings.Repeat("b", 16)
	for i := 1; i < sessionSwitchAfter; i++ {
		if st.feedPayload([]byte(foreign)) {
			t.Fatal("foreign frame completed the transfer")
		}
		if st.txqrTotal != 4096 {
			t.Fatalf("frame %d switched the session early (total=%d)", i, st.txqrTotal)
		}
	}
	has(t, audit, "frame.session-mismatch")

	// The run is long enough now: adopt the new transfer instead of staying
	// stuck on the old one forever.
	st.feedPayload([]byte(foreign))
	if st.txqrTotal != 65536 {
		t.Fatalf("after %d foreign frames total = %d, want 65536", sessionSwitchAfter, st.txqrTotal)
	}
	has(t, audit, "frame.session-switch")
}

// TestNameFrameIsMetadataNotProgress: "-1/.../name|file" must not count as a
// received block, or the row shows progress one block ahead of reality.
func TestNameFrameIsMetadataNotProgress(t *testing.T) {
	st, audit, _ := newAuditedState(t)
	st.feedPayload([]byte("0/1024/4096|" + strings.Repeat("a", 16)))
	before := st.txqrGot

	st.feedPayload([]byte("-1/1024/4096/name|报告 2026.pdf"))

	if st.txqrGot != before {
		t.Errorf("name frame counted as a block: %d → %d", before, st.txqrGot)
	}
	if st.fileName != "报告 2026.pdf" {
		t.Errorf("fileName = %q", st.fileName)
	}
	has(t, audit, "frame.name")
}

// TestInvalidHeaderIsExplained: a frame that is not ours must say so, with the
// bytes, instead of only bumping a counter.
func TestInvalidHeaderIsExplained(t *testing.T) {
	st, audit, clk := newAuditedState(t)

	st.feedPayload([]byte("not a frame at all"))
	has(t, audit, "frame.header-invalid")

	// Binary junk (a Color Matrix frame looks like this to us) must be shown
	// escaped so a pasted log line stays readable. Step past the sampling
	// window first, otherwise this folds into the line above.
	clk.add(auditFlush + time.Second)
	st.feedPayload([]byte{0x01, 0x02, 0x00, 0xFF})
	entries := audit.Entries(0)
	last := entries[len(entries)-1]
	if !strings.Contains(last.Detail, `\x01`) {
		t.Errorf("detail does not escape binary: %q", last.Detail)
	}
	if !strings.Contains(last.Detail, "|") {
		t.Errorf("detail should show the '|' lookup failure: %q", last.Detail)
	}
}

// TestStallIsReportedOncePerWindow: identical frames forever is the most
// misleading failure (frames counter climbs, transfer is dead). It must produce
// a log line, and must not produce one per frame.
func TestStallIsReportedOncePerWindow(t *testing.T) {
	st, audit, _ := newAuditedState(t)

	frame := []byte("0/1024/4096|" + strings.Repeat("a", 16))
	st.feedPayload(frame) // first block
	// Pretend the last gain was long ago, then feed duplicates.
	st.lastGain = st.lastGain.Add(-6 * time.Second)
	for i := 0; i < 50; i++ {
		st.feedPayload(frame)
	}
	stalled := 0
	for _, e := range audit.Entries(0) {
		if e.Reason == "fountain.stalled" {
			stalled++
		}
	}
	if stalled == 0 {
		t.Fatalf("no stall entry; got %v", reasons(audit))
	}
	if stalled > 2 {
		t.Errorf("stall reported %d times for one window", stalled)
	}
}
