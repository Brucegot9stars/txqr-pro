package decimen

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Decimen optical file transfer — systematic-carousel fountain in standard
// QR frames. Wire format mirrors decimen-optical-transfer (see
// docs/protocol-analysis.md): senders and receivers derive every frame's block
// subset independently and never compare notes, so frameSeed(), splitmix32()
// and repairIndices() are wire format and must stay byte-identical across
// implementations (Go sender here, Kotlin receiver on Android).

// MAGIC bytes for the 20-byte frame header (little-endian).
const (
	Magic0       = 0xD1
	Magic1       = 0x0D // v2: systematic-carousel fountain
	HeaderLen    = 20
	MaxFileBytes = 64 * 1024 * 1024
)

// Header is the 20-byte little-endian frame header.
// Layout:
//
//	0  u8   magic 0xD1
//	1  u8   magic 0x0D
//	2  u16  sessionId
//	4  u32  seq
//	8  u16  k
//	10 u16  blockLen
//	12 u32  totalLen
//	16 u32  payloadFnv
type Header struct {
	SessionId  uint16
	Seq        uint32
	K          uint16
	BlockLen   uint16
	TotalLen   uint32
	PayloadFnv uint32
}

// PackHeader writes the 20-byte little-endian header.
func PackHeader(h Header) []byte {
	b := make([]byte, HeaderLen)
	b[0] = Magic0
	b[1] = Magic1
	binary.LittleEndian.PutUint16(b[2:], h.SessionId)
	binary.LittleEndian.PutUint32(b[4:], h.Seq)
	binary.LittleEndian.PutUint16(b[8:], h.K)
	binary.LittleEndian.PutUint16(b[10:], h.BlockLen)
	binary.LittleEndian.PutUint32(b[12:], h.TotalLen)
	binary.LittleEndian.PutUint32(b[16:], h.PayloadFnv)
	return b
}

// ParseHeader parses a 20-byte header and reports whether it is
// self-consistent (beyond the caller checking magic).
func ParseHeader(b []byte) (Header, bool) {
	if len(b) < HeaderLen {
		return Header{}, false
	}
	h := Header{
		SessionId:  binary.LittleEndian.Uint16(b[2:]),
		Seq:        binary.LittleEndian.Uint32(b[4:]),
		K:          binary.LittleEndian.Uint16(b[8:]),
		BlockLen:   binary.LittleEndian.Uint16(b[10:]),
		TotalLen:   binary.LittleEndian.Uint32(b[12:]),
		PayloadFnv: binary.LittleEndian.Uint32(b[16:]),
	}
	if h.K == 0 || h.BlockLen == 0 || h.TotalLen == 0 {
		return Header{}, false
	}
	return h, true
}

// FrameHeader is the header for a full wire frame (header + payload).
type Frame struct {
	Header  Header
	Payload []byte
}

// PackFrame returns header || payload. payload length must equal BlockLen.
func PackFrame(h Header, payload []byte) []byte {
	out := make([]byte, HeaderLen+len(payload))
	copy(out, PackHeader(h))
	copy(out[HeaderLen:], payload)
	return out
}

// ParseFrame parses a full wire frame. Returns nil for anything not a valid
// self-consistent Decimen frame.
func ParseFrame(b []byte) *Frame {
	if len(b) <= HeaderLen {
		return nil
	}
	if b[0] != Magic0 || b[1] != Magic1 {
		return nil
	}
	h, ok := ParseHeader(b[:HeaderLen])
	if !ok {
		return nil
	}
	if len(b) != HeaderLen+int(h.BlockLen) {
		return nil
	}
	return &Frame{Header: h, Payload: b[HeaderLen:]}
}

// StreamIdentity covers every header field that must hold constant for a
// decoder to keep accepting frames. seq is deliberately absent.
func StreamIdentity(h Header) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d",
		h.SessionId, h.K, h.BlockLen, h.TotalLen, h.PayloadFnv)
}

