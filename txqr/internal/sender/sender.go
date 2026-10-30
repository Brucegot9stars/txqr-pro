// Package sender implements the QRT web sender: an HTTP service that turns an
// uploaded file into a stream of QR frames and serves them as PNGs for the
// browser-based carousel UI (index.html).
//
// It is written to be embeddable: the unified desktop app runs it in-process,
// while cmd/txqr-web exposes it standalone. That constraint drives two rules
// that the original command violated:
//
//   - routes live on a private *http.ServeMux, never the DefaultServeMux, so an
//     embedding process keeps control of the global mux;
//   - every long-lived resource is owned by Server and released by Close, so
//     starting and stopping inside a GUI never leaks listeners or goroutines.
package sender

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"github.com/divan/txqr/qr"
)

//go:embed index.html
var indexHTML []byte

// Decimen wire constants: V40-L byte capacity minus the frame header.
const (
	DecimenFrameBytes = 2953
	DecimenBlockLen   = DecimenFrameBytes - decimen.HeaderLen
	DecimenQRVersion  = 40
	DecimenQRMask     = 4
	decimenMaxSourceK = 0xffff // k is a u16 in the frame header
)

// DefaultMaxUpload caps request bodies. Without a cap a client can pin the
// whole machine's memory with one POST (the classic pipeline also copies the
// body into a string, roughly doubling the transient footprint).
const DefaultMaxUpload = txqr.MaxTransferBytes

// session is one encoded transfer waiting to be played out frame by frame.
type session struct {
	frames         [][]byte
	pngs           [][]byte
	total          int
	split          int    // effective payload bytes per frame, after any mode clamp
	modules        int    // symbol modules per side, quiet zone included
	ppm            int    // device pixels per module the frames were rendered at
	mode           string // "qr", "color" or "decimen"
	createdAt      time.Time
	lastUsed       time.Time
	renderBudget   int64
	renderExceeded atomic.Bool

	// decimen mode: fountain metadata echoed to the UI
	carousel  bool
	k         int
	sessionID uint16
	blockLen  int

	name string
	size int
}

// Event records one completed encode so the host UI can show recent transfers.
type Event struct {
	Time   time.Time
	Name   string
	Size   int
	Mode   string
	Frames int
	MD5    string
}

// Stats is a point-in-time snapshot for the UI.
type Stats struct {
	Listening     bool
	Addr          string
	ActiveSession int
	Transfers     int
	BytesSent     int64
}

// Server is the sender HTTP service. Zero value is unusable; use New.
type Server struct {
	mu       sync.Mutex
	sessions map[string]*session
	events   []Event

	transfers int
	bytesSent int64

	listener net.Listener
	srv      *http.Server
	mux      *http.ServeMux
	// done belongs to the current run: Start replaces it, so a stop/start
	// cycle never reuses (and re-closes) a channel that is already closed.
	done       chan struct{}
	maxUpload  int64
	encodeSlot chan struct{}
}

// New builds a Server with no network activity.
func New() *Server {
	s := &Server{
		sessions:   map[string]*session{},
		done:       make(chan struct{}),
		maxUpload:  DefaultMaxUpload,
		encodeSlot: make(chan struct{}, 1),
	}
	// Before the first Start nothing runs, so Done() must report that instead
	// of blocking a caller that selects on it.
	close(s.done)
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/", s.serveIndex)
	s.mux.HandleFunc("/encode", s.handleEncode)
	s.mux.HandleFunc("/md5", s.handleMD5)
	s.mux.HandleFunc("/frame/", s.handleFrame)
	s.mux.HandleFunc("/cancel/", s.handleCancel)
	return s
}

// SetMaxUpload overrides the request body cap. Must be called before Start.
func (s *Server) SetMaxUpload(n int64) {
	if n > 0 {
		s.maxUpload = n
	}
}

