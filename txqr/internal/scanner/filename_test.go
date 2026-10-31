package scanner

import (
	"bytes"
	"image"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/divan/txqr"
)

func filenameFrames(t *testing.T, name string) ([]byte, [][]byte, []byte) {
	t.Helper()
	data := bytes.Repeat([]byte("verified filename content"), 100)
	e := txqr.NewEncoder(256, txqr.CodecRaptorQ)
	e.SetFileName(name)
	frames, err := e.Encode(string(data))
	if err != nil {
		t.Fatal(err)
	}
	var packets [][]byte
	var metadata []byte
	for _, p := range frames {
		f, err := txqr.ParseFrame(p)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name {
			metadata = []byte(p)
		} else {
			packets = append(packets, []byte(p))
		}
	}
	return data, packets, metadata
}

func TestLateFilenamePublishesExactlyOnceWithoutForeignSessionSwitch(t *testing.T) {
	data, packets, metadata := filenameFrames(t, "报告.zip")
	r := &region{serial: 1, scanning: true, st: &decodeState{}}
	m := &Manager{}
	for _, p := range packets {
		if _, ok := m.commitPayload(decJob{r: r, gen: 1}, p); ok {
			t.Fatal("published before metadata")
		}
	}
	if !bytes.Equal(r.st.result, data) || r.st.nameDeadline.IsZero() {
		t.Fatal("verified result not retained")
	}
	end, read, deadline := r.st.endTime, r.st.bytes, r.st.nameDeadline
	if deadline.Sub(end) != fileNameWait {
		t.Fatal("wrong bounded grace period")
	}
	_, _, foreign := filenameFrames(t, "other.zip")
	for i := 0; i < sessionSwitchAfter+5; i++ {
		m.commitPayload(decJob{r: r, gen: 1}, foreign)
	}
	bad := append([]byte(nil), metadata...)
	bad[len(bad)-1] ^= 1
	m.commitPayload(decJob{r: r, gen: 1}, bad)
	if !r.st.endTime.Equal(end) || !r.st.nameDeadline.Equal(deadline) || r.st.bytes != read {
		t.Fatal("metadata wait mutated accounting/deadline")
	}
	if _, ok := m.finishPendingName(r, 1, deadline.Add(-time.Nanosecond)); ok {
		t.Fatal("grace period ended early")
	}
	job, ok := m.commitPayload(decJob{r: r, gen: 1}, metadata)
	if !ok || job.name != "报告.zip" || !bytes.Equal(job.data, data) {
		t.Fatal("late filename not published")
	}
	if !r.st.endTime.Equal(end) {
		t.Fatal("metadata wait inflated content transfer time")
	}
	if _, ok := m.commitPayload(decJob{r: r, gen: 1}, metadata); ok {
		t.Fatal("published twice")
	}
}

func TestNameDeadlinePublishesWithoutAnyNewCapture(t *testing.T) {
	data, packets, _ := filenameFrames(t, "missing.zip")
	saved := make(chan SavedFile, 2)
	m := New(func(Rect) (*image.RGBA, []byte, error) { return nil, nil, nil }, Options{SaveDir: t.TempDir(), OnSaved: func(f SavedFile) { saved <- f }})
	t.Cleanup(func() { _ = m.Close() })
	id := m.AddRegion(Rect{R: 10, B: 10})
	if err := m.Start(id); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	r := m.byIDLocked(id)
	gen := r.serial
	m.mu.Unlock()
	for _, p := range packets {
		m.commitPayload(decJob{r: r, gen: gen}, p)
	}
	if _, _, ok := m.Result(id); ok {
		t.Fatal("published while waiting")
	}
	// Accelerate only the test deadline. The production duration is checked above.
	r.st.mu.Lock()
	r.st.nameDeadline = time.Now().Add(50 * time.Millisecond)
	r.st.mu.Unlock()
	select {
	case result := <-saved:
		if result.Errored || result.Name != "received.bin" {
			t.Fatal(result)
		}
		got, err := os.ReadFile(result.Path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("fallback changed bytes", err)
		}
	case <-time.After(time.Second):
		t.Fatal("name timeout required another QR frame")
	}
	snap := m.Snapshots()[0]
	if !snap.Done || !strings.Contains(snap.Status, "未收到原始文件名") {
		t.Fatal("fallback warning missing")
	}
	select {
	case <-saved:
		t.Fatal("saved twice")
	default:
	}
}
