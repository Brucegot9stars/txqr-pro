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

	// id + audit identify this region in the diagnostic log.
	id    int
	audit *Audit

	// Stall detection: frames that keep arriving while no new block is
	// accepted are the classic "it worked a minute ago" symptom. Counting
	// duplicates over time is what separates that from a decode failure.
	lastGain  time.Time
	noGain    int
	mismatch  int // consecutive frames whose header disagrees with the session
	lastIssue string
}

// sessionSwitchAfter is how many consecutive frames with a foreign header we
// accept before concluding the sender started a new transfer and adopting it.
// One stray frame (a screen still showing the previous file for a moment) must
// not reset a working transfer.
const sessionSwitchAfter = 15

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
	raw, err, attempts := qr.DecodeRawMultiScaleVerbose(rgba, []float64{1.0, 0.6, 1.7})
	payload := qr.ExtractFramePayload(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.captured++
	if err != nil || payload == nil {
		s.decodeFail++
		s.saveDebugFrame(rgba)
		s.noteDecodeFailureLocked(rgba, err, attempts)
		return nil
	}
	return payload
}

// noteDecodeFailureLocked explains a failed capture. "No QR at all" and "a QR
// whose payload is not one of our frames" look identical in a failure counter
// but have nothing in common: the first is a framing/focus problem, the second
// usually means the screen shows a protocol this receiver does not speak (for
// example a Color Matrix frame, which only the Android app can decode).
func (s *decodeState) noteDecodeFailureLocked(rgba *image.RGBA, err error, attempts []qr.Attempt) {
	if s.audit == nil {
		return
	}
	b := rgba.Bounds()
	found, withPayload := 0, 0
	firstErr := ""
	for _, at := range attempts {
		if at.Found {
			found++
		}
		if at.Payload {
			withPayload++
		}
		if firstErr == "" && at.Err != nil {
			firstErr = at.Err.Error()
		}
	}
	if found > 0 && withPayload == 0 {
		s.note("qr.found-no-payload", AuditWarn, fmt.Sprintf(
			"框 %dx%d：%d/%d 次尝试检出了二维码，但载荷不是 txqr/decimen 帧（可能是 Color Matrix 帧——桌面端不支持——或屏幕上的其它二维码）",
			b.Dx(), b.Dy(), found, len(attempts)))
		return
	}
	if firstErr == "" && err != nil {
		firstErr = err.Error()
	}
	if firstErr == "" {
		firstErr = "未知"
	}
	s.note("qr.notfound", AuditWarn, fmt.Sprintf(
		"框 %dx%d：%d 次尝试（crop/bare/放大）都没检出二维码；首次错误 %s",
		b.Dx(), b.Dy(), len(attempts), firstErr))
}

// note records a diagnostic line for this region and remembers it for the UI.
func (s *decodeState) note(reason, level, detail string) {
	if s.audit == nil {
		return
	}
	s.audit.Note(s.id, level, reason, detail)
	s.lastIssue = s.audit.LastFor(s.id).Label
}

