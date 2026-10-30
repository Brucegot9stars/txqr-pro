package sender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSenderLogsRotateAndRemainValidJSON(t *testing.T) {
	d := &senderDiagnostics{path: filepath.Join(t.TempDir(), "sender.log")}
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(d.path, make([]byte, 2<<20), 0600); err != nil {
			t.Fatal(err)
		}
		d.record("error", "encode.error", map[string]any{"error": "容量超限\nquoted \"message\""})
	}
	files, _ := filepath.Glob(d.path + "*")
	if len(files) != 3 {
		t.Fatalf("rotation retained %d files", len(files))
	}
	current, _ := os.ReadFile(d.path)
	var entry map[string]any
	if err := json.Unmarshal(current, &entry); err != nil || entry["stage"] != "encode.error" {
		t.Fatal("invalid log JSON", err)
	}
}

func TestSenderLogFailureKeepsBoundedMemoryTail(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	d := &senderDiagnostics{path: filepath.Join(file, "sender.log")}
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 80; i++ {
				d.record("error", "worker.failure", map[string]any{"error": strings.Repeat("X", 5000)})
			}
		}()
	}
	wg.Wait()
	if len(d.lines) != 200 || d.writeError == "" || len(d.lines[0]) > 5000 {
		t.Fatal("disk failure lost diagnostics or memory bound")
	}
}
