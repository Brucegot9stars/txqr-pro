package txqr

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// codecCase is one scheme under test.
type codecCase struct {
	name string
	ct   CodecType
}

var codecCases = []codecCase{
	{"LT(经典)", CodecLT},
	{"Binary", CodecBinary},
	{"Raptor(RFC5053)", CodecRaptor},
	{"RaptorQ(RFC6330)", CodecRaptorQ},
	{"Online", CodecOnline},
}

func matrixPayload(n int) []byte {
	r := rand.New(rand.NewSource(int64(n)))
	b := make([]byte, n)
	r.Read(b)
	return b
}

// roundTripResult is what one encode→decode run measured.
type roundTripResult struct {
	frames     int // frames the encoder emitted
	needed     int // frames consumed before the decoder reported completion
	ok         bool
	encodeTime time.Duration
	decodeTime time.Duration
}

func runRoundTrip(ct CodecType, data []byte, chunk int, drop float64, seed int64) roundTripResult {
	enc := NewEncoder(chunk, ct)
	enc.SetRedundancyFactor(2.0)

	t0 := time.Now()
	frames, err := enc.Encode(string(data))
	res := roundTripResult{encodeTime: time.Since(t0)}
	if err != nil {
		return res
	}
	res.frames = len(frames)

	// Optional erasure: drop frames the "camera never saw".
	order := frames
	if drop > 0 {
		r := rand.New(rand.NewSource(seed))
		kept := make([]string, 0, len(frames))
		for _, f := range frames {
			if r.Float64() >= drop {
				kept = append(kept, f)
			}
		}
		order = kept
	}

	d := NewDecoder()
	t1 := time.Now()
	for i, f := range order {
		if err := d.Decode(f); err != nil {
			res.decodeTime = time.Since(t1)
			return res
		}
		res.needed = i + 1
		if d.IsCompleted() {
			break
		}
	}
	res.decodeTime = time.Since(t1)
	res.ok = d.IsCompleted() && bytes.Equal(d.DataBytes(), data)
	return res
}

// TestCodecMatrixRoundTrip is the "is this scheme actually usable" gate: every
// scheme in the sender's dropdown must survive a full encode→decode cycle at
// several sizes, both intact and with a third of the frames lost.
func TestCodecMatrixRoundTrip(t *testing.T) {
	const chunk = 2048
	sizes := []int{1024, 16 << 10, 128 << 10}

	fmt.Printf("\n%s  chunk=%d  redundancy=2.0\n",
		pad("方案", 18), chunk)
	fmt.Printf("%s %s %s %s %s %s\n",
		pad("数据量", 10), pad("帧数", 7), pad("解出帧", 8), pad("开销", 8), pad("正确性", 8), pad("耗时", 12))

	for _, c := range codecCases {
		for _, n := range sizes {
			data := matrixPayload(n)
			k := (n + chunk - 1) / chunk
			res := runRoundTrip(c.ct, data, chunk, 0, int64(n))
			if !res.ok {
				t.Errorf("%s %d B: round trip failed (frames=%d needed=%d)",
					c.name, n, res.frames, res.needed)
			}
			overhead := float64(res.needed) / float64(k)
			fmt.Printf("%s %s %s %s %s %s\n",
				pad(c.name, 18),
				pad(fmt.Sprintf("%d B", n), 10),
				pad(fmt.Sprintf("%d", res.frames), 7),
				pad(fmt.Sprintf("%d/%d", res.needed, k), 8),
				pad(fmt.Sprintf("%.3fx", overhead), 8),
				pad(fmt.Sprintf("%v", res.encodeTime+res.decodeTime), 12))
		}
	}
}

// TestCodecMatrixErasure feeds only ~70% of the frames, mimicking a camera
// that misses frames. A scheme that needs far more than K frames here is a
// scheme that stalls in practice.
func TestCodecMatrixErasure(t *testing.T) {
	const chunk = 2048
	const n = 64 << 10
	k := (n + chunk - 1) / chunk

	fmt.Printf("\n丢帧 30%%（%d B, K=%d）\n", n, k)
	fmt.Printf("%s %s %s %s\n", pad("方案", 18), pad("解出帧/K", 12), pad("开销", 8), pad("正确性", 8))

	for _, c := range codecCases {
		data := matrixPayload(n)
		res := runRoundTrip(c.ct, data, chunk, 0.30, 99)
		// With 30% loss the best case is K/(1-0.30) = 1.43x frames shown.
		overhead := float64(res.needed) / float64(k)
		fmt.Printf("%s %s %s %s\n",
			pad(c.name, 18),
			pad(fmt.Sprintf("%d/%d", res.needed, k), 12),
			pad(fmt.Sprintf("%.3fx", overhead), 8),
			pad(boolMark(res.ok), 8))
		if !res.ok {
			t.Errorf("%s: failed with 30%% erasure (needed %d frames of %d available)",
				c.name, res.needed, res.frames)
		}
	}
}

