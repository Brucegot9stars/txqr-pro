package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"github.com/divan/txqr/qr"
)

// decodeState holds the receive-side decoders and progress stats. All access
// goes through the mutex; the capture loop and the UI both touch it.
type decodeState struct {
	mu sync.Mutex

	decimenDec *decimen.LTDecoder
	decimenHdr decimen.Header
	txqrDec    *txqr.Decoder

	startTime time.Time
	lastTime  time.Time
	frames    int
	bytes     int64
	captured  int     // QR decode attempts
	decodeFail int    // attempts that yielded no payload
	savedDebug int    // debug frames written
	completed bool
	result    []byte
	fileName  string
	mode      string // "decimen" or "txqr"
	// SaveFails keeps undecodable captures to %TEMP%\qrt-debug when enabled
	// (a diagnosis aid; writing every failed frame slows the receiver).
	SaveFails bool

	speedWnd []speedSample // sliding window of (time, effective bytes)

	// txqr progress bookkeeping (txqr.Decoder.Read() is unimplemented → 0)
	txqrTotal int
	txqrChunk int
	txqrNeed  int
	txqrGot   int
	txqrSeen  map[int]struct{}
}

// speedSample records cumulative effective bytes at a moment; the window's
// delta drives the live speed line.
type speedSample struct {
	t time.Time
	b int64
}

