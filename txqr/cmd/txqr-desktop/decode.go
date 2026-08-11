package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/divan/txqr"
	"github.com/divan/txqr/decimen"
	"github.com/divan/txqr/qr"
)

// decodeState holds the receive-side decoders and progress stats. All access
// goes through the mutex; the capture loop and the UI both touch it.
type decodeState struct {
	mu sync.Mutex

	decimenDec *decimen.LTDecoder
	decimenHdr decimen.Header
	txqrDec    *txqr.Decoder

	startTime time.Time
	lastTime  time.Time
	frames    int
	bytes     int64
	captured  int // QR decode attempts
	decodeFail int // attempts that yielded no payload
	completed bool
	result    []byte
	fileName  string
	mode      string // "decimen" or "txqr"
}

func (s *decodeState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decimenDec = nil
	s.txqrDec = nil
	s.startTime = time.Time{}
	s.lastTime = time.Time{}
	s.frames = 0
	s.bytes = 0
	s.completed = false
	s.result = nil
	s.fileName = ""
	s.mode = ""
}

// feed decodes one captured frame and routes it to the decimen or classic
// decoder. Returns true when the transfer just completed.
func (s *decodeState) feed(rgba *image.RGBA) bool {
	raw, err := qr.DecodeRaw(rgba)
	payload := qr.ExtractFramePayload(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.captured++
	if err != nil || payload == nil {
		s.decodeFail++
		return false
	}
	if s.completed {
		return true
	}
	if s.startTime.IsZero() {
		s.startTime = time.Now()
	}
	s.lastTime = time.Now()

	if len(payload) >= 2 && payload[0] == 0xD1 && payload[1] == 0x0D {
		return s.feedDecimenLocked(payload)
	}
	return s.feedTxqrLocked(payload)
}

func (s *decodeState) feedDecimenLocked(frame []byte) bool {
	f := decimen.ParseFrame(frame)
	if f == nil {
		return false
	}
	if s.decimenDec == nil || !sameHeader(s.decimenHdr, f.Header) {
		s.decimenHdr = f.Header
		s.decimenDec = decimen.NewDecoder(f.Header)
	}
	s.mode = "decimen"
	s.frames++
	s.bytes += int64(len(f.Payload))
	s.decimenDec.AddFrame(f.Header.Seq, f.Payload)
	if s.decimenDec.IsComplete() {
		container := s.decimenDec.Assemble()
		if file, err := decimen.UnpackContainer(container); err == nil {
			if shaMatches(container, file.Bytes) {
				s.completed = true
				s.result = file.Bytes
				s.fileName = file.Name
				return true
			}
		}
	}
	return false
}

func (s *decodeState) feedTxqrLocked(payload []byte) bool {
	if s.txqrDec == nil {
		s.txqrDec = txqr.NewDecoder()
	}
	s.mode = "txqr"
	s.frames++
	s.bytes += int64(len(payload))
	s.txqrDec.Decode(string(payload))
	if s.txqrDec.IsCompleted() {
		s.completed = true
		s.result = s.txqrDec.DataBytes()
		s.fileName = ""
		return true
	}
	return false
}

func sameHeader(a, b decimen.Header) bool {
	return a.SessionId == b.SessionId && a.K == b.K && a.BlockLen == b.BlockLen &&
		a.TotalLen == b.TotalLen && a.PayloadFnv == b.PayloadFnv
}

func shaMatches(container, file []byte) bool {
	// The DCF2 header carries the SHA-256 of the original file in bytes 17..49.
	if len(container) < 49 {
		return false
	}
	want := container[17:49]
	got := sha256.Sum256(file)
	return bytes.Equal(got[:], want)
}

// snapshot copies the stats the UI needs, under the mutex.
func (s *decodeState) snapshot() (progress int, status string, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed {
		return 100, "完成", true
	}
	p := 0
	if s.decimenDec != nil {
		p = s.decimenDec.SolvedCount() * 100 / int(s.decimenHdr.K)
		if p > 99 {
			p = 99
		}
	} else if s.txqrDec != nil {
		total := s.txqrDec.Total()
		if total > 0 {
			p = s.txqrDec.Read() * 100 / total
			if p > 99 {
				p = 99
			}
		}
	}
	status = fmtStatus(s.mode, s.frames, s.captured, s.decodeFail, s.bytes, s.startTime)
	return p, status, false
}

func fmtStatus(mode string, frames, captured, decodeFail int, bytes int64, start time.Time) string {
	elapsed := time.Since(start).Seconds()
	speed := "0 KB/s"
	if elapsed > 0.1 {
		speed = fmtBytes(int64(float64(bytes)/elapsed)) + "/s"
	}
	if mode == "decimen" {
		return fmt.Sprintf("捕获%d · 解码失败%d · 有效帧%d · 有效 %s · %s",
			captured, decodeFail, frames, fmtBytes(bytes), speed)
	}
	return fmt.Sprintf("捕获%d · 解码失败%d · 有效帧%d · %s · %s",
		captured, decodeFail, frames, fmtBytes(bytes), speed)
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
