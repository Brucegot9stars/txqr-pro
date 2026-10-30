package sender

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/divan/txqr/internal/version"
)

type encodeResponse struct {
	http.ResponseWriter
	status  int
	message bytes.Buffer
}

func (w *encodeResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *encodeResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *encodeResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= 400 && w.message.Len() < 2048 {
		w.message.Write(data[:min(len(data), 2048-w.message.Len())])
	}
	return w.ResponseWriter.Write(data)
}

type senderDiagnostics struct {
	mu         sync.Mutex
	path       string
	lines      []string
	writeError string
}

func newSenderDiagnostics() *senderDiagnostics {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "qrt-pro", "logs")
	if os.MkdirAll(dir, 0700) != nil {
		dir = filepath.Join(os.TempDir(), "qrt-pro", "logs")
	}
	return &senderDiagnostics{path: filepath.Join(dir, "sender.log")}
}

// One current file plus two 2 MiB backups. Logging failure never fails a transfer;
// the visible in-memory tail still explains the error and the failed log write.
func (d *senderDiagnostics) record(level, stage string, fields map[string]any) {
	bounded := make(map[string]any, len(fields))
	for key, value := range fields {
		if text, ok := value.(string); ok && len(text) > 4096 {
			value = text[:4096] + "…"
		}
		bounded[key] = value
	}
	data, _ := json.Marshal(map[string]any{"time": time.Now().Format(time.RFC3339Nano), "version": version.Number, "pid": os.Getpid(), "level": level, "stage": stage, "detail": bounded})
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, string(data))
	if len(d.lines) > 200 {
		d.lines = append([]string(nil), d.lines[len(d.lines)-200:]...)
	}
	err := os.MkdirAll(filepath.Dir(d.path), 0700)
	if err == nil {
		if info, e := os.Stat(d.path); e == nil && info.Size()+int64(len(data)+1) > 2<<20 {
			_ = os.Remove(d.path + ".2")
			_ = os.Rename(d.path+".1", d.path+".2")
			err = os.Rename(d.path, d.path+".1")
		}
	}
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(d.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, err = file.Write(append(data, '\n'))
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		d.writeError = err.Error()
	} else {
		d.writeError = ""
	}
}

func (s *Server) note(level, stage string, fields map[string]any) {
	if s.diagnostics != nil {
		s.diagnostics.record(level, stage, fields)
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	d := s.diagnostics
	if d == nil {
		http.Error(w, "diagnostics unavailable", http.StatusServiceUnavailable)
		return
	}
	d.mu.Lock()
	result := map[string]any{"version": version.Number, "path": d.path, "writeError": d.writeError, "lines": append([]string{}, d.lines...)}
	d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		s.note("error", "logs.response", map[string]any{"error": fmt.Sprint(err)})
	}
}
