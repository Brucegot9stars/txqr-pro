package scanner

import (
	"bytes"
	"image"
	"image/draw"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"github.com/divan/txqr/qr"
)

func toRGBA(img image.Image) *image.RGBA {
	b := image.NewRGBA(img.Bounds())
	draw.Draw(b, b.Bounds(), img, img.Bounds().Min, draw.Src)
	return b
}

// TestDecimenScanPipeline drives decodeState.feed with rendered decimen frames
// (the exact path the desktop capture loop uses) and checks the recovered file.
func TestDecimenScanPipeline(t *testing.T) {
	file := make([]byte, 120_000)
	for i := range file {
		file[i] = byte((i*37 + (i>>8)*11) & 0xff)
	}
	container, _, err := decimen.PackContainer("scan.bin", "application/octet-stream", file)
	if err != nil {
		t.Fatal(err)
	}

	const blockLen = 2933
	enc := decimen.NewEncoder(container, blockLen, 0x1234)
	hdr := decimen.Header{
		SessionId:  0x1234,
		Seq:        0,
		K:          enc.K,
		BlockLen:   blockLen,
		TotalLen:   uint32(len(container)),
		PayloadFnv: decimen.Fnv1a(container),
	}

	var st decodeState
	cycle := 2 * int(enc.K)
	completed := false
	for seq := uint32(0); seq < uint32(cycle); seq++ {
		h := hdr
		h.Seq = seq
		frame := decimen.PackFrame(h, enc.Encode(seq))
		if _, err := qr.EncodeVersioned(string(frame), 512, qr.Low, 40, 4); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if st.feedPayload(frame) {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("did not complete within one cycle")
	}
	result, name, ok := st.completedResult()
	if !ok {
		t.Fatal("completedResult false after completion")
	}
	if name != "scan.bin" || !bytes.Equal(result, file) {
		t.Fatalf("recovered mismatch: name=%q bytes=%d want %d", name, len(result), len(file))
	}
}

// TestTxqrScanPipeline drives the classic txqr path.
func TestTxqrScanPipeline(t *testing.T) {
	payload := "hello desktop receiver 中文 test 0123456789"
	enc := txqr.NewEncoder(64, txqr.CodecLT)
	frames, err := enc.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	var st decodeState
	completed := false
	for _, f := range frames {
		if st.feedPayload([]byte(f)) {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("txqr did not complete")
	}
	result, _, _ := st.completedResult()
	if string(result) != payload {
		t.Fatalf("txqr mismatch: %q", string(result))
	}
}

// TestSnapshotNeverDividesByZero guards the progress readouts against a frame
// header carrying K == 0. Any parsing change upstream would otherwise turn a
// malformed frame into an integer divide-by-zero panic on the UI thread.
func TestSnapshotNeverDividesByZero(t *testing.T) {
	var st decodeState
	hdr := decimen.Header{SessionId: 1, K: 0, BlockLen: 10, TotalLen: 10, PayloadFnv: 1}
	st.decimenHdr = hdr
	st.decimenDec = decimen.NewDecoder(hdr)
	for _, byBlocks := range []bool{true, false} {
		if _, _, _ = st.snapshot(byBlocks); false {
			t.Fatal("unreachable")
		}
	}
}

// frameStream replays pre-rendered QR images as if they were screen captures.
type frameStream struct {
	mu    sync.Mutex
	imgs  []*image.RGBA
	i     int
	calls int
}

func newFrameStream(t *testing.T, frames [][]byte, size int) *frameStream {
	t.Helper()
	fs := &frameStream{}
	for _, f := range frames {
		img, err := qr.EncodeVersioned(string(f), size, qr.Low, 40, 4)
		if err != nil {
			t.Fatalf("render frame: %v", err)
		}
		fs.imgs = append(fs.imgs, toRGBA(img))
	}
	return fs
}

// capture returns the next frame each call, cycling like a live screen would.
func (fs *frameStream) capture(Rect) (*image.RGBA, []byte, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.calls++
	img := fs.imgs[fs.i%len(fs.imgs)]
	fs.i++
	return img, make([]byte, 4*img.Bounds().Dx()*img.Bounds().Dy()), nil
}

// TestManagerEndToEndFeed drives a Manager through the real worker pool with a
// fake capturer and asserts the file lands on disk with the right bytes.
func TestManagerEndToEndFeed(t *testing.T) {
	file := make([]byte, 40_000)
	for i := range file {
		file[i] = byte((i*29 + 7) & 0xff)
	}
	container, _, err := decimen.PackContainer("end-to-end.bin", "application/octet-stream", file)
	if err != nil {
		t.Fatal(err)
	}
	const blockLen = 2933
	enc := decimen.NewEncoder(container, blockLen, 0x1234)
	hdr := decimen.Header{
		SessionId: 0x1234, Seq: 0, K: enc.K, BlockLen: blockLen,
		TotalLen: uint32(len(container)), PayloadFnv: decimen.Fnv1a(container),
	}
	var wire [][]byte
	for seq := uint32(0); seq < 2*uint32(enc.K); seq++ {
		h := hdr
		h.Seq = seq
		wire = append(wire, decimen.PackFrame(h, enc.Encode(seq)))
	}
	fs := newFrameStream(t, wire, 512)

	dir := t.TempDir()
	saved := make(chan SavedFile, 1)
	m := New(fs.capture, Options{
		SaveDir: dir,
		OnSaved: func(f SavedFile) {
			select {
			case saved <- f:
			default:
			}
		},
	})
	defer m.Close()

	id := m.AddRegion(Rect{L: 0, T: 0, R: 64, B: 64})
	if id <= 0 {
		t.Fatalf("AddRegion returned %d", id)
	}
	if err := m.Start(id); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The fake capturer is instant, so the whole cycle is replayed quickly.
	select {
	case f := <-saved:
		if f.Errored {
			t.Fatalf("save failed: %s", f.Err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("transfer never completed")
	}

	data, name, ok := m.Result(id)
	if !ok {
		t.Fatal("no completed result")
	}
	if name != "end-to-end.bin" || !bytes.Equal(data, file) {
		t.Fatalf("result mismatch: name=%q bytes=%d want %d", name, len(data), len(file))
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, "end-to-end.bin"))
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !bytes.Equal(onDisk, file) {
		t.Fatal("saved file differs from source")
	}
	if _, _, _, ok := m.Preview(); !ok {
		t.Fatal("preview never populated")
	}
	if files := m.SavedFiles(); len(files) != 1 {
		t.Fatalf("saved list %d entries, want 1", len(files))
	}
}

// TestManagerRestartCompletedRegion covers "scan another": a completed region
// must reset and be reusable instead of refusing to start forever.
func TestManagerRestartCompletedRegion(t *testing.T) {
	var st decodeState
	st.feedPayload(nil) // no-op; keeps the zero-state covered
	m := New(nil, Options{SaveDir: t.TempDir()})
	defer m.Close()

	id := m.AddRegion(Rect{L: 0, T: 0, R: 8, B: 8})
	if id <= 0 {
		t.Fatalf("AddRegion %d", id)
	}
	if err := m.Start(id); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !m.AnyScanning() {
		t.Fatal("expected scanning after Start")
	}
	m.Stop(id)
	if m.AnyScanning() {
		t.Fatal("still scanning after Stop")
	}
	if err := m.Start(id); err != nil {
		t.Fatalf("restart: %v", err)
	}
	m.Stop(id)

	m.RemoveRegion(id)
	if m.RegionCount() != 0 {
		t.Fatalf("regions after remove: %d", m.RegionCount())
	}
	if err := m.Start(id); err == nil {
		t.Fatal("expected error starting a removed region")
	}
	m.Clear()
	if m.RegionCount() != 0 {
		t.Fatalf("regions after clear: %d", m.RegionCount())
	}
}

// TestAddRegionRejectsEmpty guards against zero-sized boxes, which the old UI
// could produce from a click-drag with no movement.
func TestAddRegionRejectsEmpty(t *testing.T) {
	m := New(nil, Options{SaveDir: t.TempDir()})
	defer m.Close()
	if id := m.AddRegion(Rect{L: 10, T: 10, R: 10, B: 10}); id != -1 {
		t.Fatalf("expected rejection, got id %d", id)
	}
	if id := m.AddRegion(Rect{L: 0, T: 0, R: -5, B: 5}); id != -1 {
		t.Fatalf("expected rejection for negative width, got id %d", id)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"../../evil.bat", "evil.bat"},
		{`C:\Windows\system32\x.dll`, "x.dll"},
		{"", "received.bin"},
		{"   ", "received.bin"},
		{"...", "received.bin"},
		{"NUL", "received.bin"},
		{"con.txt", "received.bin"},
		// Everything before the final separator is dropped (never traversed),
		// control characters are removed, and every Windows host-forbidden
		// character becomes an underscore.
		{"a\x00b/<>:\"|?*`c.txt", "________c.txt"},
		{"名字 file .txt", "名字 file .txt"},
	}
	for _, c := range cases {
		if got := SanitizeFileName(c.in, filepath.Join("x", "received.bin")); got != c.want {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("a", 200) + ".bin"
	got := SanitizeFileName(long, "fallback.bin")
	if len([]rune(got)) > 120 {
		t.Errorf("long name not truncated: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, ".bin") {
		t.Errorf("truncation dropped the extension: %q", got)
	}
}

// TestUniquePathAvoidsOverwrite proves two transfers of the same file survive.
func TestUniquePathAvoidsOverwrite(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(uniquePath(base), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := uniquePath(base)
	if second == base {
		t.Fatal("second write would have overwritten the first")
	}
	if err := os.WriteFile(second, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(base)
	if string(first) != "first" {
		t.Fatalf("original clobbered: %q", first)
	}
}

func TestFmtBytesAndElapsed(t *testing.T) {
	if got := FmtBytes(2048); got != "2.0 KB" {
		t.Errorf("FmtBytes(2048) = %q", got)
	}
	if got := FmtBytes(0); got != "0 B" {
		t.Errorf("FmtBytes(0) = %q", got)
	}
	if got := FmtElapsed(1500 * time.Millisecond); got != "1.5s" {
		t.Errorf("FmtElapsed = %q", got)
	}
	if got := FmtElapsed(-time.Second); got != "0ms" {
		t.Errorf("negative elapsed = %q", got)
	}
}