// Start binds addr and serves until Close. addr may use port 0 to let the OS
// pick a free port; read Addr afterwards. Start is a no-op when already running.
// Start may be called again after Close: every run gets its own done channel.
func (s *Server) Start(addr string) error {
	s.mu.Lock()
	running := s.listener != nil
	s.mu.Unlock()
	if running {
		return nil
	}

	// Explicit timeouts: without them a stalled client (mobile hotspot drops
	// mid-upload) holds a goroutine and its buffers forever.
	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       0, // bodies are large and legitimately slow
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 16,
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	done := make(chan struct{})
	s.mu.Lock()
	if s.listener != nil { // lost a race with a concurrent Start
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.listener = ln
	s.srv = srv
	s.done = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("sender: serve stopped: %v", err)
		}
	}()
	go s.cleanup(done)
	return nil
}

// Addr reports the bound address ("host:port"), or "" when stopped.
// It expands ":9000" into a concrete host:port so the UI can print a URL that
// actually resolves.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addrLocked()
}

// addrLocked reads the bound address. Callers must hold s.mu — Go mutexes are
// not reentrant, so every public helper that already took the lock uses this
// instead of Addr. panic-free even when stopped (returns "").
func (s *Server) addrLocked() string {
	ln := s.listener
	if ln == nil {
		return ""
	}
	a := ln.Addr().String()
	if strings.HasPrefix(a, "[::]:") {
		return "127.0.0.1:" + a[strings.LastIndexByte(a, ':')+1:]
	}
	if strings.HasPrefix(a, "0.0.0.0:") {
		return "127.0.0.1:" + a[strings.LastIndexByte(a, ':')+1:]
	}
	return a
}

// LocalURL is the loopback URL of the sender page, for display and for the
// "open browser" action.
func (s *Server) LocalURL() string {
	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		return ""
	}
	if host == "" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// LANURLs returns non-loopback addresses so a second machine can drive the
// sender over the network.
func (s *Server) LANURLs() []string {
	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		return nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range addrs {
		ip, ok := a.(*net.IPNet)
		if !ok || ip.IP.IsLoopback() || ip.IP.To4() == nil {
			continue
		}
		u := "http://" + net.JoinHostPort(ip.IP.String(), port) + "/"
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// Close stops serving and releases the listener. It is idempotent, safe to call
// before Start, and safe to call repeatedly across Start/Close cycles: the
// server used to guard this with a single sync.Once, so the *second* stop after
// a restart silently did nothing and the port stayed open while the UI claimed
// the service had stopped.
func (s *Server) Close() error {
	s.mu.Lock()
	srv, ln, done := s.srv, s.listener, s.done
	s.srv, s.listener = nil, nil
	s.mu.Unlock()
	if srv == nil && ln == nil {
		return nil // nothing running
	}

	var err error
	if srv != nil {
		// Short graceful-drain window: the payloads served here are already
		// rendered PNGs, so anything in flight finishes well inside it. A
		// longer grace period would only make "stop" feel sluggish.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = srv.Shutdown(ctx)
		// Shutdown waits for in-flight requests; force whatever is left.
		if cerr := srv.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	if ln != nil {
		// Shutdown already closes it; closing again only makes the release
		// explicit for the case where srv was nil.
		_ = ln.Close()
	}
	// Wait for Serve to return so the port is really free before a restart,
	// but never hang the UI thread on a stuck handler.
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return err
}

// Done returns a channel closed once the *current* run's Serve returns. It is
// already closed before the first Start and after every Close, so selecting on
// it never blocks on a stopped server.
func (s *Server) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Stats snapshots what the UI wants to print.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Listening:     s.listener != nil,
		Addr:          s.addrLocked(),
		ActiveSession: len(s.sessions),
		Transfers:     s.transfers,
		BytesSent:     s.bytesSent,
	}
}

// Events returns the most recent encode events (newest first, capped).
func (s *Server) Events(n int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.events) {
		n = len(s.events)
	}
	out := make([]Event, n)
	for i := 0; i < n; i++ {
		out[i] = s.events[len(s.events)-1-i]
	}
	return out
}

func (s *Server) recordEvent(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if len(s.events) > 200 {
		// Keep the ring bounded: the UI only ever shows the tail.
		copy(s.events, s.events[len(s.events)-200:])
		s.events = s.events[:200]
	}
}

