// Package scanner implements the desktop receive pipeline: capture screen
// rectangles at a fixed cadence, decode the QR frames, reassemble the transfer
// and hand the bytes to a single writer goroutine.
//
// It is deliberately UI-free. The unified app (cmd/qrt-pro) and any future
// headless wrapper share it; the UI observes it through snapshots and callbacks
// rather than touching its state directly. That separation is also what lets
// the concurrency bugs below stay fixed: nothing outside may read or write
// region state without going through this package's accessors.
package scanner

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
	"sync/atomic"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"github.com/divan/txqr/qr"
)

// decodeState holds the receive-side decoders and progress stats for one region.
// Every field is reachable only with mu held, except SaveFails, which is written
// from the UI thread and read by workers — hence its own atomic.
type decodeState struct {
	mu sync.Mutex

	decimenDec *decimen.LTDecoder
	decimenHdr decimen.Header
	txqrDec    *txqr.Decoder

	startTime  time.Time
	endTime    time.Time // frozen when the transfer completes (avg speed stays stable)
	lastTime   time.Time
	frames     int
	bytes      int64
	captured   int // QR decode attempts
	decodeFail int
	savedDebug int
	completed  bool
	result     []byte
	fileName   string
	mode       string // "decimen" or "txqr"
	SaveFails  SaveFailFlag

	speedWnd []speedSample // sliding window of (time, effective bytes)

	// txqr progress bookkeeping (txqr.Decoder.Read() is unimplemented → 0)
	txqrTotal int
	txqrChunk int
	txqrNeed  int
	txqrGot   int
	txqrSeen  map[int]struct{}
}

// SaveFailFlag is written by the UI thread and read by every decode worker, so
// it must not be a plain bool sharing a decodeState's other fields: racing that
// read/write pair would be undefined behaviour even though only one thread ever
// mutates the flag.
type SaveFailFlag struct{ v atomic.Int32 }

// Set stores the flag.
func (f *SaveFailFlag) Set(on bool) {
	if on {
		f.v.Store(1)
		return
	}
	f.v.Store(0)
}

// Get reports the flag.
func (f *SaveFailFlag) Get() bool { return f.v.Load() != 0 }

type speedSample struct {
	t time.Time
	b int64
}

// debugFrameLimit bounds how many undecodable captures ever hit disk, so
// "save failed frames" can never fill the temp dir.
const debugFrameLimit = 20

