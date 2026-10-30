package sender

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/divan/txqr"
	"github.com/divan/txqr/internal/version"
	"github.com/divan/txqr/qr"
)

func TestPageContainsRuntimeVersionAndRealLimits(t *testing.T) {
	s := New()
	s.SetMaxUpload(8 << 20)
	r := httptest.NewRecorder()
	s.serveIndex(r, httptest.NewRequest("GET", "/", nil))
	part := regexp.MustCompile(`const APP_CONFIG = (.*);`).FindStringSubmatch(r.Body.String())
	if len(part) != 2 {
		t.Fatal("missing runtime configuration")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(part[1]), &config); err != nil {
		t.Fatal(err)
	}
	if config["version"] != version.Number || config["maxUploadBytes"] != float64(8<<20) ||
		config["maxSourceBlocks"] != float64(txqr.MaxSourceBlocks) || config["qrFrameBytes"] != float64(qr.MaxMediumBytes) {
		t.Fatal("UI limits/version do not match the server", config)
	}
	if strings.Contains(r.Body.String(), "/*APP_CONFIG*/") {
		t.Fatal("unexpanded page configuration")
	}
}

func TestOversizedQRRejectedBeforeEncodingWorker(t *testing.T) {
	s := New()
	r := httptest.NewRecorder()
	s.handleEncode(r, httptest.NewRequest("POST", "/encode?split=2900&codec=raptorq", bytes.NewReader(make([]byte, 5000))))
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "2331") || !strings.Contains(r.Body.String(), "协议头") {
		t.Fatalf("oversized QR did not get an actionable preflight error: %d %s", r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "encode worker") || len(s.sessions) != 0 {
		t.Fatal("invalid QR reached the worker or published a session")
	}
}

func TestClassicLimitsExplainBothFailureModes(t *testing.T) {
	for _, input := range []struct {
		size, split int
		want        string
	}{
		{25_585_664, 2048, "源块"},
		{47104, 2400, "2331"},
	} {
		err := validateClassicRequest(input.size, input.split, "qr", txqr.CodecRaptorQ, "file.bin")
		if err == nil || !strings.Contains(err.Error(), input.want) {
			t.Fatalf("size=%d split=%d: %v", input.size, input.split, err)
		}
	}
	if err := validateClassicRequest(47104, 2048, "qr", txqr.CodecRaptorQ, "file.bin"); err != nil {
		t.Fatal(err)
	}
}
