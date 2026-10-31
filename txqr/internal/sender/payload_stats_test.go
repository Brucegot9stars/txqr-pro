package sender

import (
	"bytes"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/divan/txqr"
)

func TestFrameResponseCountsOpticalPayloadNotPNG(t *testing.T) {
	for _, mode := range []string{"qr", "color", "decimen"} {
		t.Run(mode, func(t *testing.T) {
			var sess *session
			var err error
			if mode == "decimen" {
				sess, err = encodeDecimen(httptest.NewRequest("POST", "/encode?name=test.bin", nil), bytes.Repeat([]byte{42}, 1000), frameSize{ppm: 2})
			} else {
				sess, err = encodeClassic(bytes.Repeat([]byte{42}, 1000), 128, mode, txqr.CodecLT, frameSize{ppm: 2}, "test.bin")
			}
			if err != nil {
				t.Fatal(err)
			}
			// A different PNG size must not affect the optical data accounting.
			for i := range sess.pngs {
				sess.pngs[i] = bytes.Repeat([]byte{1}, 77+i)
			}
			srv := New()
			t.Cleanup(func() { _ = srv.Close() })
			srv.sessions["stats"] = sess
			for i := 0; i < 2; i++ {
				rec := httptest.NewRecorder()
				srv.handleFrame(rec, httptest.NewRequest("GET", "/frame/stats/"+strconv.Itoa(i), nil))
				want := 125 // 1000 bytes partitioned into eight equal source symbols.
				if i == 0 {
					want = 0 // The classic filename packet is metadata.
				}
				if mode == "decimen" {
					want = DecimenBlockLen
				}
				if rec.Code != 200 || rec.Header().Get("X-QRT-Payload-Bytes") != strconv.Itoa(want) {
					t.Fatalf("frame %d: status %d, payload header %q; want %d", i, rec.Code, rec.Header().Get("X-QRT-Payload-Bytes"), want)
				}
			}
		})
	}
	if _, ok := framePayloadBytes([]byte("invalid"), "qr"); ok {
		t.Fatal("invalid packet supplied a byte count")
	}
}
