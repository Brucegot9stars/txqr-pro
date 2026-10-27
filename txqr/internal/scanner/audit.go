package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Audit is the receiver's diagnostic log: why did this frame, this batch of
// frames, or this whole transfer fail?
//
// The receive path used to report only a failure count, which is useless for
// the question users actually ask ("it worked ten minutes ago, now it never
// completes"). A count cannot distinguish the four very different causes:
//
//	no QR on screen   → wrong region, display off, code too small
//	QR found, no payload → not a txqr frame (e.g. Color Matrix), or a
//	                       different protocol sharing the screen
//	header rejected   → frame truncated / sender and receiver disagree
//	decoder stalled    → frames arrive but they are all duplicates, so the
//	                     fountain never gains a new block
//
// Every entry carries a machine-readable reason plus a human-readable detail,
// so a log sent by a user can be read without guessing.
type Audit struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64

	ring []AuditEntry
	// seen holds the sampling state per (region, reason): the decode path runs
	// at frame rate, so logging every occurrence would write megabytes per
	// second and bury the signal. First occurrence is always logged, repeats
	// are folded into one line every flushEvery.
	seen map[auditKey]*auditNote
	// fileErr records why file logging is off, so the UI can say so instead of
	// silently pretending there is a log.
	fileErr string
	// maxBytes bounds the file before it rotates; a field so tests can shrink it.
	maxBytes int64

	now func() time.Time
}

type auditKey struct {
	region int
	reason string
}

type auditNote struct {
	count    int
	lastEmit time.Time
}

// AuditEntry is one line of the log, also what the UI can render.
type AuditEntry struct {
	At     time.Time
	Region int
	Level  string // "info" | "warn" | "error"
	Reason string // stable id, e.g. "qr.notfound"
	Label  string // short Chinese label for the UI
	Detail string
	Repeat int // occurrences folded into this line (1 = first time)
}

const (
	auditRingMax  = 400
	auditFlush    = 3 * time.Second
	auditMaxBytes = 4 << 20
)

// Levels used by Note.
const (
	AuditInfo  = "info"
	AuditWarn  = "warn"
	AuditError = "error"
)

// reasonLabels turns a stable reason id into the short text shown in the UI.
// The log file carries both, so a user can paste one line and it is readable.
var reasonLabels = map[string]string{
	"app.start":              "接收端启动",
	"region.start":           "开始扫描区域",
	"region.stop":            "停止扫描区域",
	"capture.error":          "屏幕捕获失败",
	"capture.empty":          "截到空画面",
	"qr.notfound":            "二维码未检出",
	"qr.found-no-payload":    "检出代码但载荷不可识别",
	"frame.decimen-invalid":  "decimen 帧头无效",
	"frame.header-invalid":   "txqr 帧头无法解析",
	"frame.session-mismatch": "帧头与当前会话不一致",
	"frame.session-switch":   "检测到新会话，已重置解码器",
	"frame.session-new":      "识别到新传输（帧头）",
	"frame.name":             "收到文件名帧",
	"region.too-small":       "框太小，装不下这个码",
	"region.dense":           "框偏小，解码会不稳定",
	"frame.dropped":          "帧被解码器拒绝",
	"fountain.stalled":       "长时间没有新块",
	"save.error":             "写入文件失败",
	"result.complete":        "传输完成",
}

// OpenAudit prepares a log at path. It never returns nil: if the file cannot be
// opened the receiver still runs, keeps the in-memory ring, and says why the
// file is missing through Entries rather than failing the whole pipeline.
func OpenAudit(path string) *Audit {
	a := &Audit{
		path:     path,
		seen:     map[auditKey]*auditNote{},
		now:      time.Now,
		maxBytes: auditMaxBytes,
	}
	if path == "" {
		a.fileErr = "未指定日志路径"
		return a
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		a.fileErr = err.Error()
		return a
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		a.fileErr = err.Error()
		return a
	}
	a.f = f
	if st, err := f.Stat(); err == nil {
		a.size = st.Size()
	}
	return a
}

// Path reports where entries are being written ("" when logging is memory-only).
func (a *Audit) Path() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return ""
	}
	return a.path
}

// FileError reports why file logging is unavailable, or "".
func (a *Audit) FileError() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fileErr
}

// Close flushes and closes the underlying file.
func (a *Audit) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		_ = a.f.Close()
		a.f = nil
	}
}