// noteAlways is note() for rare, must-see events.
func (s *decodeState) noteAlways(reason, level, detail string) {
	if s.audit == nil {
		return
	}
	s.audit.Always(s.id, level, reason, detail)
	s.lastIssue = s.audit.LastFor(s.id).Label
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
		// The magic matched (feedPayload checked) but the length does not, which
		// means the capture caught a partially rendered or partially read frame.
		s.note("frame.decimen-invalid", AuditWarn, fmt.Sprintf(
			"decimen 帧头无效：长度 %d，前 24 字节 %s（期望 magic d1 0d、长度 = 20 + blockLen）",
			len(frame), hexPrefix(frame, 24)))
		return false
	}
	if s.decimenDec == nil || !sameHeader(s.decimenHdr, f.Header) {
		detail := fmt.Sprintf("decimen 传输：session=%d k=%d blockLen=%d 总长=%d",
			f.Header.SessionId, f.Header.K, f.Header.BlockLen, f.Header.TotalLen)
		if s.decimenDec != nil {
			detail = fmt.Sprintf("decimen 会话切换：旧 session=%d k=%d 总长=%d → 新 session=%d k=%d 总长=%d",
				s.decimenHdr.SessionId, s.decimenHdr.K, s.decimenHdr.TotalLen,
				f.Header.SessionId, f.Header.K, f.Header.TotalLen)
		}
		s.noteAlways("frame.session-new", AuditInfo, detail)
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
		s.lastGain = time.Now()
		s.noGain = 0
	} else {
		s.noGain++
		s.noteStallLocked(fmt.Sprintf("已解出 %d/%d 块", s.decimenDec.SolvedCount(), f.Header.K))
	}
	if s.decimenDec.IsComplete() {
		container := s.decimenDec.Assemble()
		if file, err := decimen.UnpackContainer(container); err == nil {
			if shaMatches(container, file.Bytes) {
				s.completed = true
				s.endTime = time.Now()
				s.result = file.Bytes
				s.fileName = file.Name
				s.noteAlways("result.complete", AuditInfo, fmt.Sprintf(
					"decimen 传输完成：%s（%d 字节），共 %d 帧 %.1fs",
					file.Name, len(file.Bytes), s.frames, s.endTime.Sub(s.startTime).Seconds()))
				return true
			}
			s.note("frame.dropped", AuditError,
				"decimen 容器组装完成但 SHA-256 校验失败——收到的数据有误，请重新扫描")
			return false
		}
	}
	return false
}

// noteStallLocked reports "frames keep arriving but nothing new is being
// accepted", which is the single most misleading symptom: the UI looks alive
// (frames counter climbs) while the transfer is dead. Sample-limited by the
// audit, and re-armed so it does not repeat every frame.
func (s *decodeState) noteStallLocked(progress string) {
	if s.lastGain.IsZero() || time.Since(s.lastGain) < 5*time.Second {
		return
	}
	rate := float64(s.noGain) / time.Since(s.lastGain).Seconds()
	s.note("fountain.stalled", AuditWarn, fmt.Sprintf(
		"%s，最近 %.1fs 内 %d 帧全是重复（约 %.0f 帧/秒）——发送端可能暂停/换文件，或区域只拍到同一帧",
		progress, time.Since(s.lastGain).Seconds(), s.noGain, rate))
	s.lastGain = time.Now()
	s.noGain = 0
}

// splitFrameHeader parses "blockCode/chunkLen/total[/codec]".
func splitFrameHeader(h string) (code, chunkLen, total int, codec string, ok bool) {
	parts := strings.Split(h, "/")
	if len(parts) < 3 {
		return 0, 0, 0, "", false
	}
	c, err1 := strconv.Atoi(parts[0])
	cl, err2 := strconv.Atoi(parts[1])
	t, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || cl <= 0 || t <= 0 {
		return 0, 0, 0, "", false
	}
	if len(parts) >= 4 {
		codec = parts[3]
	}
	return c, cl, t, codec, true
}

// adoptSessionLocked records the header of the transfer this region is now
// following. The log line is the single most useful record when a transfer
// "sometimes works": it says which total/chunk/codec the receiver locked onto.
func (s *decodeState) adoptSessionLocked(code, chunkLen, total int, codec string) {
	s.txqrTotal = total
	s.txqrChunk = chunkLen
	s.txqrNeed = (total + chunkLen - 1) / chunkLen
	s.mismatch = 0
	s.noGain = 0
	s.lastGain = time.Now()
	name := codec
	if name == "" {
		name = "lt"
	}
	s.noteAlways("frame.session-new", AuditInfo, fmt.Sprintf(
		"识别到 txqr 传输：chunk=%d 总长=%d 需要 %d 块 编码=%s 首帧 blockCode=%d",
		chunkLen, total, s.txqrNeed, name, code))
}