// cleanup drops sessions nobody has asked for recently. It runs on its own
// ticker and stops when the run it belongs to ends, unlike the old time.Tick
// loop that never terminated.
func (s *Server) cleanup(done <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.mu.Lock()
			for k, v := range s.sessions {
				last := v.lastUsed
				if last.IsZero() {
					last = v.createdAt
				}
				if time.Since(last) > 30*time.Minute {
					delete(s.sessions, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// ---------------------------------------------------------------- handlers

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleEncode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	select {
	case s.encodeSlot <- struct{}{}:
		defer func() { <-s.encodeSlot }()
	default:
		http.Error(w, "another encode is in progress", http.StatusTooManyRequests)
		return
	}
	s.mu.Lock()
	sessionsFull := len(s.sessions) >= 4
	var retained int64
	for _, existing := range s.sessions {
		for _, f := range existing.frames {
			retained += int64(len(f))
		}
		for _, p := range existing.pngs {
			retained += int64(len(p))
		}
	}
	s.mu.Unlock()
	if sessionsFull || retained >= 256<<20 {
		http.Error(w, "cancel an existing transfer before encoding another", http.StatusTooManyRequests)
		return
	}
	split := 1024
	if v := r.URL.Query().Get("split"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= txqr.MaxChunkBytes {
			split = n
		} else {
			http.Error(w, "invalid split", http.StatusBadRequest)
			return
		}
	}

	mode := "qr"
	if v := r.URL.Query().Get("mode"); v == "color" || v == "decimen" {
		mode = v
	}

	// fit is the device-pixel budget the page has for one code. It only decides
	// how many pixels each module gets; the page then displays the image at
	// exactly that size, so the browser never resamples it.
	size := frameSize{}
	if v := r.URL.Query().Get("fit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			size.fit = n
		}
	}
	// ppm is the explicit form: the user picked a module size in the UI.
	if v := r.URL.Query().Get("ppm"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			size.ppm = n
		}
	}

	// The color matrix holds far fewer bytes per frame than a QR symbol, and
	// the UI keeps the same split knob as classic mode. Without this clamp the
	// default split (2048) makes every frame fail to render and the transfer
	// silently emits nothing.
	if mode == "color" {
		split = clampColorSplit(split)
	}

	codecTag := r.URL.Query().Get("codec")
	codec := txqr.ParseCodec(codecTag)
	if codecTag != "" && codecTag != "lt" && string(codec) != codecTag {
		http.Error(w, "invalid codec", http.StatusBadRequest)
		return
	}

	body, err := s.readBody(w, r)
	if err != nil {
		return
	}
	if len(body) == 0 {
		http.Error(w, "empty request body", http.StatusBadRequest)
		return
	}

	sum := md5.Sum(body)

	sess, err := encodeIsolated(r.Context(), workerRequest{
		Mode: mode, Codec: codec, Name: r.URL.Query().Get("name"), MediaType: r.URL.Query().Get("type"),
		Split: split, Fit: size.fit, PPM: size.ppm, Size: len(body), MemoryBytes: workerMemoryBytes, CacheBytes: maxSessionBytes - retained,
	}, body, nil)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		http.Error(w, "encode: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	sess.name = r.URL.Query().Get("name")
	sess.lastUsed = time.Now()
	sess.size = len(body)

	id := newID()
	s.mu.Lock()
	s.sessions[id] = sess
	s.transfers++
	s.bytesSent += int64(len(body))
	s.mu.Unlock()
	s.recordEvent(Event{
		Time:   time.Now(),
		Name:   truncateName(sess.name, 96),
		Size:   len(body),
		Mode:   mode,
		Frames: len(sess.frames),
		MD5:    hex.EncodeToString(sum[:]),
	})

	resp := map[string]any{
		"id":    id,
		"count": len(sess.frames),
		"total": sess.total,
		"md5":   hex.EncodeToString(sum[:]),
		// Echo the payload size actually used: a mode clamp can make it
		// smaller than what the client asked for.
		"split": sess.split,
		// modules lets the page size the <img> to a whole number of device
		// pixels per module instead of scaling it to the viewport. See
		// fitFrames() in index.html: any integer module size decodes, any
		// fractional one is a lottery.
		"modules": sess.modules,
		// ppm is how many device pixels each module occupies in the frames the
		// server is serving; the page displays them at exactly modules*ppm.
		"ppm": sess.ppm,
	}
	if mode == "decimen" {
		resp["carousel"] = true
		resp["k"] = sess.k
		resp["sessionId"] = sess.sessionID
		resp["blockLen"] = sess.blockLen
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("sender: write encode response: %v", err)
	}
}

// readBody enforces the upload cap and distinguishes an oversize payload (413,
// actionable) from a broken pipe or cancel (400). Also writing its own error
// response lets callers keep a single-exit read path.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, min(s.maxUpload, int64(txqr.MaxTransferBytes))))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("file too large (limit %d bytes)", tooLarge.Limit),
				http.StatusRequestEntityTooLarge)
			return nil, err
		}
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return nil, err
	}
	return body, nil
}

