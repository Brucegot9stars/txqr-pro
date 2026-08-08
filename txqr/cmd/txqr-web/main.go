// Command txqr-web serves a web-based GUI sender for txqr.
//
// Usage:
//
//	go run ./cmd/txqr-web
//
// Then open http://localhost:8080 in a browser, pick a file, and
// point the phone camera at the fullscreen QR codes.
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/qr"
)

//go:embed index.html
var indexHTML []byte

type session struct {
	frames    [][]byte
	pngs      [][]byte
	total     int
	createdAt time.Time
}

var (
	mu       sync.Mutex
	sessions = map[string]*session{}
)

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	flag.Parse()

	if f := openLog(); f != nil {
		log.SetOutput(f)
		defer f.Close()
	} else {
		log.SetOutput(io.Discard)
	}

	go cleanup()

	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/encode", handleEncode)
	http.HandleFunc("/md5", handleMD5)
	http.HandleFunc("/frame/", handleFrame)
	http.HandleFunc("/cancel/", handleCancel)

	log.Printf("TXQR web sender listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// openLog redirects logging to a file next to the executable so the
// process never blocks on a console/stderr that may be absent (hidden
// window or double-clicked launcher).
func openLog() *os.File {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(exe), "txqr-web.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func handleEncode(w http.ResponseWriter, r *http.Request) {
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	str := string(body)
	sum := md5.Sum(body)
	encoder := txqr.NewEncoder(split, codec)
	if codec == txqr.CodecOnline {
		encoder.SetOnlineParams(onlineEpsilon, onlineQuality)
	}
	frames, err := encoder.Encode(str)
	if err != nil {
		http.Error(w, "encode: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s := &session{
		frames:    make([][]byte, len(frames)),
		pngs:      make([][]byte, len(frames)),
		total:     len(str),
		createdAt: time.Now(),
	}
	for i, f := range frames {
		s.frames[i] = []byte(f)
	}

	// Render all QR frames up front in parallel using every available core,
	// so the encode step itself is multi-core and the play loop serves
	// pre-rendered PNGs instantly. The loop honors the client request
	// context so a cancelled upload aborts rendering early.
	renderFrames(s, r.Context())

	if renderFailed(s) {
		http.Error(w, "encode: QR render failed", http.StatusInternalServerError)
		return
	}
	if r.Context().Err() != nil {
		return // client aborted/cancelled; drop the partial session
	}

	id := newID()
	mu.Lock()
	sessions[id] = s
	mu.Unlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":    id,
		"count": len(frames),
		"total": len(str),
		"md5":   hex.EncodeToString(sum[:]),
	})
}

// handleMD5 computes and returns the MD5 of an uploaded file body without
// encoding anything, so the UI can show the checksum as soon as a file is
// selected (matching the md5 the Android receiver displays on completion).
func handleMD5(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	sum := md5.Sum(body)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"md5": hex.EncodeToString(sum[:]),
	})
}

func handleFrame(w http.ResponseWriter, r *http.Request) {
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

	mu.Lock()
	s, ok := sessions[id]
	if ok && idx >= 0 && idx < len(s.frames) && s.pngs[idx] == nil {
		s.pngs[idx] = renderPNG(s.frames[idx])
	}
	var png []byte
	if ok {
		png = s.pngs[idx]
	}
	mu.Unlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

func handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/cancel/")
	if id == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	mu.Lock()
	delete(sessions, id)
	mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func renderPNG(frame []byte) []byte {
	img, err := qr.Encode(string(frame), 512, qr.Medium)
	if err != nil {
		log.Printf("QR encode error: %v", err)
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("PNG encode error: %v", err)
		return nil
	}
	return buf.Bytes()
}

// renderFrames pre-renders every frame's PNG using a worker pool sized to the
// number of available CPUs. It stops scheduling new work as soon as the
// request context is cancelled so a client abort still clears the pool.
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
				s.pngs[i] = renderPNG(s.frames[i])
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

func cleanup() {
	for range time.Tick(time.Minute) {
		mu.Lock()
		for k, s := range sessions {
			if time.Since(s.createdAt) > 30*time.Minute {
				delete(sessions, k)
			}
		}
		mu.Unlock()
	}
}