// Fnv1a computes the 32-bit FNV-1a hash.
func Fnv1a(b []byte) uint32 {
	h := uint32(0x811c9dc5)
	for _, c := range b {
		h ^= uint32(c)
		h *= 0x01000193
	}
	return h
}

// splitmix32 is the deterministic PRNG shared with the receiver (integer ops
// only, so it cannot diverge across engines or languages).
func splitmix32(seed uint32) func() uint32 {
	s := seed
	return func() uint32 {
		s += 0x9e3779b9
		t := s ^ (s >> 16)
		t *= 0x21f0aaad
		t ^= t >> 15
		t *= 0x735a2d97
		t ^= t >> 15
		return t
	}
}

// frameSeed mixes session and seq into a stream-wide per-frame seed.
func frameSeed(sessionId uint16, seq uint32) uint32 {
	h := (uint32(sessionId)+1)*0x9e3779b1 ^ (seq + 0x85ebca6b)
	h = (h ^ (h >> 13)) * 0xc2b2ae35
	return h ^ (h >> 16)
}

const (
	repairDegreeMin = 4
	repairDegreeMax = 24
)

// repairIndices draws a deterministic uniform mid-degree subset of blocks,
// preserving insertion order (JS Set semantics — iteration order is part of
// the wire format because it defines the XOR order).
func repairIndices(k, sessionId uint16, seq uint32) []uint16 {
	rnd := splitmix32(frameSeed(sessionId, seq))
	d := int(k)
	if ld := repairDegreeMin + int(rnd()%uint32(repairDegreeMax-repairDegreeMin+1)); ld < d {
		d = ld
	}
	idx := make([]uint16, 0, d)
	seen := make(map[uint16]struct{}, d)
	for len(seen) < d {
		b := uint16(rnd() % uint32(k))
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		idx = append(idx, b)
	}
	return idx
}

// CycleLength is frames per carousel cycle: one systematic sweep of all k
// blocks, then k repair frames.
func CycleLength(k uint16) int {
	return 2 * int(k)
}

// FrameComposition is the block subset XORed into frame seq — identical on
// both ends. Systematic during the sweep, mid-degree repair after.
func FrameComposition(k, sessionId uint16, seq uint32) []uint16 {
	pos := seq % uint32(CycleLength(k))
	if pos < uint32(k) {
		return []uint16{uint16(pos)}
	}
	return repairIndices(k, sessionId, seq)
}

// LTEncoder is the systematic-carousel fountain encoder.
type LTEncoder struct {
	K         uint16
	BlockLen  int
	SessionId uint16
	words     int
	blocks    []uint32
	payload   []byte
}

// NewEncoder builds an encoder for payload split into BlockLen-byte blocks.
func NewEncoder(payload []byte, blockLen int, sessionId uint16) *LTEncoder {
	k := (len(payload) + blockLen - 1) / blockLen
	if k < 1 {
		k = 1
	}
	words := (blockLen + 3) / 4
	blocks := make([]uint32, k*words)
	buf := make([]byte, k*words*4)
	// The reference packs each blockLen-byte source block into a words*4-byte
	// slot (zero-padded). blockLen is not a multiple of 4, so slot stride !=
	// blockLen and a plain contiguous copy would misalign every block after
	// the first. This re-pack is part of the wire format.
	for b := 0; b < k; b++ {
		src := payload[b*blockLen:]
		if len(src) > blockLen {
			src = src[:blockLen]
		}
		copy(buf[b*words*4:], src)
	}
	for i := 0; i < len(blocks); i++ {
		blocks[i] = binary.LittleEndian.Uint32(buf[i*4:])
	}
	return &LTEncoder{
		K:         uint16(k),
		BlockLen:  blockLen,
		SessionId: sessionId,
		words:     words,
		blocks:    blocks,
		payload:   payload,
	}
}

// SourcePayload returns the original payload bytes.
func (e *LTEncoder) SourcePayload() []byte { return e.payload }