func truncateName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// colorFrameOverhead reserves the maximum QRT2 header and its separator.
// It is reserved out of the matrix capacity
// so a color frame can never exceed what ColorMatrixEncode accepts.
const colorFrameOverhead = txqr.MaxFrameOverhead + 1

// clampColorSplit caps the payload size to what one color matrix frame can
// carry. The sender renders "header|payload" as a whole frame, so the header
// has to be reserved or every frame fails to render.
func clampColorSplit(split int) int {
	max := qr.NumColorPayloadBytes() - colorFrameOverhead
	if max < 1 {
		return split
	}
	if split > max {
		return max
	}
	return split
}

// encodeClassic is the text-based pipeline (QR/color, LT/Raptor/…).
func encodeClassic(body []byte, split int, mode string, codec txqr.CodecType, size frameSize, names ...string) (*session, error) {
	return encodeClassicContext(context.Background(), body, split, mode, codec, size, names...)
}

func encodeClassicContext(ctx context.Context, body []byte, split int, mode string, codec txqr.CodecType, size frameSize, names ...string) (*session, error) {
	str := string(body)
	encoder := txqr.NewEncoder(split, codec)
	if len(names) > 0 {
		encoder.SetFileName(names[0])
	}
	frames, err := encoder.EncodeContext(ctx, str)
	if err != nil {
		return nil, err
	}
	s := &session{
		frames:    make([][]byte, len(frames)),
		pngs:      make([][]byte, len(frames)),
		total:     len(str),
		split:     split,
		mode:      mode,
		createdAt: time.Now(),
	}
	for i, f := range frames {
		s.frames[i] = []byte(f)
	}
	s.modules = frameModules(mode, s.frames)
	s.ppm = size.forModules(s.modules)
	return s, nil
}

// frameModules reports the symbol's modules per side, quiet zone included, so
// the page can size the <img> to a whole number of device pixels per module.
// Zero means "unknown", which the page treats as "fall back to viewport width".
func frameModules(mode string, frames [][]byte) int {
	switch mode {
	case "color":
		return qr.ColorMatrixSize
	case "decimen":
		// Pinned V40: 177 data modules plus the 4-module quiet zone per side.
		return 21 + 4*(DecimenQRVersion-1) + 8
	}
	if len(frames) == 0 {
		return 0
	}
	// Header digit counts and metadata length vary; force every symbol to
	// the largest version required by any frame in this session.
	maxLen := 0
	for _, frame := range frames {
		if len(frame) > maxLen {
			maxLen = len(frame)
		}
	}
	return qr.ModulesForBytes(maxLen)
}

