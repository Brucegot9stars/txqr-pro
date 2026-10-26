package scanner

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/divan/txqr/internal/w32"
)

// Rect is captured in virtual-screen coordinates. It duplicates w32.Rect rather
// than aliasing it so the pipeline stays testable without linking the UI layer.
type Rect struct {
	L, T, R, B int32
}

// W/H are the rect extents, never negative.
func (r Rect) W() int32 {
	if r.R <= r.L {
		return 0
	}
	return r.R - r.L
}

func (r Rect) H() int32 {
	if r.B <= r.T {
		return 0
	}
	return r.B - r.T
}

// CaptureFunc grabs a screen rectangle and returns both an RGBA image for
// decoding and the raw BGRA bytes for the preview pane.
type CaptureFunc func(r Rect) (*image.RGBA, []byte, error)

// DefaultCapture captures through the Win32 screen DC.
func DefaultCapture(r Rect) (*image.RGBA, []byte, error) {
	return w32.Capture(w32.Rect{L: r.L, T: r.T, R: r.R, B: r.B})
}

// FrameInterval is the capture cadence (~60 fps). A frame that overruns simply
// skips the next tick rather than queueing behind itself.
const FrameInterval = 16 * time.Millisecond

// maxDecodeWorkers caps parallel gozxing decodes; beyond this the extra cores
// only add contention (and each worker holds a full RGBA frame).
const maxDecodeWorkers = 8

// SavedFile describes one completed transfer the pipeline wrote to disk.
type SavedFile struct {
	Name    string
	Path    string
	Size    int
	Mode    string
	Time    time.Time
	Errored bool
	Err     string
}

// Options configures a Manager. Both callbacks run on worker goroutines, never
// on the UI thread, so implementations must marshal to their own thread before
// touching widgets.
type Options struct {
	// SaveDir receives finished transfers. Empty means "the user's Downloads".
	SaveDir string
	// OnUpdate fires when something the UI polls has changed (usually "repaint").
	OnUpdate func()
	// OnSaved reports each write attempt, including failures, so a problem can
	// never be silently swallowed as it used to be.
	OnSaved func(SavedFile)
	// SaveFails keeps a bounded number of undecodable frames for diagnosis.
	SaveFails bool
}

// Manager owns every scan region and the worker pools feeding them.
type Manager struct {
	capture CaptureFunc
	opts    Options

	mu      sync.Mutex
	regions []*region
	nextID  int

	// progressByBlocks toggles the decimen flavour: solved blocks vs frames.
	progressByBlocks bool
	saveFails        bool

	saveDir string

	decQueue  chan decJob
	saveQueue chan saveJob

	previewMu sync.Mutex
	preview   []byte
	previewW  int
	previewH  int
	previewID int

	savedMu sync.Mutex
	saved   []SavedFile

	// qMu serialises "am I allowed to send" against channel close; without it a
	// worker can send on a closed queue and panic during shutdown.
	qMu    sync.RWMutex
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// region is one user-selected screen rectangle being scanned independently —
// several can run at once, typically one per monitor.
type region struct {
	id   int
	rect Rect
	st   *decodeState

	serial      int
	scanning    bool
	done        bool
	doneOnce    atomic.Int32
	cancelCap   context.CancelFunc
	labelDetail string
}

type decJob struct {
	r    *region
	gen  int
	rgba *image.RGBA
}

type saveJob struct {
	data []byte
	name string
	mode string
}

// New builds a Manager and starts its worker pools. Close must be called to
// release them; the pools are not tied to process exit.
func New(capture CaptureFunc, opts Options) *Manager {
	if capture == nil {
		capture = DefaultCapture
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		capture:   capture,
		opts:      opts,
		saveFails: opts.SaveFails,
		decQueue:  make(chan decJob, 2*decodeWorkers()),
		saveQueue: make(chan saveJob, 16),
		ctx:       ctx,
		cancel:    cancel,
	}
	m.saveDir = m.resolveSaveDir(opts.SaveDir)

	n := decodeWorkers()
	m.wg.Add(n + 1) // n decode workers + the saver
	for i := 0; i < n; i++ {
		go func() {
			defer m.wg.Done()
			m.decWorker()
		}()
	}
	go func() {
		defer m.wg.Done()
		m.saver()
	}()
	return m
}

func decodeWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n < 2 {
		n = 2
	}
	if n > maxDecodeWorkers {
		n = maxDecodeWorkers
	}
	return n
}

// resolveSaveDir picks a writable directory. Downloads first, then the folder
// holding the executable, then the OS temp dir — always something that exists,
// because the old code could end up writing into an arbitrary CWD.
func (m *Manager) resolveSaveDir(want string) string {
	candidates := []string{}
	if want != "" {
		candidates = append(candidates, want)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "Downloads"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	candidates = append(candidates, os.TempDir())
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			continue
		}
		return abs
	}
	return "."
}

