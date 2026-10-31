package sender

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/qr"
)

func TestInitialFramesReadyAndRemainingFramesRenderOnDemand(t *testing.T) {
	body := make([]byte, 128<<10)
	for i := range body {
		body[i] = byte(i*31 + i/137)
	}
	sess, err := encodeIsolated(context.Background(), workerRequest{Mode: "qr", Codec: txqr.CodecRaptorQ, Split: 2048, Name: "test.bin", Size: len(body), MemoryBytes: workerMemoryBytes, CacheBytes: 16 << 20}, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.frames) <= initialFrames || sess.pngs[0] == nil || sess.pngs[initialFrames-1] == nil || sess.pngs[initialFrames] != nil {
		t.Fatal("worker did not return exactly the initial window")
	}
	s := New()
	defer s.Close()
	s.sessions["x"] = sess
	data, err := s.framePNG(context.Background(), "x", initialFrames)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	packet, err := qr.Decode(img)
	if err != nil || packet != string(sess.frames[initialFrames]) {
		t.Fatal("on-demand PNG changed the packet", err)
	}
}

func TestFullPreparationOptionStillPreparesEveryFrame(t *testing.T) {
	body := bytes.Repeat([]byte("full preparation"), 7000)
	sess, err := encodeIsolated(context.Background(), workerRequest{Mode: "qr", Codec: txqr.CodecRaptorQ, Split: 2048, Size: len(body), MemoryBytes: workerMemoryBytes, CacheBytes: 16 << 20, RenderAll: true}, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.frames) <= initialFrames || sess.preparedFrames != len(sess.frames) || renderFailed(sess) {
		t.Fatal("full preparation did not retain all images")
	}
}

func TestPrefetchFillsAheadAndStopsOnClose(t *testing.T) {
	s := New()
	sess, err := encodeClassic(bytes.Repeat([]byte("data"), 2000), 512, "qr", txqr.CodecRaptorQ, frameSize{ppm: 2})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions["x"] = sess
	if _, err := s.framePNG(context.Background(), "x", 0); err != nil {
		t.Fatal(err)
	}
	s.prefetch("x", 0)
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ready = sess.pngs[1] != nil
		s.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !ready || sess.prefetchContext.Err() == nil {
		t.Fatal("prefetch failed or outlived close")
	}
}

func TestConcurrentFrameRequestsShareRenderAndCancelDoesNotBlock(t *testing.T) {
	s := New()
	defer s.Close()
	sess, err := encodeClassic([]byte("ABC"), 512, "qr", txqr.CodecLT, frameSize{ppm: 4})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions["x"] = sess
	entered, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	s.renderFn = func(string, []byte, int, int) []byte {
		if count.Add(1) == 1 {
			close(entered)
		}
		<-release
		return []byte("PNG")
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.framePNG(context.Background(), "x", 0); results <- err }()
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("render did not start")
	}
	r := httptest.NewRecorder()
	s.handleCancel(r, httptest.NewRequest("DELETE", "/cancel/x", nil))
	close(release)
	wg.Wait()
	close(results)
	if count.Load() != 1 {
		t.Fatalf("concurrent frame rendered %d times", count.Load())
	}
	for err := range results {
		if !errors.Is(err, errMissingSession) {
			t.Fatal("deleted session published PNG", err)
		}
	}
}

func TestPNGCacheEvictsAndRegenerates(t *testing.T) {
	s := New()
	defer s.Close()
	sess := &session{frames: make([][]byte, 70), pngs: make([][]byte, 70), modules: 29, ppm: 2, mode: "qr"}
	for i := range sess.frames {
		sess.frames[i] = []byte("frame")
	}
	s.sessions["x"] = sess
	image := make([]byte, 2<<20)
	renders := 0
	s.renderFn = func(string, []byte, int, int) []byte { renders++; return image }
	for i := range sess.frames {
		if _, err := s.framePNG(context.Background(), "x", i); err != nil {
			t.Fatal(err)
		}
	}
	if sess.pngBytes > maxPNGCacheBytes || sess.pngs[0] != nil || sess.pngs[69] == nil {
		t.Fatal("cache did not evict oldest images")
	}
	before := renders
	if _, err := s.framePNG(context.Background(), "x", 0); err != nil || renders != before+1 {
		t.Fatal("evicted frame could not be regenerated", err)
	}
}

func BenchmarkPrepareQR1MiB(b *testing.B) {
	for _, all := range []bool{false, true} {
		name := "Initial64"
		if all {
			name = "AllFrames"
		}
		b.Run(name, func(b *testing.B) {
			data := make([]byte, 1<<20)
			for i := range data {
				data[i] = byte(i*37 + i/97)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sess, err := encodeClassic(data, 2048, "qr", txqr.CodecRaptorQ, frameSize{ppm: 4}, "1MiB.bin")
				if err != nil {
					b.Fatal(err)
				}
				n := min(initialFrames, len(sess.frames))
				if all {
					n = len(sess.frames)
				}
				renderFrameCount(sess, context.Background(), n)
				for _, p := range sess.pngs[:n] {
					if p == nil {
						b.Fatal("initial render failed")
					}
				}
			}
		})
	}
}

func BenchmarkRenderQR2048(b *testing.B) {
	data := make([]byte, 46<<10)
	for i := range data {
		data[i] = byte(i*19 + i/31)
	}
	sess, err := encodeClassic(data, 2048, "qr", txqr.CodecRaptorQ, frameSize{ppm: 4})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if renderByMode(sess.mode, sess.frames[i%len(sess.frames)], sess.ppm, sess.modules) == nil {
			b.Fatal("render failed")
		}
	}
}

func BenchmarkPNGLevels(b *testing.B) {
	img, err := qr.Encode("0/2048/4096|"+string(bytes.Repeat([]byte("abcdefgh"), 256)), 724, qr.Medium)
	if err != nil {
		b.Fatal(err)
	}
	for _, level := range []png.CompressionLevel{png.DefaultCompression, png.BestSpeed} {
		name := "Default"
		if level == png.BestSpeed {
			name = "BestSpeed"
		}
		b.Run(name, func(b *testing.B) {
			encoder := png.Encoder{CompressionLevel: level}
			b.ReportAllocs()
			size := 0
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var out bytes.Buffer
				if err := encoder.Encode(&out, img); err != nil {
					b.Fatal(err)
				}
				size = out.Len()
			}
			b.ReportMetric(float64(size), "PNG_B")
		})
	}
}

func TestFastPNGPreservesEveryPixel(t *testing.T) {
	img, err := qr.Encode("0/2048/4096|"+string(bytes.Repeat([]byte("abcdefgh"), 256)), 724, qr.Medium)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(encodePNG(img)))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds() != img.Bounds() {
		t.Fatal("PNG dimensions changed")
	}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			rr, gg, bb, aa := decoded.At(x, y).RGBA()
			if r != rr || g != gg || b != bb || a != aa {
				t.Fatal("fast PNG changed QR pixels")
			}
		}
	}
}