// encodeDecimen builds the DCF2 container plus one full carousel cycle (2k
// frames: a systematic sweep then k repair frames). Replaying the cycle forever
// lets the receiver finish from any starting point; repeats are counted as
// duplicates, never as corruption.
func encodeDecimen(r *http.Request, body []byte, size frameSize) (*session, error) {
	name := r.URL.Query().Get("name")
	mediaType := r.URL.Query().Get("type")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	container, _, err := decimen.PackContainer(name, mediaType, body)
	if err != nil {
		return nil, err
	}
	if len(container) == 0 {
		return nil, errors.New("empty container")
	}

	k := (len(container) + DecimenBlockLen - 1) / DecimenBlockLen
	if k < 1 {
		k = 1
	}
	if k > decimenMaxSourceK {
		return nil, fmt.Errorf("file needs %d blocks, over the %d-block u16 limit; use a larger frame", k, decimenMaxSourceK)
	}

	sessionID := randUint16()
	enc := decimen.NewEncoder(container, DecimenBlockLen, sessionID)
	hdr := decimen.Header{
		SessionId:  sessionID,
		Seq:        0,
		K:          uint16(k),
		BlockLen:   uint16(DecimenBlockLen),
		TotalLen:   uint32(len(container)),
		PayloadFnv: decimen.Fnv1a(container),
	}

	cycle := 2 * k
	s := &session{
		frames:    make([][]byte, cycle),
		pngs:      make([][]byte, cycle),
		total:     len(container),
		split:     DecimenBlockLen,
		mode:      "decimen",
		createdAt: time.Now(),
		carousel:  true,
		k:         k,
		sessionID: sessionID,
		blockLen:  DecimenBlockLen,
	}
	for seq := 0; seq < cycle; seq++ {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		h := hdr
		h.Seq = uint32(seq)
		s.frames[seq] = decimen.PackFrame(h, enc.Encode(uint32(seq)))
	}
	s.modules = frameModules("decimen", nil)
	s.ppm = size.forModules(s.modules)
	return s, nil
}

// randUint16 never returns 0 so a captured frame always distinguishes itself
// from an uninitialised header.
func randUint16() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err == nil {
		v := uint16(b[0]) | uint16(b[1])<<8
		if v != 0 {
			return v
		}
	}
	v := uint16(time.Now().UnixNano())
	if v == 0 {
		v = 1
	}
	return v
}

// handleMD5 returns the checksum of an uploaded body without encoding it, so the
// UI can show it as soon as a file is picked (matching what the receiver prints
// on completion).
func (s *Server) handleMD5(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.encodeSlot <- struct{}{}:
		defer func() { <-s.encodeSlot }()
	default:
		http.Error(w, "another upload is in progress", http.StatusTooManyRequests)
		return
	}
	body, err := s.readBody(w, r)
	if err != nil {
		return
	}
	sum := md5.Sum(body)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"md5": hex.EncodeToString(sum[:])})
}

