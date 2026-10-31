package scanner

import (
	"bytes"
	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaleResultCannotMutateRestartedScan(t *testing.T) {
	frames, err := txqr.NewEncoder(512, txqr.CodecLT).Encode("ABC")
	if err != nil {
		t.Fatal(err)
	}
	r := &region{serial: 2, scanning: true, st: &decodeState{}}
	m := &Manager{}
	if _, ok := m.commitPayload(decJob{r: r, gen: 1}, []byte(frames[0])); ok {
		t.Fatal("stale completion")
	}
	if r.st.txqrDec != nil || r.st.completed {
		t.Fatal("stale payload entered current state")
	}
	job, ok := m.commitPayload(decJob{r: r, gen: 2}, []byte(frames[0]))
	if ok {
		t.Fatal("nameless transfer did not allow a metadata grace period")
	}
	if _, stale := m.finishPendingName(r, 1, time.Now().Add(fileNameWait)); stale {
		t.Fatal("stale deadline published the current scan")
	}
	job, ok = m.finishPendingName(r, 2, time.Now().Add(fileNameWait))
	if !ok || !bytes.Equal(job.data, []byte("ABC")) {
		t.Fatal("current result rejected")
	}
}

func TestCloseWaitsForCaptureAndRejectsRestart(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	m := New(func(Rect) (*image.RGBA, []byte, error) { close(entered); <-release; return nil, nil, nil }, Options{SaveDir: t.TempDir()})
	id := m.AddRegion(Rect{R: 10, B: 10})
	if err := m.Start(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("capture not started")
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("Close left capture running")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close stuck")
	}
	if m.Start(id) == nil {
		t.Fatal("restart after close accepted")
	}
}

func TestLongExtensionDoesNotPanic(t *testing.T) {
	got := SanitizeFileName("x."+strings.Repeat("字", 150), "fallback.bin")
	if len([]rune(got)) > 120 || got == "" {
		t.Fatal("invalid sanitized name")
	}
}

func TestDecimenMetadataCorruptionIsTerminal(t *testing.T) {
	container, _, err := decimen.PackContainer("a.bin", "application/octet-stream", []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	enc := decimen.NewEncoder(container, 2933, 1)
	h := decimen.Header{SessionId: 1, K: enc.K, BlockLen: 2933, TotalLen: uint32(len(container)), PayloadFnv: decimen.Fnv1a(container)}
	block := enc.Encode(0)
	block[49] ^= 1
	st := &decodeState{}
	if st.feedPayload(decimen.PackFrame(h, block)) || st.failed == "" {
		t.Fatal("corruption was not terminal")
	}
	count := st.frames
	h.Seq = 2
	st.feedPayload(decimen.PackFrame(h, enc.Encode(2)))
	if st.frames != count || st.completed {
		t.Fatal("continued using corrupt state")
	}
	st.reset()
	h.Seq = 0
	if !st.feedPayload(decimen.PackFrame(h, enc.Encode(0))) {
		t.Fatal("rescan failed")
	}
}

func TestAtomicWriteDoesNotClobberExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := writeAtomic(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second"), false); !os.IsExist(err) {
		t.Fatal("exclusive publish", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first" {
		t.Fatal("previous file changed")
	}
	if err := writeAtomic(path, []byte("second"), true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("partial file leaked")
	}
}