// SaveDir reports where transfers land.
func (m *Manager) SaveDir() string { return m.saveDir }

// SetSaveDir switches the destination; existing files are untouched.
func (m *Manager) SetSaveDir(dir string) {
	if dir == "" {
		return
	}
	m.mu.Lock()
	m.saveDir = m.resolveSaveDir(dir)
	m.mu.Unlock()
}

// SetProgressMode switches between decimen block-solved and frame-collected
// progress readouts.
func (m *Manager) SetProgressMode(byBlocks bool) {
	m.mu.Lock()
	m.progressByBlocks = byBlocks
	m.mu.Unlock()
	m.notifyUpdate()
}

// ProgressMode reports the current flavour.
func (m *Manager) ProgressMode() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progressByBlocks
}

// SetSaveFails toggles keeping undecodable frames for diagnosis.
func (m *Manager) SetSaveFails(on bool) {
	m.mu.Lock()
	m.saveFails = on
	regions := append([]*region(nil), m.regions...)
	m.mu.Unlock()
	for _, r := range regions {
		r.st.SaveFails.Set(on)
	}
}

// SaveFails reports whether failed frames are kept.
func (m *Manager) SaveFails() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveFails
}

// ---------------------------------------------------------------- region API

// AddRegion registers a rectangle and returns its id. The caller is responsible
// for choosing sane coordinates (see the selection overlay in the UI layer).
func (m *Manager) AddRegion(r Rect) int {
	if r.W() <= 0 || r.H() <= 0 {
		return -1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := m.nextID
	st := &decodeState{}
	st.SaveFails.Set(m.saveFails)
	reg := &region{
		id:          id,
		rect:        r,
		st:          st,
		labelDetail: fmt.Sprintf("%dx%d", r.W(), r.H()),
	}
	m.regions = append(m.regions, reg)
	return id
}

// RemoveRegion stops and drops one region.
func (m *Manager) RemoveRegion(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.regions {
		if r.id != id {
			continue
		}
		m.stopRegionLocked(r)
		m.regions = append(m.regions[:i], m.regions[i+1:]...)
		m.mu.Unlock()
		m.notifyUpdate()
		m.mu.Lock()
		return
	}
}

// Clear stops and removes every region and drops the preview.
func (m *Manager) Clear() {
	m.mu.Lock()
	regions := m.regions
	m.regions = nil
	for _, r := range regions {
		m.stopRegionLocked(r)
	}
	m.mu.Unlock()
	m.previewMu.Lock()
	m.preview, m.previewW, m.previewH, m.previewID = nil, 0, 0, 0
	m.previewMu.Unlock()
	m.notifyUpdate()
}

// Start begins (or restarts) scanning one region. A completed region is reset
// first, so "scan another" no longer requires deleting and re-boxing it.
func (m *Manager) Start(id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.byIDLocked(id)
	if r == nil {
		return fmt.Errorf("unknown region %d", id)
	}
	if r.scanning {
		return nil
	}
	if r.done {
		r.st.reset()
		r.done = false
		r.doneOnce.Store(0)
	}
	r.serial++
	gen := r.serial
	ctx, cancel := context.WithCancel(m.ctx)
	r.cancelCap = cancel
	r.scanning = true
	go m.runCapture(r, gen, ctx)
	return nil
}

// Stop halts one region. Idempotent.
func (m *Manager) Stop(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.byIDLocked(id); r != nil {
		m.stopRegionLocked(r)
	}
}

// StartAll starts every region that is neither running nor freshly completed,
// which is what the toolbar button means after adding a box.
func (m *Manager) StartAll() error {
	m.mu.Lock()
	var first error
	for _, r := range m.regions {
		if r.scanning || r.done {
			continue
		}
		r.serial++
		gen := r.serial
		ctx, cancel := context.WithCancel(m.ctx)
		r.cancelCap = cancel
		r.scanning = true
		go m.runCapture(r, gen, ctx)
	}
	m.mu.Unlock()
	m.notifyUpdate()
	return first
}

// StopAll halts every region. The selection overlay calls it before snapshotting
// the desktop, because a fullscreen window would otherwise block captures.
func (m *Manager) StopAll() {
	m.mu.Lock()
	for _, r := range m.regions {
		m.stopRegionLocked(r)
	}
	m.mu.Unlock()
	m.notifyUpdate()
}

// AnyScanning reports whether at least one region is capturing.
func (m *Manager) AnyScanning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.regions {
		if r.scanning {
			return true
		}
	}
	return false
}