func (s *Server) handleFrame(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/frame/"), "/")
	if len(parts) != 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	id, idxStr := parts[0], parts[1]
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	sess, ok := s.sessions[id]
	var pngBytes []byte
	badIndex := false
	switch {
	case !ok:
	case idx < 0 || idx >= len(sess.frames):
		// Index bound to the frame slice; pngs has identical length. Answering
		// 400 here keeps "no such frame" distinct from "frame failed to render".
		badIndex = true
	case sess.pngs[idx] == nil:
		sess.lastUsed = time.Now()
		pngBytes = renderByMode(sess.mode, sess.frames[idx], sess.ppm, sess.modules)
		if pngBytes != nil {
			sess.pngs[idx] = pngBytes
		}
	default:
		sess.lastUsed = time.Now()
		pngBytes = sess.pngs[idx]
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if badIndex {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}
	if pngBytes == nil {
		// The old handler answered 200 with an empty body here, which the
		// player silently rendered as a blank frame and then hung waiting for
		// the next one. Failing loudly lets the UI retry or stop.
		http.Error(w, "frame render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pngBytes)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/cancel/")
	if id == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- rendering

func renderByMode(mode string, frame []byte, ppm int, modules ...int) []byte {
	switch mode {
	case "color":
		return renderColorPNG(frame, ppm)
	case "decimen":
		return renderDecimenPNG(frame, ppm)
	default:
		if len(modules) > 0 && modules[0] > 0 {
			mods := modules[0]
			img, err := qr.EncodeVersioned(string(frame), ppm*mods, qr.Medium, (mods-25)/4, -1)
			if err != nil {
				return nil
			}
			return encodePNG(img)
		}
		return renderPNG(frame, ppm)
	}
}

// Module rendering: frames are rendered at an exact whole number of device
// pixels per module, and the page displays them at exactly that size. A barcode
// detector estimates one module width and then samples on a fixed grid, so any
// fractional module size makes some modules a pixel wider than others and the
// grid drifts; at whole-pixel modules every sample lands where it should.
// Measured at 1:1: 21 of 21 frame/size/margin combinations decoded, versus ~60%
// when the browser stretched the image to the viewport.
const (
	defaultPPM = 4 // used when the client does not ask for a specific fit
	minPPM     = 2 // below this a module is unreadable no matter the layout
	maxPPM     = 8 // beyond this the gain is nil and the PNG gets large
)

// frameSize is how the client asked for the frames to be rendered. Both fields
// are hints: ppm wins when given, otherwise the fit budget decides how many
// device pixels each module gets.
type frameSize struct {
	fit int // device-pixel budget for one code; 0 = unspecified
	ppm int // explicit device pixels per module; 0 = derive from fit
}

// forModules resolves the module size for a symbol of the given width. Any
// positive ppm is honoured (clamped into the usable range) — a value of 1 is a
// request for "as small as possible", not for "no opinion".
func (f frameSize) forModules(modules int) int {
	if f.ppm > 0 {
		if f.ppm < minPPM {
			return minPPM
		}
		if f.ppm > maxPPM {
			return maxPPM
		}
		return f.ppm
	}
	return pickPPM(f.fit, modules)
}

// pickPPM chooses the module size that best fills fitPixels without scaling.
func pickPPM(fitPixels, modules int) int {
	if fitPixels <= 0 || modules <= 0 {
		return defaultPPM
	}
	ppm := fitPixels / modules
	if ppm < minPPM {
		ppm = minPPM
	}
	if ppm > maxPPM {
		ppm = maxPPM
	}
	return ppm
}

func renderPNG(frame []byte, ppm int) []byte {
	img, err := qr.Encode(string(frame), -ppm, qr.Medium)
	if err != nil {
		log.Printf("QR encode error: %v", err)
		return nil
	}
	return encodePNG(img)
}

// renderDecimenPNG pins V40-L with a fixed mask: identical length, ECC and mask
// mean identical geometry across frames — required for grid tiling — and the
// pinned mask skips the spec's 8-way mask evaluation.
func renderDecimenPNG(frame []byte, ppm int) []byte {
	mods := frameModules("decimen", nil)
	img, err := qr.EncodeVersioned(string(frame), ppm*mods, qr.Low, DecimenQRVersion, DecimenQRMask)
	if err != nil {
		log.Printf("decimen QR encode error: %v", err)
		return nil
	}
	return encodePNG(img)
}

// renderColorPNG renders a frame as a 4-colour matrix (2 bit/module).
func renderColorPNG(frame []byte, ppm int) []byte {
	// ColorMatrixEncode floors to a whole number of pixels per cell itself.
	img, err := qr.ColorMatrixEncode(frame, ppm*qr.ColorMatrixSize)
	if err != nil {
		log.Printf("color encode error: %v", err)
		return nil
	}
	return encodePNG(img)
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("PNG encode error: %v", err)
		return nil
	}
	return buf.Bytes()
}

// renderFrames pre-renders every frame across available CPUs, stopping early
// when the request is cancelled so an aborted upload doesn't burn the pool.
func renderFrames(s *session, ctx context.Context) {
	n := len(s.frames)
	if n == 0 {
		return
	}
	workers := min(8, runtime.GOMAXPROCS(0))
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	var renderedBytes atomic.Int64
	jobs := make(chan int, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil || s.renderExceeded.Load() {
					continue
				}
				png := renderByMode(s.mode, s.frames[i], s.ppm, s.modules)
				if s.renderBudget > 0 && renderedBytes.Add(int64(len(png))) > s.renderBudget {
					s.renderExceeded.Store(true)
					continue
				}
				s.pngs[i] = png
			}
		}()
	}
send:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
}

// renderFailed reports whether any frame failed to render to a PNG.
func renderFailed(s *session) bool {
	for _, p := range s.pngs {
		if p == nil {
			return true
		}
	}
	return false
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