// TestCodecCarouselEfficiency models the real link: the sender loops its frame
// list forever and each displayed frame reaches the receiver independently with
// probability p. Frames displayed per successful transfer is the only number
// that turns into wall-clock time — at a fixed fps it *is* the transfer time.
func TestCodecCarouselEfficiency(t *testing.T) {
	const chunk = 2048
	const n = 64 << 10
	const trials = 10
	k := (n + chunk - 1) / chunk
	data := matrixPayload(n)

	fmt.Printf("\n循环播放 + 每帧独立接收概率（%d B, K=%d, %d 次试验，上限 %d 帧）\n",
		n, k, trials, 20*k)
	fmt.Printf("%s %s %s %s\n", pad("方案", 18), pad("p=0.9", 12), pad("p=0.6", 12), pad("p=0.35", 12))

	for _, c := range codecCases {
		row := pad(c.name, 18)
		for _, p := range []float64{0.9, 0.6, 0.35} {
			total, ok := 0, 0
			worst := 0
			for tr := 0; tr < trials; tr++ {
				shown, good := runCarousel(c.ct, data, chunk, p, int64(tr+1), 20*k)
				if good {
					ok++
					total += shown
					if shown > worst {
						worst = shown
					}
				}
			}
			cell := "全部超时"
			if ok > 0 {
				cell = fmt.Sprintf("%.0f 帧(%.2fx)", float64(total)/float64(ok),
					float64(total)/float64(ok)/float64(k))
			}
			row += pad(cell, 12)
		}
		fmt.Println(row)
	}
}

// runCarousel replays the frame list cyclically; each pass over a frame is
// delivered with probability p. Returns frames displayed and whether it
// completed within maxFrames.
func runCarousel(ct CodecType, data []byte, chunk int, p float64, seed int64, maxFrames int) (int, bool) {
	enc := NewEncoder(chunk, ct)
	enc.SetRedundancyFactor(2.0)
	frames, err := enc.Encode(string(data))
	if err != nil {
		return 0, false
	}
	d := NewDecoder()
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < maxFrames; i++ {
		if r.Float64() < p {
			if err := d.Decode(frames[i%len(frames)]); err != nil {
				return i + 1, false
			}
			if d.IsCompleted() {
				if !bytes.Equal(d.DataBytes(), data) {
					return i + 1, false
				}
				return i + 1, true
			}
		}
	}
	return maxFrames, false
}

// TestOnlineParamsAreFixedByWireFormat guards the hazard the Online codec
// used to have: the frame header carries only the tag "online", so epsilon,
// quality and the RNG seed must be identical constants on both ends. While the
// sender could change them, the receiver still reported completion but handed
// back bytes that never matched the source — a corrupt file reported as a
// successful transfer.
func TestOnlineParamsAreFixedByWireFormat(t *testing.T) {
	const chunk = 2048
	for _, n := range []int{8 << 10, 32 << 10, 100 << 10} {
		data := matrixPayload(n)
		res := runRoundTrip(CodecOnline, data, chunk, 0, int64(n))
		if !res.ok {
			t.Errorf("online %d B: round trip failed (frames=%d needed=%d)", n, res.frames, res.needed)
		}
	}

	// The encoder-side and decoder-side codec factories must agree exactly.
	// The decoder rebuilds the codec from the header alone, so any divergence
	// here is silent corruption, not an error.
	const numChunks = 16
	encCodec := (&Encoder{}).createGofountainCodec(numChunks, CodecOnline)
	decCodec := gofountainCodec(numChunks, CodecOnline)
	for i := int64(0); i < 32; i++ {
		a, b := encCodec.PickIndices(i), decCodec.PickIndices(i)
		if len(a) != len(b) {
			t.Fatalf("block %d: encoder picked %d indices, decoder %d", i, len(a), len(b))
		}
		for j := range a {
			if a[j] != b[j] {
				t.Fatalf("block %d: index %d encoder=%d decoder=%d", i, j, a[j], b[j])
			}
		}
	}
}

// TestRaptorQFallsBackNowhere records that RaptorQ has no size fallback: it
// either encodes or errors, unlike gofountain's Raptor which silently degrades
// to LT. Callers rely on this when reasoning about frame counts.
func TestRaptorQFrameCount(t *testing.T) {
	const chunk = 2048
	for _, n := range []int{1024, 8 << 10, 64 << 10} {
		data := matrixPayload(n)
		k := (n + chunk - 1) / chunk
		enc := NewEncoder(chunk, CodecRaptorQ)
		frames, err := enc.Encode(string(data))
		if err != nil {
			t.Fatalf("raptorq %d B: %v", n, err)
		}
		want := k + int(float64(k)*1.05)
		if len(frames) != want {
			t.Errorf("raptorq %d B: got %d frames, want %d (K=%d)", n, len(frames), want, k)
		}
		fmt.Printf("raptorq %d B → K=%d 帧数=%d (固定 2.05K，忽略 redundancyFactor)\n", n, k, len(frames))
	}
}

func pad(s string, n int) string {
	// CJK glyphs are double width in a terminal; count runes as 2 when wide.
	w := 0
	for _, r := range s {
		if r > 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	if w >= n {
		return s
	}
	return s + string(bytes.Repeat([]byte(" "), n-w))
}

func boolMark(b bool) string {
	if b {
		return "OK"
	}
	return "FAIL"
}
