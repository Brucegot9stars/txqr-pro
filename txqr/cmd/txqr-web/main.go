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

	split := 100
	if v := r.URL.Query().Get("split"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			split = n
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	str := string(body)
	frames, err := txqr.NewEncoder(split).Encode(str)
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

	id := newID()
	mu.Lock()
	sessions[id] = s
	mu.Unlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":    id,
		"count": len(frames),
		"total": len(str),
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
