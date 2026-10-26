package txqr

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"

	fountain "github.com/google/gofountain"
)

// Online code parameters. They are part of the wire format: the frame header
// carries only the tag "online", so a receiver must rebuild the codec from
// these constants alone (see gofountainCodec). Letting one end change them
// makes the two ends disagree — the decoder still reports completion but
// returns bytes that never match the source, which surfaces as a corrupted
// file reported as a successful transfer.
const (
	onlineEpsilon = 0.01
	onlineQuality = 3
	onlineSeed    = 42
)

// Encoder represents protocol encoder.
type Encoder struct {
	chunkLen         int
	redundancyFactor float64
	codec            CodecType
	fileName         string
}

// NewEncoder creates and inits a new encoder for the given chunk length and codec.
func NewEncoder(n int, codec CodecType) *Encoder {
	return &Encoder{
		chunkLen:         n,
		redundancyFactor: 2.0,
		codec:            codec,
	}
}

// SetFileName sets the original file name, transmitted to the receiver in a
// dedicated metadata frame so received files keep their original name.
// Empty value disables the metadata frame.
func (e *Encoder) SetFileName(name string) {
	e.fileName = name
}

// SetRedundancyFactor changes the value of redundancy factor.
func (e *Encoder) SetRedundancyFactor(rf float64) {
	e.redundancyFactor = rf
}

// SetOnlineParams is intentionally absent. epsilon, quality and the RNG seed
// are fixed by the wire format (see onlineEpsilon above): the frame header has
// no room for them, so a sender that changed them would emit frames no
// receiver can decode correctly.

// Encode encodes data and splits it into chunks to be
// further converted to QR code frames.
func (e *Encoder) Encode(str string) ([]string, error) {
	data := []byte(str)
	total := len(data)

	if total == 0 {
		return nil, fmt.Errorf("empty data")
	}

	var ret []string
	if e.fileName != "" {
		ret = append(ret, e.nameFrame(total))
	}

	// RaptorQ uses a separate encoding path
	if e.codec == CodecRaptorQ {
		frames, err := raptorQEncode(data, e.chunkLen, total)
		if err != nil {
			return nil, err
		}
		return append(ret, frames...), nil
	}

	// All gofountain-based codecs (LT, Binary, Raptor, Online)
	if total < e.chunkLen {
		return append(ret, e.frame(0, total, data, "")), nil
	}

	numChunks := numberOfChunks(total, e.chunkLen)

	// gofountain's Raptor (R10/RFC 5053) codec only supports K in
	// [4, 8192]. Messages with fewer than 4 source blocks can't use the
	// Raptor pre-code (it panics for K < 4), and RFC 5052 decoders -
	// including the Android receiver - also require K >= 4. Fall back to
	// plain LT for such tiny messages so the sender never emits invalid
	// Raptor frames. The fallback frame has no codec tag, so the receiver
	// initializes an LT decoder and decodes it transparently.
	effective := e.codec
	if e.codec == CodecRaptor && numChunks < 4 {
		effective = CodecLT
	}

	codec := e.createGofountainCodec(numChunks, effective)

	var msg = make([]byte, total)
	copy(msg, data)
	idsToEncode := ids(int(float64(numChunks) * e.redundancyFactor))
	lubyBlocks := fountain.EncodeLTBlocks(msg, idsToEncode, codec)

	// gofountain's Raptor (RFC 5053) decode path cannot reliably reconstruct
	// data whose length is not an exact multiple of the chunk size (it
	// returns completed=true but garbled bytes for most real-world sizes).
	// Verify the round trip locally and silently degrade to LT when Raptor
	// can't decode its own encoding, so the sender never emits frames that
	// the receiver would decode as corrupt data.
	if effective == CodecRaptor && !raptorRoundTrip(codec, data, lubyBlocks) {
		effective = CodecLT
		codec = e.createGofountainCodec(numChunks, CodecLT)
		msg = make([]byte, total)
		copy(msg, data)
		lubyBlocks = fountain.EncodeLTBlocks(msg, idsToEncode, codec)
	}

	for _, block := range lubyBlocks {
		ret = append(ret, e.frame(block.BlockCode, total, block.Data, codecName(effective)))
	}
	return ret, nil
}

// raptorRoundTrip verifies gofountain's Raptor codec can reproduce original
// data from the freshly encoded blocks. gofountain's Raptor decode path
// returns completed+true but garbled bytes for many real-world sizes (length
// not an exact multiple of the chunk size), so the sender falls back to LT
// when it can't round-trip. blocks are the freshly encoded Raptor blocks;
// they hold slices from EncodeLTBlocks' destructive input buffer, so the
// decoder gets defensive copies.
func raptorRoundTrip(codec fountain.Codec, data []byte, blocks []fountain.LTBlock) bool {
	d := codec.NewDecoder(len(data))
	cp := make([]fountain.LTBlock, len(blocks))
	for i, b := range blocks {
		buf := make([]byte, len(b.Data))
		copy(buf, b.Data)
		cp[i] = fountain.LTBlock{BlockCode: b.BlockCode, Data: buf}
	}
	if !d.AddBlocks(cp) {
		return false
	}
	out := d.Decode()
	return out != nil && bytes.Equal(out, data)
}

// nameFrame builds the metadata frame carrying the original file name.
// Format: -1/{chunkLen}/{total}/{codec}/name|{fileName}
func (e *Encoder) nameFrame(total int) string {
	name := strings.ReplaceAll(strings.ReplaceAll(e.fileName, "/", "_"), "|", "_")
	return fmt.Sprintf("-1/%d/%d/%s/name|%s", e.chunkLen, total, string(e.codec), name)
}

func (e *Encoder) createGofountainCodec(numChunks int, ct CodecType) fountain.Codec {
	switch ct {
	case CodecBinary:
		return fountain.NewBinaryCodec(numChunks)
	case CodecRaptor:
		return fountain.NewRaptorCodec(numChunks, 4)
	case CodecOnline:
		return fountain.NewOnlineCodec(numChunks, onlineEpsilon, onlineQuality, onlineSeed)
	default:
		return fountain.NewLubyCodec(numChunks,
			rand.New(fountain.NewMersenneTwister(200)),
			solitonDistribution(numChunks))
	}
}

// codecName returns the on-the-wire codec tag for a frame, or "" for the
// implicit LT default (which produces the shorter 3-part header).
func codecName(ct CodecType) string {
	if ct == CodecLT {
		return ""
	}
	return string(ct)
}

func (e *Encoder) frame(blockCode int64, total int, data []byte, codec string) string {
	if codec == "" {
		return fmt.Sprintf("%d/%d/%d|%s", blockCode, e.chunkLen, total, string(data))
	}
	return fmt.Sprintf("%d/%d/%d/%s|%s", blockCode, e.chunkLen, total, codec, string(data))
}

func numberOfChunks(length, chunkLen int) int {
	n := length / chunkLen
	if length%chunkLen > 0 {
		n++
	}
	return n
}
