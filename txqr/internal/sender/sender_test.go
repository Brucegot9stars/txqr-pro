package sender

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/divan/txqr/qr"
)

// startTestServer binds a free loopback port and fails the test loudly rather
// than leaking a listener if binding fails.
func startTestServer(t *testing.T) *Server {
	t.Helper()
	srv := New()
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func (s *Server) url(path string) string {
	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port) + path
}

func TestMD5MatchesUploadedBody(t *testing.T) {
	srv := startTestServer(t)
	body := []byte("hello qrt")
	resp, err := http.Post(srv.url("/md5"), "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post md5: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		MD5 string `json:"md5"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := md5.Sum(body)
	if out.MD5 != hex.EncodeToString(want[:]) {
		t.Fatalf("md5 %q want %q", out.MD5, hex.EncodeToString(want[:]))
	}
}

func TestIndexAndUnknownPath(t *testing.T) {
	srv := startTestServer(t)
	resp, err := http.Get(srv.url("/"))
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	resp404, err := http.Get(srv.url("/nope"))
	if err != nil {
		t.Fatalf("get /nope: %v", err)
	}
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected status %d", resp404.StatusCode)
	}
}

func TestEncodeFrameAndCancelRoundTrip(t *testing.T) {
	srv := startTestServer(t)
	payload := []byte(strings.Repeat("QRT frame payload ", 40))

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("thefile", "sample.txt")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatalf("write form: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.url("/encode?split=100"), &body)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post encode: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("encode status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode encode: %v", err)
	}
	if out.ID == "" || out.Count == 0 {
		t.Fatalf("bad encode response %+v", out)
	}

	// The frame render must produce real PNG bytes, not an empty 200.
	fresp, err := http.Get(srv.url(fmt.Sprintf("/frame/%s/0", out.ID)))
	if err != nil {
		t.Fatalf("get frame: %v", err)
	}
	defer fresp.Body.Close()
	if fresp.StatusCode != http.StatusOK {
		t.Fatalf("frame status %d", fresp.StatusCode)
	}
	pngBytes, _ := io.ReadAll(fresp.Body)
	if len(pngBytes) < 8 || string(pngBytes[1:4]) != "PNG" {
		t.Fatalf("not a PNG (%d bytes)", len(pngBytes))
	}

	// Out-of-range indices are rejected instead of indexing past the slice.
	oob, err := http.Get(srv.url(fmt.Sprintf("/frame/%s/99999", out.ID)))
	if err != nil {
		t.Fatalf("get oob frame: %v", err)
	}
	defer oob.Body.Close()
	if oob.StatusCode != http.StatusBadRequest {
		t.Fatalf("oob frame status %d", oob.StatusCode)
	}

	// Cancel releases the session, after which its frames 404.
	creq, err := http.NewRequest(http.MethodDelete, srv.url("/cancel/"+out.ID), nil)
	if err != nil {
		t.Fatalf("cancel request: %v", err)
	}
	cresp, err := http.DefaultClient.Do(creq)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status %d", cresp.StatusCode)
	}
	gone, err := http.Get(srv.url(fmt.Sprintf("/frame/%s/0", out.ID)))
	if err != nil {
		t.Fatalf("after cancel: %v", err)
	}
	defer gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("post-cancel frame status %d", gone.StatusCode)
	}

	if st := srv.Stats(); st.Transfers != 1 {
		t.Fatalf("transfers %d want 1", st.Transfers)
	}
	if ev := srv.Events(1); len(ev) != 1 || ev[0].Frames != out.Count {
		t.Fatalf("event mismatch: %+v want frames %d", ev, out.Count)
	}
}

func TestEncodeRejectsWrongMethodAndEmptyBody(t *testing.T) {
	srv := startTestServer(t)
	resp, err := http.Post(srv.url("/encode"), "application/octet-stream", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body status %d want 400", resp.StatusCode)
	}

	get, err := http.Get(srv.url("/md5"))
	if err != nil {
		t.Fatalf("get md5: %v", err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("get md5 status %d want 405", get.StatusCode)
	}
}

func TestCloseIsIdempotentAndStopsServing(t *testing.T) {
	srv := New()
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start: %v", err)
	}
	addr := srv.Addr()
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if srv.Addr() != "" {
		t.Fatalf("addr after close: %q", srv.Addr())
	}
	select {
	case <-srv.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done never closed after Close")
	}
	if _, err := http.Get("http://" + addr + "/"); err == nil {
		t.Fatal("server still accepting connections after Close")
	}
}

func TestStartTwiceIsNoop(t *testing.T) {
	srv := startTestServer(t)
	first := srv.Addr()
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if srv.Addr() != first {
		t.Fatalf("address changed on second start: %s -> %s", first, srv.Addr())
	}
}

func TestMaxUploadRejectsOversizeBody(t *testing.T) {
	srv := New()
	srv.SetMaxUpload(64)
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	resp, err := http.Post(srv.url("/md5"), "application/octet-stream", bytes.NewReader(make([]byte, 4096)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d want 413", resp.StatusCode)
	}
}

// TestColorModeClampsSplitToMatrixCapacity covers the bug where the UI's
// default split (2048) is larger than a color matrix frame can hold: every
// frame failed to render, the server answered 500 and the transfer produced
// nothing. The split has to be clamped to the matrix capacity, and the
// effective value echoed back so the UI can show it.
func TestColorModeClampsSplitToMatrixCapacity(t *testing.T) {
	srv := startTestServer(t)
	body := bytes.Repeat([]byte("qrt-pro color mode "), 300) // ~6 KB

	resp, err := http.Post(srv.url("/encode?mode=color&split=2048&codec=lt"),
		"application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("encode status %d: %s", resp.StatusCode, b)
	}

	var out struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
		Split int    `json:"split"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Split <= 0 || out.Split > qr.NumColorPayloadBytes()-colorFrameOverhead {
		t.Errorf("split = %d, want in (0, %d]", out.Split, qr.NumColorPayloadBytes()-colorFrameOverhead)
	}
	if out.Count == 0 {
		t.Fatal("no frames encoded")
	}

	// Every frame must render to a PNG; a nil PNG is what silently failed before.
	img, err := http.Get(srv.url("/frame/" + out.ID + "/0"))
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	defer img.Body.Close()
	if img.StatusCode != http.StatusOK {
		t.Fatalf("frame status %d", img.StatusCode)
	}
	png, _ := io.ReadAll(img.Body)
	if len(png) < 8 || !bytes.Equal(png[:4], []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("frame %d bytes, not a PNG", len(png))
	}
}

// TestClampColorSplit pins the arithmetic the color path relies on.
func TestClampColorSplit(t *testing.T) {
	cap := qr.NumColorPayloadBytes() - colorFrameOverhead
	cases := []struct{ in, want int }{
		{1, 1},
		{1024, 1024},
		{2048, cap},
		{cap, cap},
	}
	for _, c := range cases {
		if got := clampColorSplit(c.in); got != c.want {
			t.Errorf("clampColorSplit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