func (s *decodeState) saveDebugFrame(rgba *image.RGBA) {
	if !s.SaveFails.Get() || s.savedDebug >= debugFrameLimit {
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
	// Errors are intentionally swallowed (diagnostics must not break scanning),
	// but the file handle must still be closed.
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
	s.endTime = time.Time{}
	s.lastTime = time.Time{}
	s.frames = 0
	s.bytes = 0
	s.captured = 0
	s.decodeFail = 0
	s.speedWnd = nil
	s.txqrTotal, s.txqrChunk, s.txqrNeed, s.txqrGot = 0, 0, 0, 0
	s.txqrSeen = nil
	s.completed = false
	s.result = nil
	s.fileName = ""
	s.mode = ""
}

// decode runs gozxing on a frame and updates counters. Each region owns its own
// decodeState, so several capture workers may be inside this at once for
// different regions without colliding.
func (s *decodeState) decode(rgba *image.RGBA) []byte {
	// try native, then smaller, then larger — a real capture can have the QR at
	// any relative size inside the boxed region
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

// feedPayload hands a decoded frame to the (single) decoder. Serialised per
// region — the LT/txqr decoders are not thread-safe, and it is cheap (µs), so
// it never becomes the bottleneck.
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
	// repeated frames the camera re-reads would otherwise inflate the counter.
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
				s.endTime = time.Now()
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
		s.endTime = time.Now()
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

// Snapshot is a point-in-time, copy-out view of one region's progress. Nothing
// referenced here aliases live scanner state, so the UI can hold it freely.
type Snapshot struct {
	ID       int
	Label    string
	W, H     int
	Scanning bool
	Done     bool
	Progress int
	Status   string
	Mode     string
	Speed    int64
	AvgSpeed int64
	Bytes    int64
	Frames   int
	Captured int
	Failed   int
}

// snapshot copies the stats the UI needs, under the mutex. byBlocks selects the
// decimen progress flavour: solved blocks vs frames collected.
func (s *decodeState) snapshot(byBlocks bool) (progress int, status string, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed {
		return 100, "平均 " + FmtBytes(s.avgSpeedBps()) + "/s", true
	}
	p := 0
	if s.decimenDec != nil {
		if byBlocks {
			p = s.decimenDec.SolvedCount() * 100 / kDivisor(s.decimenHdr.K)
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

// kDivisor guards every percentage denominator derived from a frame header: a
// malformed or hostile frame carries K == 0 and would otherwise panic the whole
// receiver with an integer divide by zero.
func kDivisor(k uint16) int {
	if k == 0 {
		return 1
	}
	return int(k)
}

// stats returns the numeric readouts the UI trends over time.
func (s *decodeState) stats() (speed, avg int64, bytes int64, frames int, captured, failed int, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentSpeedBps(), s.avgSpeedBps(), s.bytes, s.frames, s.captured, s.decodeFail, s.mode
}

// completedResult copies out the assembled bytes; it must be reset() before the
// same region can be scanned again.
func (s *decodeState) completedResult() ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.completed {
		return nil, "", false
	}
	return s.result, s.fileName, true
}

// currentSpeedBps is the live effective-data rate: a sliding ~5s window of
// deduplicated parsed bytes. Falls back to the session average when the window
// has fewer than two samples, so the line never shows a bogus 0 between slow
// arrivals.
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
	return s.avgSpeedBps()
}

// avgSpeedBps is the whole-transfer effective-data rate: total deduplicated
// parsed bytes over the entire session. Uses the frozen endTime once complete,
// so the "done" figure stays stable.
func (s *decodeState) avgSpeedBps() int64 {
	if s.bytes <= 0 {
		return 0
	}
	end := s.endTime
	if end.IsZero() {
		end = time.Now()
	}
	elapsed := end.Sub(s.startTime)
	if elapsed <= 0 {
		return 0
	}
	return int64(float64(s.bytes) / elapsed.Seconds())
}

// pushSpeedSample records cumulative effective bytes; it trims the window
// eagerly so a long scan never accumulates unbounded samples.
func (s *decodeState) pushSpeedSample() {
	s.speedWnd = append(s.speedWnd, speedSample{t: time.Now(), b: s.bytes})
	cutoff := time.Now().Add(-5 * time.Second)
	for len(s.speedWnd) > 1 && s.speedWnd[0].t.Before(cutoff) {
		// Shift instead of re-slicing forever: repeated reslicing of a large
		// backing array retains memory that will never be used again.
		copy(s.speedWnd, s.speedWnd[1:])
		s.speedWnd = s.speedWnd[:len(s.speedWnd)-1]
	}
}

func fmtStatus(mode string, frames, captured, decodeFail int, bytes, speedBps int64) string {
	speed := "0 KB/s"
	if speedBps > 0 {
		speed = FmtBytes(speedBps) + "/s"
	}
	if mode == "decimen" {
		return fmt.Sprintf("捕获%d · 解码失败%d · 有效帧%d · 有效 %s · %s",
			captured, decodeFail, frames, FmtBytes(bytes), speed)
	}
	return fmt.Sprintf("捕获%d · 解码失败%d · 有效帧%d · %s · %s",
		captured, decodeFail, frames, FmtBytes(bytes), speed)
}

// FmtBytes renders a byte count with a binary unit.
func FmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// FmtElapsed renders a duration compactly for a status line.
func FmtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d >= time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
}