// RegionCount reports how many regions exist.
func (m *Manager) RegionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.regions)
}

func (m *Manager) byIDLocked(id int) *region {
	for _, r := range m.regions {
		if r.id == id {
			return r
		}
	}
	return nil
}

// stopRegionLocked must be called with mu held.
func (m *Manager) stopRegionLocked(r *region) {
	if !r.scanning {
		return
	}
	r.scanning = false
	r.serial++
	if r.cancelCap != nil {
		r.cancelCap()
		r.cancelCap = nil
	}
}

// ---------------------------------------------------------------- snapshots

// Snapshots returns a copy-out view of every region, ready to paint.
func (m *Manager) Snapshots() []Snapshot {
	m.mu.Lock()
	regions := append([]*region(nil), m.regions...)
	byBlocks := m.progressByBlocks
	m.mu.Unlock()

	out := make([]Snapshot, 0, len(regions))
	for _, r := range regions {
		p, status, done := r.st.snapshot(byBlocks)
		speed, avg, nbytes, frames, captured, failed, mode := r.st.stats()
		m.mu.Lock()
		scanning := r.scanning
		m.mu.Unlock()
		s := Snapshot{
			ID:       r.id,
			Label:    fmt.Sprintf("区域 %d · %s", r.id, r.labelDetail),
			W:        int(r.rect.W()),
			H:        int(r.rect.H()),
			Scanning: scanning,
			Done:     done || r.done,
			Progress: clampPct(p),
			Status:   status,
			Mode:     mode,
			Speed:    speed,
			AvgSpeed: avg,
			Bytes:    nbytes,
			Frames:   frames,
			Captured: captured,
			Failed:   failed,
		}
		out = append(out, s)
	}
	return out
}

func clampPct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// Preview returns the most recent captured frame and its dimensions. The bytes
// are copied so callers can paint after releasing every lock.
func (m *Manager) Preview() ([]byte, int, int, bool) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	if len(m.preview) == 0 || m.previewW <= 0 || m.previewH <= 0 {
		return nil, 0, 0, false
	}
	out := make([]byte, len(m.preview))
	copy(out, m.preview)
	return out, m.previewW, m.previewH, true
}

// Result returns the assembled bytes of a completed region, for "save as".
func (m *Manager) Result(id int) ([]byte, string, bool) {
	m.mu.Lock()
	r := m.byIDLocked(id)
	m.mu.Unlock()
	if r == nil {
		return nil, "", false
	}
	return r.st.completedResult()
}