// Entries returns the newest entries, newest last (at most n).
func (a *Audit) Entries(n int) []AuditEntry {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > len(a.ring) {
		n = len(a.ring)
	}
	out := make([]AuditEntry, n)
	copy(out, a.ring[len(a.ring)-n:])
	return out
}

// LastFor returns the most recent entry for a region (zero when none).
func (a *Audit) LastFor(region int) AuditEntry {
	if a == nil {
		return AuditEntry{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.ring) - 1; i >= 0; i-- {
		if a.ring[i].Region == region {
			return a.ring[i]
		}
	}
	return AuditEntry{}
}

// Note records a sampled event: the first occurrence of a reason for a region
// is logged immediately, further occurrences are folded into one line every
// auditFlush so a 30 fps failure storm still reads like a timeline.
func (a *Audit) Note(region int, level, reason, detail string) {
	a.note(region, level, reason, detail, false)
}

// Always records an event that must not be collapsed: session changes,
// completion, region start/stop. These are rare by construction.
func (a *Audit) Always(region int, level, reason, detail string) {
	a.note(region, level, reason, detail, true)
}

func (a *Audit) note(region int, level, reason, detail string, force bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	key := auditKey{region: region, reason: reason}
	n := a.seen[key]
	if n == nil {
		n = &auditNote{}
		a.seen[key] = n
	}
	n.count++

	if !force {
		if n.lastEmit.IsZero() {
			// first occurrence, fall through and log it
		} else if now.Sub(n.lastEmit) < auditFlush {
			return
		}
	}
	repeat := n.count
	n.count = 0
	n.lastEmit = now

	a.emitLocked(AuditEntry{
		At:     now,
		Region: region,
		Level:  level,
		Reason: reason,
		Label:  labelFor(reason),
		Detail: oneLine(detail),
		Repeat: repeat,
	})
}

// emitLocked appends to the ring and the file. Caller holds a.mu.
func (a *Audit) emitLocked(e AuditEntry) {
	a.ring = append(a.ring, e)
	if len(a.ring) > auditRingMax {
		copy(a.ring, a.ring[len(a.ring)-auditRingMax:])
		a.ring = a.ring[:auditRingMax]
	}
	if a.f == nil {
		return
	}
	line := formatAuditLine(e)
	n, err := a.f.WriteString(line)
	if err == nil {
		a.size += int64(n)
		if a.size > a.maxBytes {
			a.rotateLocked()
		}
		return
	}
	// A broken log must never break receiving: remember why and go memory-only.
	a.fileErr = err.Error()
	_ = a.f.Close()
	a.f = nil
}

// rotateLocked keeps one previous generation so the log cannot grow without
// bound while still preserving the state right before the problem.
func (a *Audit) rotateLocked() {
	_ = a.f.Close()
	a.f = nil
	_ = os.Rename(a.path, a.path+".1")
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		a.fileErr = err.Error()
		return
	}
	a.f = f
	a.size = 0
}

func formatAuditLine(e AuditEntry) string {
	repeat := ""
	if e.Repeat > 1 {
		repeat = fmt.Sprintf(" ×%d", e.Repeat)
	}
	region := "全局"
	if e.Region > 0 {
		region = fmt.Sprintf("区域%d", e.Region)
	}
	line := fmt.Sprintf("%s  %-5s [%s] %s (%s)%s  %s\n",
		e.At.Format("2006-01-02 15:04:05.000"),
		strings.ToUpper(e.Level), region, e.Label, e.Reason, repeat, e.Detail)
	return line
}

func labelFor(reason string) string {
	if l, ok := reasonLabels[reason]; ok {
		return l
	}
	return reason
}

// oneLine keeps every entry on a single line: zxing errors and hex dumps can
// contain newlines, and a multi-line entry breaks grep and eyeballing alike.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// hexPrefix renders the first n bytes as hex, for identifying a frame we could
// not parse without dumping a whole payload into the log.
func hexPrefix(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "%02x", b[i])
	}
	if len(b) > n {
		sb.WriteString("…")
	}
	return sb.String()
}

// printablePrefix shows the start of a payload with non-printables escaped, so
// a broken header ("12/0/|x") is visible at a glance.
func printablePrefix(b []byte, n int) string {
	if len(b) < n {
		n = len(b)
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		c := b[i]
		if c >= 0x20 && c <= 0x7E {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "\\x%02x", c)
	}
	if len(b) > n {
		sb.WriteString("…")
	}
	return sb.String()
}