func (s *decodeState) saveDebugFrame(rgba *image.RGBA) {
	if !s.SaveFails || s.savedDebug >= 20 {
		return
	}
	dir := filepath.Join(os.TempDir(), "qrt-debug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("fail_%d_%dx%d.png", s.savedDebug, rgba.Bounds().Dx(), rgba.Bounds().Dy()))
	f, err := os.Create(name)
	if err != nil {
		return
	}
	png.Encode(f, rgba)
	f.Close()
	s.savedDebug++
}

func (s *decodeState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decimenDec = nil
	s.txqrDec = nil
	s.startTime = time.Time{}
	s.lastTime = time.Time{}
	s.frames = 0
	s.bytes = 0
	s.speedWnd = nil
	s.txqrTotal, s.txqrChunk, s.txqrNeed, s.txqrGot = 0, 0, 0, 0
	s.txqrSeen = nil
	s.completed = false
	s.result = nil
	s.fileName = ""
	s.mode = ""
}

// feed decodes one captured frame and routes it to the decimen or classic
// decoder. Returns true when the transfer just completed.
func (s *decodeState) feed(rgba *image.RGBA) bool {
	payload := s.decode(rgba)
	if payload == nil {
		return false
	}
	return s.feedPayload(payload)
}

// decode runs gozxing on a frame and updates counters. Thread-safe against
// other decoders (each region's decodeState is independent), so multiple
// capture workers can decode in parallel.
func (s *decodeState) decode(rgba *image.RGBA) []byte {
	// try native, then smaller, then larger — a real capture can have the QR
	// at any relative size in the boxed region
	raw, err := qr.DecodeRawMultiScale(rgba, []float64{1.0, 0.6, 1.7})
	payload := qr.ExtractFramePayload(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.captured++
	if err != nil || payload == nil {
		s.decodeFail++
		s.saveDebugFrame(rgba)
		return nil
	}
	return payload
}

// feedPayload hands a decoded frame to the (single) decoder. Serialized per
// region — the LT/txqr decoders are not thread-safe, so only one caller feeds
// at a time. Cheap (µs), so it never becomes the bottleneck.
func (s *decodeState) feedPayload(payload []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed {
		return true
	}
	if s.startTime.IsZero() {
		s.startTime = time.Now()
	}
	s.lastTime = time.Now()

	if len(payload) >= 2 && payload[0] == 0xD1 && payload[1] == 0x0D {
		return s.feedDecimenLocked(payload)
	}
	return s.feedTxqrLocked(payload)
}

func (s *decodeState) feedDecimenLocked(frame []byte) bool {
	f := decimen.ParseFrame(frame)
	if f == nil {
		return false
	}
	if s.decimenDec == nil || !sameHeader(s.decimenHdr, f.Header) {
		s.decimenHdr = f.Header
		s.decimenDec = decimen.NewDecoder(f.Header)
	}
	s.mode = "decimen"
	s.frames++
	// Count ONLY genuinely new payload bytes (dedup by seq) as "effective":
	// repeated frames the camera re-reads inflate a raw byte counter.
	before := s.decimenDec.FramesNew
	s.decimenDec.AddFrame(f.Header.Seq, f.Payload)
	if s.decimenDec.FramesNew > before {
		s.bytes += int64(len(f.Payload))
		s.pushSpeedSample()
	}
	if s.decimenDec.IsComplete() {
		container := s.decimenDec.Assemble()
		if file, err := decimen.UnpackContainer(container); err == nil {
			if shaMatches(container, file.Bytes) {
				s.completed = true
				s.result = file.Bytes
				s.fileName = file.Name
				return true
			}
		}
	}
	return false
}

func (s *decodeState) feedTxqrLocked(payload []byte) bool {
	if s.txqrDec == nil {
		s.txqrDec = txqr.NewDecoder()
	}
	s.mode = "txqr"
	s.frames++
	// txqr.Decoder.Read() is a stub returning 0, so track dedup + progress
	// ourselves from the frame header ("blockCode/chunkLen/total[/codec]|...").
	chunk := string(payload)
	if idx := strings.IndexByte(chunk, '|'); idx > 0 {
		if parts := strings.Split(chunk[:idx], "/"); len(parts) >= 3 {
			code, err1 := strconv.Atoi(parts[0])
			cl, err2 := strconv.Atoi(parts[1])
			total, err3 := strconv.Atoi(parts[2])
			if err1 == nil && err2 == nil && cl > 0 && err3 == nil && total > 0 {
				if s.txqrTotal == 0 {
					s.txqrTotal = total
					s.txqrChunk = cl
					s.txqrNeed = (total + cl - 1) / cl
				}
				if s.txqrSeen == nil {
					s.txqrSeen = map[int]struct{}{}
				}
				if _, dup := s.txqrSeen[code]; !dup {
					s.txqrSeen[code] = struct{}{}
					s.txqrGot++
					s.bytes += int64(len(payload))
					s.pushSpeedSample()
				}
			}
		}
	}
	s.txqrDec.Decode(chunk)
	if s.txqrDec.IsCompleted() {
		s.completed = true
		s.result = s.txqrDec.DataBytes()
		s.fileName = ""
		return true
	}
	return false
}

func sameHeader(a, b decimen.Header) bool {
	return a.SessionId == b.SessionId && a.K == b.K && a.BlockLen == b.BlockLen &&
		a.TotalLen == b.TotalLen && a.PayloadFnv == b.PayloadFnv
}

func shaMatches(container, file []byte) bool {
	// The DCF2 header carries the SHA-256 of the original file in bytes 17..49.
	if len(container) < 49 {
		return false
	}
	want := container[17:49]
	got := sha256.Sum256(file)
	return bytes.Equal(got[:], want)
}

// snapshot copies the stats the UI needs, under the mutex. byBlocks selects
// the decimen progress flavor: solved blocks vs frames collected.
func (s *decodeState) snapshot(byBlocks bool) (progress int, status string, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed {
		return 100, "完成", true
	}
	p := 0
	if s.decimenDec != nil {
		if byBlocks {
			p = s.decimenDec.SolvedCount() * 100 / int(s.decimenHdr.K)
		} else {
			// Progress by frames COLLECTED (new seqs), not blocks solved: the
			// peeling cascade back-loads — solved count stalls ~50% then jumps.
			// The receiver needs about k*1.05 distinct frames to finish.
			need := int(float64(s.decimenHdr.K) * 1.05)
			if need < 1 {
				need = 1
			}
			p = s.decimenDec.FramesNew * 100 / need
		}
		if p > 99 {
			p = 99
		}
	} else if s.txqrDec != nil {
		if s.txqrNeed > 0 {
			p = s.txqrGot * 100 / s.txqrNeed
			if p > 99 {
				p = 99
			}
		}
	}
	status = fmtStatus(s.mode, s.frames, s.captured, s.decodeFail, s.bytes, s.currentSpeedBps())
	return p, status, false
}

// currentSpeedBps is the live effective-data rate: a sliding ~5s window of
// deduplicated parsed bytes. Falls back to the session average when the window
// has fewer than two samples (sparse valid frames), so the line never shows a
// bogus 0 between slow arrivals.
func (s *decodeState) currentSpeedBps() int64 {
	if len(s.speedWnd) >= 2 {
		last := s.speedWnd[len(s.speedWnd)-1]
		first := s.speedWnd[0]
		span := last.t.Sub(first.t)
		if span > 0 {
			delta := last.b - first.b
			if delta > 0 {
				return int64(float64(delta) / span.Seconds())
			}
		}
	}
	if s.bytes > 0 {
		elapsed := time.Since(s.startTime)
		if elapsed > 0 {
			return int64(float64(s.bytes) / elapsed.Seconds())
		}
	}
	return 0
}

// pushSpeedSample records cumulative effective bytes; used by the live speed.
func (s *decodeState) pushSpeedSample() {
	s.speedWnd = append(s.speedWnd, speedSample{t: time.Now(), b: s.bytes})
	cutoff := time.Now().Add(-5 * time.Second)
	for len(s.speedWnd) > 1 && s.speedWnd[0].t.Before(cutoff) {
		s.speedWnd = s.speedWnd[1:]
	}
}

func fmtStatus(mode string, frames, captured, decodeFail int, bytes, speedBps int64) string {
	speed := "0 KB/s"
	if speedBps > 0 {
		speed = fmtBytes(speedBps) + "/s"
	}
	if mode == "decimen" {
		return fmt.Sprintf("捕获%d · 解码失败%d · 有效帧%d · 有效 %s · %s",
			captured, decodeFail, frames, fmtBytes(bytes), speed)
	}
	return fmt.Sprintf("捕获%d · 解码失败%d · 有效帧%d · %s · %s",
		captured, decodeFail, frames, fmtBytes(bytes), speed)
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