// SavedFiles lists completed writes, newest first.
func (m *Manager) SavedFiles() []SavedFile {
	m.savedMu.Lock()
	defer m.savedMu.Unlock()
	out := make([]SavedFile, len(m.saved))
	copy(out, m.saved)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// SaveResult writes one completed region's bytes to path.
func (m *Manager) SaveResult(id int, path string) error {
	data, _, ok := m.Result(id)
	if !ok || len(data) == 0 {
		return errors.New("该区域没有已完成的传输")
	}
	return os.WriteFile(path, data, 0o644)
}

// ---------------------------------------------------------------- pipeline

func (m *Manager) notifyUpdate() {
	if m.opts.OnUpdate != nil {
		m.opts.OnUpdate()
	}
}

// runCapture captures the region at a fixed cadence. Decoding happens on the
// worker pool, so one slow frame never stalls the timer. Frames are dropped when
// every worker is busy: a stale frame is worth less than the next one.
func (m *Manager) runCapture(r *region, gen int, ctx context.Context) {
	ticker := time.NewTicker(FrameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		current := r.serial == gen && r.scanning
		rect := r.rect
		m.mu.Unlock()
		if !current {
			return
		}
		if rect.W() <= 0 || rect.H() <= 0 {
			return
		}
		rgba, bgra, err := m.capture(rect)
		if err != nil {
			continue
		}
		if bgra != nil {
			m.previewMu.Lock()
			m.preview = bgra
			m.previewW = int(rect.W())
			m.previewH = int(rect.H())
			m.previewID = r.id
			m.previewMu.Unlock()
		}
		m.submitDecode(decJob{r: r, gen: gen, rgba: rgba})
		m.notifyUpdate()
	}
}

// submitDecode never blocks the capture loop and never panics on shutdown.
func (m *Manager) submitDecode(j decJob) {
	m.qMu.RLock()
	defer m.qMu.RUnlock()
	if m.closed {
		return
	}
	select {
	case m.decQueue <- j:
	default: // every worker busy: drop this frame
	}
}

// decWorker decodes frames and feeds each region's single decoder. Several
// workers can converge on the same transfer; only the first hands it to the
// saver, and only after the region has been stopped.
func (m *Manager) decWorker() {
	for job := range m.decQueue {
		if job.rgba == nil {
			continue
		}
		m.mu.Lock()
		stale := job.r.serial != job.gen || !job.r.scanning
		m.mu.Unlock()
		if stale {
			continue // frame from a stopped or restarted scan
		}
		payload := job.r.st.decode(job.rgba)
		if payload == nil {
			continue
		}
		if !job.r.st.feedPayload(payload) {
			continue
		}
		// The CAS makes "who saves" exactly-once even though several workers may
		// observe completion in the same instant.
		if !job.r.doneOnce.CompareAndSwap(0, 1) {
			continue
		}
		m.mu.Lock()
		job.r.done = true
		m.mu.Unlock()
		m.Stop(job.r.id)

		data, name, ok := job.r.st.completedResult()
		if !ok || len(data) == 0 {
			continue
		}
		m.submitSave(saveJob{data: data, name: name, mode: "auto"})
	}
}

func (m *Manager) submitSave(j saveJob) {
	m.qMu.RLock()
	defer m.qMu.RUnlock()
	if m.closed {
		return
	}
	// Completion events are rare; block rather than drop the transfer.
	select {
	case m.saveQueue <- j:
	case <-m.ctx.Done():
	}
}

// saver is the single writer for completed transfers, keeping disk I/O off the
// decode workers and reporting every outcome — including failures the old code
// hid behind a "success" dialog.
func (m *Manager) saver() {
	for job := range m.saveQueue {
		path, err := m.writeToDisk(job)
		rec := SavedFile{
			Name:    filepath.Base(path),
			Path:    path,
			Size:    len(job.data),
			Mode:    job.mode,
			Time:    time.Now(),
			Errored: err != nil,
		}
		if err != nil {
			rec.Err = err.Error()
		}
		m.savedMu.Lock()
		m.saved = append(m.saved, rec)
		if len(m.saved) > 100 {
			copy(m.saved, m.saved[len(m.saved)-100:])
			m.saved = m.saved[:100]
		}
		m.savedMu.Unlock()
		m.notifyUpdate()
		if m.opts.OnSaved != nil {
			m.opts.OnSaved(rec)
		}
	}
}

// writeToDisk writes job to the save dir under a sanitised, non-clobbering name.
func (m *Manager) writeToDisk(job saveJob) (string, error) {
	m.mu.Lock()
	dir := m.saveDir
	m.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建保存目录 %s: %w", dir, err)
	}
	name := SanitizeFileName(job.name, filepath.Join(dir, "received.bin"))
	path := uniquePath(filepath.Join(dir, name))
	if err := os.WriteFile(path, job.data, 0o644); err != nil {
		return path, fmt.Errorf("写入 %s: %w", path, err)
	}
	return path, nil
}

// uniquePath avoids silently overwriting an earlier receive of the same file:
// a second transfer of "a.txt" becomes "a (2).txt".
func uniquePath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return fmt.Sprintf("%s (%d)%s", base, time.Now().Unix(), ext)
}

// sanitizeRunes are the characters Windows forbids in file names.
const sanitizeRunes = "<>:\"\\/|?*`"

// SanitizeFileName reduces any received name to something writable. The name
// arrives from the wire inside the transfer container, so trusting it would let
// a sender choose "../startup.bat" or contain NULs and path separators. Fallback
// is used when nothing usable remains.
func SanitizeFileName(name, fallback string) string {
	var b strings.Builder
	trimmed := strings.TrimSpace(name)
	// Only ever keep the base name: any directory component is dropped rather
	// than escaped, which is the difference between "rewrite" and "traverse".
	trimmed = filepath.Base(strings.ReplaceAll(trimmed, "\\", "/"))
	for _, r := range trimmed {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(sanitizeRunes, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if out == "" || out == "." || out == ".." || isReservedName(out) {
		out = filepath.Base(fallback)
	}
	if len([]rune(out)) > 120 {
		runes := []rune(out)
		ext := filepath.Ext(out)
		stem := string(runes[:120-len([]rune(ext))])
		out = stem + ext
	}
	return out
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func isReservedName(s string) bool {
	base := strings.ToUpper(s)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return reservedNames[base]
}

// Close stops every region and releases the worker pools. Safe to call twice.
func (m *Manager) Close() error {
	m.mu.Lock()
	for _, r := range m.regions {
		m.stopRegionLocked(r)
	}
	m.mu.Unlock()

	// Stop one save loop before touching the queues, so no worker is blocked
	// mid-send when they close.
	m.cancel()

	m.qMu.Lock()
	if !m.closed {
		m.closed = true
		close(m.decQueue)
		close(m.saveQueue)
	}
	m.qMu.Unlock()

	m.wg.Wait()
	return nil
}