// Encode produces the frame payload (BlockLen bytes) for a seq.
func (e *LTEncoder) Encode(seq uint32) []byte {
	idx := FrameComposition(e.K, e.SessionId, seq)
	out := make([]uint32, e.words)
	for _, b := range idx {
		off := int(b) * e.words
		for w := 0; w < e.words; w++ {
			out[w] ^= e.blocks[off+w]
		}
	}
	buf := make([]byte, e.words*4)
	for i, v := range out {
		binary.LittleEndian.PutUint32(buf[i*4:], v)
	}
	return buf[:e.BlockLen]
}

// PendingFrame is a decoded frame waiting on an unsolved block.
type PendingFrame struct {
	idx   []uint16
	words []uint32
}

// LTDecoder is the systematic-carousel fountain decoder (peeling cascade).
type LTDecoder struct {
	K         int
	BlockLen  int
	SessionId uint16
	TotalLen  int
	words     int
	solved    [][]uint32 // nil until the block is solved
	byBlock   map[uint16]map[*PendingFrame]struct{}
	seen      map[uint32]struct{}
	solvedN   int
	FramesNew int
	FramesDup int
	// Frames with a NEW seq that carried no new information.
	FramesRedundant int
}

// NewDecoder inits a decoder from the wire header.
func NewDecoder(h Header) *LTDecoder {
	return &LTDecoder{
		K:         int(h.K),
		BlockLen:  int(h.BlockLen),
		SessionId: h.SessionId,
		TotalLen:  int(h.TotalLen),
		words:     (int(h.BlockLen) + 3) / 4,
		solved:    make([][]uint32, int(h.K)),
		byBlock:   make(map[uint16]map[*PendingFrame]struct{}),
		seen:      make(map[uint32]struct{}),
	}
}

// IsComplete reports whether every source block is solved.
func (d *LTDecoder) IsComplete() bool { return d.solvedN >= d.K }

// SolvedCount reports how many source blocks are solved (drives progress UI).
func (d *LTDecoder) SolvedCount() int { return d.solvedN }

func (d *LTDecoder) xorInto(dst, src []uint32) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

// AddFrame feeds one decoded frame payload. Returns true when the transfer
// just completed.
func (d *LTDecoder) AddFrame(seq uint32, block []byte) bool {
	if _, dup := d.seen[seq]; dup {
		d.FramesDup++
		return false
	}
	d.seen[seq] = struct{}{}
	d.FramesNew++
	if d.IsComplete() {
		return false
	}

	idx := FrameComposition(uint16(d.K), d.SessionId, seq)
	words := make([]uint32, d.words)
	buf := make([]byte, d.words*4)
	copy(buf, block[:min(len(block), len(buf))])
	for i := range words {
		words[i] = binary.LittleEndian.Uint32(buf[i*4:])
	}

	// Reduce with already-solved blocks.
	rem := make([]uint16, 0, len(idx))
	for _, b := range idx {
		if s := d.solved[b]; s != nil {
			d.xorInto(words, s)
		} else {
			rem = append(rem, b)
		}
	}
	if len(rem) == 0 {
		d.FramesRedundant++
		return false
	}
	if len(rem) == 1 {
		d.resolve(rem[0], words)
		return d.IsComplete()
	}
	pf := &PendingFrame{idx: rem, words: words}
	for _, b := range rem {
		set := d.byBlock[b]
		if set == nil {
			set = make(map[*PendingFrame]struct{})
			d.byBlock[b] = set
		}
		set[pf] = struct{}{}
	}
	return false
}

