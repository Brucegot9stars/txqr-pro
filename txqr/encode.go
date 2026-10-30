package txqr

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	mathrand "math/rand"
	"strings"
	"unicode/utf8"

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
	sessionID        string
	digest           [sha256.Size]byte
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
	return e.EncodeContext(context.Background(), str)
}

// EncodeContext observes cancellation around library calls and while packing
// frames. A running gofountain library call itself cannot be interrupted.
func (e *Encoder) EncodeContext(ctx context.Context, str string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := []byte(str)
	total := len(data)

	if total == 0 {
		return nil, fmt.Errorf("empty data")
	}

	if e.chunkLen <= 0 || e.chunkLen > MaxChunkBytes || !validCodec(e.codec) ||
		math.IsNaN(e.redundancyFactor) || math.IsInf(e.redundancyFactor, 0) ||
		e.redundancyFactor < 1 || e.redundancyFactor > 4 {
		return nil, fmt.Errorf("invalid encoding parameters")
	}
	effective := e.codec
	numChunks := numberOfChunks(total, e.chunkLen)
	if effective == CodecRaptor && numChunks < 4 {
		effective = CodecLT
	}
	if err := validateParameters(total, e.chunkLen, effective); err != nil {
		return nil, err
	}
	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	e.sessionID = hex.EncodeToString(session[:])
	e.digest = sha256.Sum256(data)

	// RaptorQ uses a separate encoding path
	if e.codec == CodecRaptorQ {
		frames, err := e.raptorQEncode(ctx, data, total)
		if err != nil {
			return nil, err
		}
		return e.withNameFrame(frames, total, CodecRaptorQ), nil
	}

	// All gofountain-based codecs (LT, Binary, Raptor, Online)
	if total <= e.chunkLen {
		return e.withNameFrame([]string{e.frame(0, total, data, "")}, total, CodecLT), nil
	}

	// gofountain's Raptor (R10/RFC 5053) codec only supports K in
	// [4, 8192]. Messages with fewer than 4 source blocks can't use the
	// Raptor pre-code (it panics for K < 4), and RFC 5052 decoders -
	// including the Android receiver - also require K >= 4. Fall back to
	// plain LT for such tiny messages. The explicit LT tag makes receivers
	// rebuild the matching decoder.

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if effective == CodecRaptor && !raptorRoundTrip(codec, data, lubyBlocks) {
		effective = CodecLT
		codec = e.createGofountainCodec(numChunks, CodecLT)
		msg = make([]byte, total)
		copy(msg, data)
		lubyBlocks = fountain.EncodeLTBlocks(msg, idsToEncode, codec)
	}

	var ret []string
	for _, block := range lubyBlocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ret = append(ret, e.frame(block.BlockCode, total, block.Data, codecName(effective)))
	}
	return e.withNameFrame(ret, total, effective), nil
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
// Metadata shares the verified transfer header and CRC with data frames.
func (e *Encoder) nameFrame(total int, ct CodecType) string {
	name := strings.ReplaceAll(strings.ReplaceAll(e.fileName, "/", "_"), "|", "_")
	runes := []rune(name)
	if len(runes) > 120 {
		name = string(runes[:120])
	}
	for len(name) > MaxFileNameBytes {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	return (&Frame{Session: e.sessionID, SHA256: e.digest, BlockCode: -1, ChunkLen: e.chunkLen,
		Total: total, Codec: ct, Name: true, Payload: []byte(name)}).pack()
}

func (e *Encoder) withNameFrame(frames []string, total int, ct CodecType) []string {
	if e.fileName == "" {
		return frames
	}
	return append([]string{e.nameFrame(total, ct)}, frames...)
}

// MaxFrameSize estimates the longest packet without running a fountain solver.
// It uses the same framing and filename handling as Encode. Raptor may later
// fall back to LT, whose shorter codec name cannot make the estimate too small.
func (e *Encoder) MaxFrameSize(total int) (int, error) {
	if !validCodec(e.codec) {
		return 0, fmt.Errorf("invalid codec")
	}
	ct := e.codec
	k := 0
	if e.chunkLen > 0 {
		k = numberOfChunks(total, e.chunkLen)
	}
	if ct != CodecRaptorQ && (k == 1 || (ct == CodecRaptor && k < 4)) {
		ct = CodecLT
	}
	if err := validateParameters(total, e.chunkLen, ct); err != nil {
		return 0, err
	}
	if math.IsNaN(e.redundancyFactor) || math.IsInf(e.redundancyFactor, 0) || e.redundancyFactor < 1 || e.redundancyFactor > 4 {
		return 0, fmt.Errorf("invalid encoding redundancy")
	}
	count := int(float64(k) * e.redundancyFactor)
	if ct == CodecRaptorQ {
		count = k + int(float64(k)*1.05)
	} else if k == 1 {
		count = 1
	}
	size := e.chunkLen
	if ct != CodecRaptorQ {
		size = numberOfChunks(total, k)
	}
	copy := *e
	copy.sessionID = strings.Repeat("0", 32)
	longest := len(copy.frame(int64(count-1), total, make([]byte, size), codecName(ct)))
	if e.fileName != "" {
		if n := len(copy.nameFrame(total, ct)); n > longest {
			longest = n
		}
	}
	return longest, nil
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
			mathrand.New(fountain.NewMersenneTwister(200)),
			solitonDistribution(numChunks))
	}
}

// codecName preserves the internal empty-string LT identifier; packing writes lt.
func codecName(ct CodecType) string {
	if ct == CodecLT {
		return ""
	}
	return string(ct)
}

func (e *Encoder) frame(blockCode int64, total int, data []byte, codec string) string {
	// gofountain keeps trailing zero padding virtual. Put it on the wire so
	// all nonempty symbols have the declared semi-equal partition size.
	if codec != string(CodecRaptorQ) && len(data) > 0 {
		size := numberOfChunks(total, numberOfChunks(total, e.chunkLen))
		if len(data) < size {
			padded := make([]byte, size)
			copy(padded, data)
			data = padded
		}
	}
	return (&Frame{Session: e.sessionID, SHA256: e.digest, BlockCode: blockCode,
		ChunkLen: e.chunkLen, Total: total, Codec: ParseCodec(codec), Payload: data}).pack()
}

func numberOfChunks(length, chunkLen int) int {
	n := length / chunkLen
	if length%chunkLen > 0 {
		n++
	}
	return n
}
