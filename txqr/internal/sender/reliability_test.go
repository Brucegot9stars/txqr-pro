package sender

import (
	"bytes"
	"context"
	"github.com/divan/txqr"
	"github.com/divan/txqr/qr"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllFramesUseOneGeometryAndPreservePayload(t *testing.T) {
	s, err := encodeClassic(bytes.Repeat([]byte("abcd"), 384), 24, "qr", txqr.CodecRaptorQ, frameSize{ppm: 4}, "报告.bin")
	if err != nil {
		t.Fatal(err)
	}
	renderFrames(s, context.Background())
	for _, i := range []int{0, 1, 10, len(s.frames) - 1} {
		img, err := png.Decode(bytes.NewReader(s.pngs[i]))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != s.modules*s.ppm {
			t.Fatal("frame geometry changed")
		}
		payload, err := qr.Decode(img)
		if err != nil {
			t.Fatal(err)
		}
		if payload != string(s.frames[i]) {
			t.Fatal("render changed packet")
		}
		if _, err := txqr.ParseFrame(payload); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActiveFrameRefreshesExpiry(t *testing.T) {
	srv := New()
	s, err := encodeClassic([]byte("ABC"), 512, "qr", txqr.CodecLT, frameSize{ppm: 4})
	if err != nil {
		t.Fatal(err)
	}
	s.createdAt = time.Now().Add(-time.Hour)
	srv.sessions["test"] = s
	rec := httptest.NewRecorder()
	srv.handleFrame(rec, httptest.NewRequest("GET", "/frame/test/0", nil))
	if rec.Code != http.StatusOK || time.Since(s.lastUsed) > time.Second {
		t.Fatal("active session not renewed")
	}
}

func TestResourceBudgetAndEncodeAdmission(t *testing.T) {
	srv := New()
	srv.encodeSlot <- struct{}{}
	rec := httptest.NewRecorder()
	srv.handleEncode(rec, httptest.NewRequest("POST", "/encode", bytes.NewReader([]byte("abc"))))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatal("parallel encode admitted")
	}
	<-srv.encodeSlot
	s, err := encodeClassic([]byte("ABC"), 512, "qr", txqr.CodecLT, frameSize{ppm: 4})
	if err != nil {
		t.Fatal(err)
	}
	s.renderBudget = 1
	renderFrames(s, context.Background())
	if !s.renderExceeded.Load() {
		t.Fatal("cache budget ignored")
	}
}

func TestInvalidSplitRejectedBeforeEncoding(t *testing.T) {
	srv := New()
	for _, query := range []string{"0", "-1", "9999999999999999", "abc"} {
		rec := httptest.NewRecorder()
		srv.handleEncode(rec, httptest.NewRequest("POST", "/encode?split="+query, bytes.NewReader([]byte("abc"))))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("split %s: %d", query, rec.Code)
		}
	}
}