func (d *LTDecoder) resolve(b uint16, w []uint32) {
	type item struct {
		b uint16
		w []uint32
	}
	queue := []item{{b, w}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		b, w := it.b, it.w
		if d.solved[b] != nil {
			continue
		}
		d.solved[b] = w
		d.solvedN++
		if d.solvedN > d.K {
			d.solvedN = d.K
			return
		}
		waiting := d.byBlock[b]
		if waiting == nil {
			continue
		}
		delete(d.byBlock, b)
		for pf := range waiting {
			d.xorInto(pf.words, w)
			kept := pf.idx[:0]
			for _, cb := range pf.idx {
				if cb != b {
					kept = append(kept, cb)
				}
			}
			pf.idx = kept
			if len(pf.idx) == 1 {
				r := pf.idx[0]
				if set := d.byBlock[r]; set != nil {
					delete(set, pf)
				}
				if d.solved[r] == nil {
					queue = append(queue, item{r, pf.words})
				}
			}
		}
	}
}

// Assemble returns the recovered payload when complete, else nil.
func (d *LTDecoder) Assemble() []byte {
	if !d.IsComplete() {
		return nil
	}
	out := make([]byte, d.TotalLen)
	for b := 0; b < d.K; b++ {
		start := b * d.BlockLen
		length := d.BlockLen
		if start+length > d.TotalLen {
			length = d.TotalLen - start
		}
		if length <= 0 {
			continue
		}
		words := d.solved[b]
		for i := 0; i < length; i++ {
			w := i / 4
			shift := uint((i % 4) * 8)
			out[start+i] = byte((words[w] >> shift) & 0xFF)
		}
	}
	return out
}

// --- DCF2 file container --------------------------------------------------

// Container magic: "DCF2".
var containerMagic = []byte("DCF2")

// precompressedTypes mirrors the reference's PRECOMPRESSED_TYPES set.
var precompressedTypes = map[string]bool{
	"application/gzip":                 true,
	"application/java-archive":         true,
	"application/vnd.rar":              true,
	"application/x-7z-compressed":      true,
	"application/x-brotli":             true,
	"application/x-bzip":               true,
	"application/x-bzip2":              true,
	"application/x-gzip":               true,
	"application/x-lzma":               true,
	"application/x-rar-compressed":     true,
	"application/x-xz":                 true,
	"application/x-zip-compressed":     true,
	"application/zip":                  true,
	"application/zstd":                 true,
}

// IsPrecompressedType reports whether a media type is already entropy-coded
// and gzip would be wasted.
func IsPrecompressedType(mediaType string) bool {
	m := strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	if strings.HasPrefix(m, "video/") {
		return true
	}
	if strings.HasPrefix(m, "image/") {
		switch m {
		case "image/bmp", "image/x-ms-bmp", "image/svg+xml", "image/tiff",
			"image/x-icon", "image/vnd.microsoft.icon":
			return false
		}
		return true
	}
	if strings.HasPrefix(m, "audio/") {
		switch m {
		case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave",
			"audio/aiff", "audio/x-aiff", "audio/basic", "audio/l16":
			return false
		}
		return true
	}
	if strings.HasPrefix(m, "application/vnd.openxmlformats-officedocument.") {
		return true
	}
	if strings.HasPrefix(m, "application/vnd.oasis.opendocument.") {
		return true
	}
	if strings.HasSuffix(m, "+zip") {
		return true
	}
	return precompressedTypes[m]
}

