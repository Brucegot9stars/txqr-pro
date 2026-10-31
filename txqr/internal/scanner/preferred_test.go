package scanner

import (
	"bytes"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/qr"
)

func TestPreferredRecognitionRecoversAndResets(t *testing.T) {
	data := bytes.Repeat([]byte{0, 1, 2, 3, 0xff, 0x80, 4}, 700)
	frames, err := txqr.NewEncoder(2048, txqr.CodecRaptorQ).Encode(string(data))
	if err != nil {
		t.Fatal(err)
	}
	r := &region{serial: 1, scanning: true, st: &decodeState{}}
	m := &Manager{}
	for i, frame := range frames {
		size := 724
		if i%2 == 1 {
			size = 600 // resized capture must return to a complete search
		}
		img, err := qr.Encode(frame, size, qr.Medium)
		if err != nil {
			t.Fatal(err)
		}
		rgba := toRGBA(img)
		payload, hint := r.st.decode(rgba)
		if !bytes.Equal(payload, []byte(frame)) || hint == "" {
			t.Fatalf("frame %d: recognition lost wire bytes", i)
		}
		_, done := m.commitPayload(decJob{r: r, gen: 1, rgba: rgba, qrHint: hint}, payload)
		if r.st.qrHint != hint || r.st.qrHintSize != rgba.Bounds().Size() {
			t.Fatal("current scan did not learn its successful geometry")
		}
		if done {
			break
		}
	}
	m.finishPendingName(r, 1, time.Now().Add(fileNameWait))
	got, _, ok := r.st.completedResult()
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("preferred scan did not recover the original file")
	}
	r.st.reset()
	if r.st.qrHint != "" || r.st.qrHintSize != (image.Point{}) {
		t.Fatal("new transfer retained an old hint")
	}
}

func TestStaleRecognitionCannotPublishHint(t *testing.T) {
	r := &region{serial: 2, scanning: true, st: &decodeState{}}
	m := &Manager{}
	_, done := m.commitPayload(decJob{r: r, gen: 1, rgba: image.NewRGBA(image.Rect(0, 0, 32, 32)), qrHint: "bare"}, nil)
	if done || r.st.qrHint != "" || r.st.qrHintSize != (image.Point{}) {
		t.Fatal("stale work taught a restarted scan its hint")
	}
}

func TestInvalidQRDoesNotLearnHint(t *testing.T) {
	packets, err := txqr.NewEncoder(2048, txqr.CodecRaptorQ).Encode(strings.Repeat("A", 4096))
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte(packets[0])
	bad[len(bad)-1] ^= 1
	img, err := qr.Encode(string(bad), 724, qr.Medium)
	if err != nil {
		t.Fatal(err)
	}
	var st decodeState
	payload, hint := st.decode(toRGBA(img))
	if payload != nil || hint != "" || st.qrHint != "" || st.decodeFail != 1 {
		t.Fatal("CRC-invalid QR accepted or learned")
	}
}

func TestConcurrentRecognitionAndReset(t *testing.T) {
	packets, err := txqr.NewEncoder(64, txqr.CodecLT).Encode("concurrent hint state")
	if err != nil {
		t.Fatal(err)
	}
	img, err := qr.Encode(packets[0], 256, qr.Medium)
	if err != nil {
		t.Fatal(err)
	}
	rgba := toRGBA(img)
	var st decodeState
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				payload, _ := st.decode(rgba)
				if !bytes.Equal(payload, []byte(packets[0])) {
					t.Error("concurrent QR recognition changed payload")
				}
			}
		}()
	}
	for i := 0; i < 10; i++ {
		st.reset()
	}
	wg.Wait()
}
