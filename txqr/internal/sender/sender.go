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
const DefaultMaxUpload = 256 << 20

// session is one encoded transfer waiting to be played out frame by frame.
type session struct {
	frames    [][]byte
	pngs      [][]byte
	total     int
	mode      string // "qr", "color" or "decimen"
	createdAt time.Time

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

	listener  net.Listener
	srv       *http.Server
	mux       *http.ServeMux
	stopOnce  sync.Once
	done      chan struct{}
	maxUpload int64
}

// New builds a Server with no network activity.
func New() *Server {
	s := &Server{
		sessions:  map[string]*session{},
		done:      make(chan struct{}),
		maxUpload: DefaultMaxUpload,
	}
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
	s.mu.Lock()
	s.listener = ln
	s.srv = srv
	s.mu.Unlock()

	go func() {
		defer close(s.done)
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("sender: serve stopped: %v", err)
		}
	}()
	go s.cleanup()
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

// Close stops serving and releases the listener. It is idempotent and safe to
// call before Start.
func (s *Server) Close() error {
	var err error
	s.stopOnce.Do(func() {
		s.mu.Lock()
		srv, ln := s.srv, s.listener
		s.srv, s.listener = nil, nil
		s.mu.Unlock()
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = srv.Shutdown(ctx)
			// Shutdown waits for idle clients; unblock listeners promptly.
			if cerr := srv.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
		_ = ln
	})
	return err
}

// Done returns a channel closed once Serve returns. Closing the server from
// the UI is enough in practice; this exists for tests and the standalone cmd.
func (s *Server) Done() <-chan struct{} { return s.done }

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
// ticker and stops when the server stops, unlike the old time.Tick loop that
// never terminated.
func (s *Server) cleanup() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			for k, v := range s.sessions {
				if time.Since(v.createdAt) > 30*time.Minute {
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

	split := 1024
	if v := r.URL.Query().Get("split"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			split = n
		}
	}

	mode := "qr"
	if v := r.URL.Query().Get("mode"); v == "color" || v == "decimen" {
		mode = v
	}

	codec := txqr.ParseCodec(r.URL.Query().Get("codec"))
	onlineEpsilon := 0.01
	if v := r.URL.Query().Get("epsilon"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			onlineEpsilon = f
		}
	}
	onlineQuality := 3
	if v := r.URL.Query().Get("quality"); v != "" {
		if q, err := strconv.Atoi(v); err == nil && q > 0 {
			onlineQuality = q
		}
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

	var sess *session
	switch mode {
	case "decimen":
		sess, err = encodeDecimen(r, body)
	default:
		sess, err = encodeClassic(body, split, mode, codec, onlineEpsilon, onlineQuality)
	}
	if err != nil {
		http.Error(w, "encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sess.name = r.URL.Query().Get("name")
	sess.size = len(body)

	// Render every frame up front across all cores; the play loop then serves
	// pre-baked PNGs. The client's context aborts the render on cancel.
	renderFrames(sess, r.Context())
	if renderFailed(sess) {
		if r.Context().Err() != nil {
			return // client aborted; nothing usable to report
		}
		http.Error(w, "encode: QR render failed", http.StatusInternalServerError)
		return
	}
	if r.Context().Err() != nil {
		return // dropped mid-flight: don't publish the partial session
	}

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
	}
	if mode == "decimen" {
		resp["carousel"] = true
		resp["k"] = sess.k
		resp["sessionId"] = sess.sessionID
		resp["blockLen"] = sess.blockLen
		resp["split"] = sess.blockLen
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxUpload))
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

// encodeClassic is the text-based pipeline (QR/color, LT/Raptor/…).
func encodeClassic(body []byte, split int, mode string, codec txqr.CodecType,
	onlineEpsilon float64, onlineQuality int) (*session, error) {
	str := string(body)
	encoder := txqr.NewEncoder(split, codec)
	if codec == txqr.CodecOnline {
		encoder.SetOnlineParams(onlineEpsilon, onlineQuality)
	}
	frames, err := encoder.Encode(str)
	if err != nil {
		return nil, err
	}
	s := &session{
		frames:    make([][]byte, len(frames)),
		pngs:      make([][]byte, len(frames)),
		total:     len(str),
		mode:      mode,
		createdAt: time.Now(),
	}
	for i, f := range frames {
		s.frames[i] = []byte(f)
	}
	return s, nil
}

// encodeDecimen builds the DCF2 container plus one full carousel cycle (2k
// frames: a systematic sweep then k repair frames). Replaying the cycle forever
// lets the receiver finish from any starting point; repeats are counted as
// duplicates, never as corruption.
func encodeDecimen(r *http.Request, body []byte) (*session, error) {
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
		mode:      "decimen",
		createdAt: time.Now(),
		carousel:  true,
		k:         k,
		sessionID: sessionID,
		blockLen:  DecimenBlockLen,
	}
	for seq := 0; seq < cycle; seq++ {
		h := hdr
		h.Seq = uint32(seq)
		s.frames[seq] = decimen.PackFrame(h, enc.Encode(uint32(seq)))
	}
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
		pngBytes = renderByMode(sess.mode, sess.frames[idx])
		if pngBytes != nil {
			sess.pngs[idx] = pngBytes
		}
	default:
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

func renderByMode(mode string, frame []byte) []byte {
	switch mode {
	case "color":
		return renderColorPNG(frame)
	case "decimen":
		return renderDecimenPNG(frame)
	default:
		return renderPNG(frame)
	}
}

func renderPNG(frame []byte) []byte {
	img, err := qr.Encode(string(frame), 512, qr.Medium)
	if err != nil {
		log.Printf("QR encode error: %v", err)
		return nil
	}
	return encodePNG(img)
}

// renderDecimenPNG pins V40-L with a fixed mask: identical length, ECC and mask
// mean identical geometry across frames — required for grid tiling — and the
// pinned mask skips the spec's 8-way mask evaluation.
func renderDecimenPNG(frame []byte) []byte {
	img, err := qr.EncodeVersioned(string(frame), 512, qr.Low, DecimenQRVersion, DecimenQRMask)
	if err != nil {
		log.Printf("decimen QR encode error: %v", err)
		return nil
	}
	return encodePNG(img)
}

// renderColorPNG renders a frame as a 4-colour matrix (2 bit/module).
func renderColorPNG(frame []byte) []byte {
	img, err := qr.ColorMatrixEncode(frame, 512)
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
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	jobs := make(chan int, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.pngs[i] = renderByMode(s.mode, s.frames[i])
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