// PackContainer wraps original file bytes (name, type, mime) into a DCF2
// container, gzipping when it helps. Returns the container bytes and whether
// gzip was used.
func PackContainer(name, mediaType string, data []byte) ([]byte, bool, error) {
	name = SanitizeFileName(name)
	if name == "" {
		name = "transfer.bin"
	}
	nameBytes := []byte(name)
	typeBytes := []byte(mediaType)
	if len(nameBytes) > 0xffff || len(typeBytes) > 0xffff {
		return nil, false, fmt.Errorf("decimen: name or type too long")
	}
	if len(data) > MaxFileBytes {
		return nil, false, fmt.Errorf("decimen: file exceeds %d bytes", MaxFileBytes)
	}

	transmitted := data
	useGzip := false
	if len(data) >= 768 && !IsPrecompressedType(mediaType) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			return nil, false, err
		}
		if err := zw.Close(); err != nil {
			return nil, false, err
		}
		if buf.Len()+64 < len(data) {
			transmitted = buf.Bytes()
			useGzip = true
		}
	}

	sum := sha256.Sum256(data)
	out := make([]byte, 49+len(nameBytes)+len(typeBytes)+len(transmitted))
	copy(out, containerMagic)
	if useGzip {
		out[4] = 1
	}
	binary.LittleEndian.PutUint16(out[5:], uint16(len(nameBytes)))
	binary.LittleEndian.PutUint16(out[7:], uint16(len(typeBytes)))
	binary.LittleEndian.PutUint32(out[9:], uint32(len(data)))
	binary.LittleEndian.PutUint32(out[13:], uint32(len(transmitted)))
	copy(out[17:], sum[:])
	off := 49
	copy(out[off:], nameBytes)
	off += len(nameBytes)
	copy(out[off:], typeBytes)
	off += len(typeBytes)
	copy(out[off:], transmitted)
	return out, useGzip, nil
}

// Container is the unpacked file payload.
type Container struct {
	Name    string
	Type    string
	Bytes   []byte
	SHA256  [32]byte
	FileLen uint32
}

// UnpackContainer parses and reconstitutes a DCF2 container.
func UnpackContainer(container []byte) (*Container, error) {
	if len(container) < 49 {
		return nil, fmt.Errorf("decimen: container too short")
	}
	for i, m := range containerMagic {
		if container[i] != m {
			return nil, fmt.Errorf("decimen: bad container magic")
		}
	}
	comp := container[4]
	if comp > 1 {
		return nil, fmt.Errorf("decimen: unsupported compression")
	}
	nameLen := int(binary.LittleEndian.Uint16(container[5:]))
	typeLen := int(binary.LittleEndian.Uint16(container[7:]))
	fileLen := binary.LittleEndian.Uint32(container[9:])
	transLen := binary.LittleEndian.Uint32(container[13:])
	dataOffset := 49 + nameLen + typeLen
	if fileLen == 0 || fileLen > MaxFileBytes || transLen == 0 || transLen > MaxFileBytes ||
		dataOffset+int(transLen) != len(container) {
		return nil, fmt.Errorf("decimen: length mismatch")
	}
	name := SanitizeFileName(string(container[49 : 49+nameLen]))
	if name == "" {
		name = "transfer.bin"
	}
	mediaType := string(container[49+nameLen : dataOffset])
	transmitted := container[dataOffset:]

	var bytesData []byte
	if comp == 1 {
		if len(transmitted) < 18 {
			return nil, fmt.Errorf("decimen: gzip payload too short")
		}
		if got := binary.LittleEndian.Uint32(transmitted[len(transmitted)-4:]); got != fileLen {
			return nil, fmt.Errorf("decimen: gzip length mismatch")
		}
		zr, err := gzip.NewReader(bytes.NewReader(transmitted))
		if err != nil {
			return nil, err
		}
		bytesData, err = io.ReadAll(io.LimitReader(zr, int64(fileLen)+1))
		zr.Close()
		if err != nil {
			return nil, err
		}
		if len(bytesData) != int(fileLen) {
			return nil, fmt.Errorf("decimen: inflate length mismatch")
		}
	} else {
		if len(transmitted) != int(fileLen) {
			return nil, fmt.Errorf("decimen: file length mismatch")
		}
		bytesData = transmitted
	}

	var sha [32]byte
	copy(sha[:], container[17:49])
	return &Container{Name: name, Type: mediaType, Bytes: bytesData, SHA256: sha, FileLen: fileLen}, nil
}

// SanitizeFileName reduces a name to a bare basename and strips controls.
func SanitizeFileName(name string) string {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	var sb strings.Builder
	for _, r := range base {
		if r < 0x20 || r == 0x7f {
			continue
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	switch out {
	case "", ".", "..":
		return "transfer.bin"
	}
	return out
}