func (s *decodeState) feedTxqrLocked(payload []byte) bool {
	if s.txqrDec == nil {
		s.txqrDec = txqr.NewDecoder()
	}
	s.mode = "txqr"
	s.frames++

	chunk := string(payload)
	idx := strings.IndexByte(chunk, '|')
	if idx <= 0 {
		s.note("frame.header-invalid", AuditWarn, fmt.Sprintf(
			"帧头缺少 '|' 分隔符：长度 %d，前 40 字节 %s",
			len(payload), printablePrefix(payload, 40)))
		return false
	}
	code, chunkLen, total, codec, ok := splitFrameHeader(chunk[:idx])
	if !ok {
		s.note("frame.header-invalid", AuditWarn, fmt.Sprintf(
			"帧头 %q 无法解析（期望 blockCode/chunkLen/total[/codec]），前 40 字节 %s",
			truncateForLog(chunk[:idx], 40), printablePrefix(payload, 40)))
		return false
	}
	if code < 0 {
		// Metadata frame: "-1/{chunk}/{total}/{codec}/name|{fileName}". It
		// carries no block, so counting it as progress would show the transfer
		// one block further along than it is.
		if strings.HasSuffix(chunk[:idx], "/name") {
			s.fileName = chunk[idx+1:]
			s.noteAlways("frame.name", AuditInfo, "文件名帧："+s.fileName)
			return false
		}
		s.note("frame.header-invalid", AuditWarn, "未知的负 blockCode："+chunk[:idx])
		return false
	}

	switch {
	case s.txqrChunk == 0:
		s.adoptSessionLocked(code, chunkLen, total, codec)
	case chunkLen != s.txqrChunk || total != s.txqrTotal:
		// A frame built for a different total/chunk cannot help this decoder:
		// it was created for the old session. Skipping keeps a stale frame on
		// screen from poisoning a working run.
		s.mismatch++
		s.note("frame.session-mismatch", AuditWarn, fmt.Sprintf(
			"本帧 chunk=%d 总长=%d，当前会话 chunk=%d 总长=%d（连续第 %d 帧）",
			chunkLen, total, s.txqrChunk, s.txqrTotal, s.mismatch))
		if s.mismatch < sessionSwitchAfter {
			return false
		}
		s.noteAlways("frame.session-switch", AuditInfo, fmt.Sprintf(
			"连续 %d 帧与当前会话不一致，判定换了文件：重置解码器，改用 chunk=%d 总长=%d",
			s.mismatch, chunkLen, total))
		s.txqrDec = txqr.NewDecoder()
		s.txqrSeen = nil
		s.txqrGot = 0
		s.adoptSessionLocked(code, chunkLen, total, codec)
	default:
		s.mismatch = 0
	}

	if s.txqrSeen == nil {
		s.txqrSeen = map[int]struct{}{}
	}
	if _, dup := s.txqrSeen[code]; !dup {
		s.txqrSeen[code] = struct{}{}
		s.txqrGot++
		s.bytes += int64(len(payload))
		s.pushSpeedSample()
		s.lastGain = time.Now()
		s.noGain = 0
	} else {
		s.noGain++
		s.noteStallLocked(fmt.Sprintf("已收 %d/%d 块", s.txqrGot, s.txqrNeed))
	}

	if err := s.txqrDec.Decode(chunk); err != nil {
		s.note("frame.dropped", AuditWarn, "解码器拒绝该帧："+err.Error())
	}
	if s.txqrDec.IsCompleted() {
		s.completed = true
		s.endTime = time.Now()
		s.result = s.txqrDec.DataBytes()
		if s.fileName == "" {
			s.fileName = ""
		}
		s.noteAlways("result.complete", AuditInfo, fmt.Sprintf(
			"txqr 传输完成：%d 字节，%d 帧 / 需要 %d 块，用时 %.1fs",
			len(s.result), s.frames, s.txqrNeed, s.endTime.Sub(s.startTime).Seconds()))
		return true
	}
	return false
}

// truncateForLog keeps a header readable inside one log line.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
	// LastIssue is the newest diagnostic label for this region. It turns an
	// eternal "0/64 块" into an actionable sentence.
	LastIssue string
	Speed     int64
	AvgSpeed  int64
	Bytes     int64
	Frames    int
	Captured  int
	Failed    int
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
func (s *decodeState) stats() (speed, avg int64, bytes int64, frames int, captured, failed int, mode, issue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentSpeedBps(), s.avgSpeedBps(), s.bytes, s.frames, s.captured, s.decodeFail, s.mode, s.lastIssue
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
