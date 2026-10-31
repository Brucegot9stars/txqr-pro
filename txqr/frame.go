package txqr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// ProtocolVersion deliberately separates verified transfers from the old,
// unverified text format. Both sender and receiver must be updated together.
const ProtocolVersion = "QRT2"

const (
	MaxTransferBytes = 64 << 20
	MaxSourceBlocks  = 8192
	MaxChunkBytes    = 2933
	MaxBlockCode     = (1 << 24) - 1
	// Covers the longest supported header, including the session, SHA and CRC.
	MaxFrameOverhead = 160
	MaxFileNameBytes = 360
)

var (
	ErrSessionMismatch = errors.New("frame belongs to a different transfer")
	ErrIntegrity       = errors.New("recovered file failed SHA-256 verification")
)

// Frame contains a fully validated wire packet. CRC covers every header field
// before the CRC itself, the separator, and the binary payload.
// QRT2/session/sha256/code/chunk/total/codec/{data|name}/crc32|payload
type Frame struct {
	Session   string
	SHA256    [sha256.Size]byte
	BlockCode int64
	ChunkLen  int
	Total     int
	Codec     CodecType
	Name      bool
	Payload   []byte
}

func validCodec(ct CodecType) bool {
	switch ct {
	case CodecLT, CodecBinary, CodecRaptor, CodecRaptorQ, CodecOnline:
		return true
	}
	return false
}

func validateParameters(total, chunk int, ct CodecType) error {
	if total <= 0 || total > MaxTransferBytes || chunk <= 0 || chunk > MaxChunkBytes || !validCodec(ct) {
		return errors.New("unsupported transfer size, chunk size or codec")
	}
	k := numberOfChunks(total, chunk)
	if k > MaxSourceBlocks || (ct == CodecRaptor && k < 4) {
		return fmt.Errorf("源块数 %d 不受支持，当前最多 %d 个；分片 %d 字节时文件上限 %.2f MiB（%d 字节）", k, MaxSourceBlocks, chunk, float64(min(MaxTransferBytes, MaxSourceBlocks*chunk))/(1<<20), min(MaxTransferBytes, MaxSourceBlocks*chunk))
	}
	return nil
}

func wireCodec(ct CodecType) string {
	if ct == CodecLT {
		return "lt"
	}
	return string(ct)
}

func frameCRC(prefix string, payload []byte) uint32 {
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(prefix))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write(payload)
	return h.Sum32()
}

func (f *Frame) pack() string {
	kind := "data"
	if f.Name {
		kind = "name"
	}
	prefix := fmt.Sprintf("%s/%s/%x/%d/%d/%d/%s/%s", ProtocolVersion,
		f.Session, f.SHA256, f.BlockCode, f.ChunkLen, f.Total, wireCodec(f.Codec), kind)
	return fmt.Sprintf("%s/%08x|%s", prefix, frameCRC(prefix, f.Payload), f.Payload)
}

// ParseFrame checks bounds and checksums before a caller allocates a decoder or
// updates its deduplication cache. It rejects legacy and unknown versions.
func ParseFrame(packet string) (*Frame, error) {
	if len(packet) > MaxFrameOverhead+1+MaxChunkBytes {
		return nil, errors.New("frame too large")
	}
	sep := strings.IndexByte(packet, '|')
	if sep < 0 || sep > MaxFrameOverhead {
		return nil, errors.New("invalid frame header")
	}
	parts := strings.Split(packet[:sep], "/")
	if len(parts) != 9 || parts[0] != ProtocolVersion {
		return nil, errors.New("unsupported frame version; update both clients")
	}
	session, err := hex.DecodeString(parts[1])
	if err != nil || len(session) != 16 || parts[1] != strings.ToLower(parts[1]) {
		return nil, errors.New("invalid session id")
	}
	digest, err := hex.DecodeString(parts[2])
	if err != nil || len(digest) != sha256.Size || parts[2] != strings.ToLower(parts[2]) {
		return nil, errors.New("invalid file digest")
	}
	code, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return nil, errors.New("invalid block code")
	}
	chunk, err := strconv.Atoi(parts[4])
	if err != nil {
		return nil, errors.New("invalid chunk length")
	}
	total, err := strconv.Atoi(parts[5])
	if err != nil {
		return nil, errors.New("invalid total length")
	}
	ct := ParseCodec(parts[6])
	if wireCodec(ct) != parts[6] {
		return nil, errors.New("unknown codec")
	}
	if err := validateParameters(total, chunk, ct); err != nil {
		return nil, err
	}
	name := parts[7] == "name"
	if (!name && parts[7] != "data") || (name && code != -1) || (!name && (code < 0 || code > MaxBlockCode)) ||
		(ct == CodecRaptor && code > 65535) {
		return nil, errors.New("invalid frame kind or block code")
	}
	if len(packet)-sep-1 > MaxChunkBytes {
		return nil, errors.New("frame payload too large")
	}
	payload := []byte(packet[sep+1:])
	if name {
		if len(payload) == 0 || len(payload) > MaxFileNameBytes {
			return nil, errors.New("invalid file name length")
		}
	} else {
		size := chunk
		if ct != CodecRaptorQ {
			size = numberOfChunks(total, numberOfChunks(total, chunk))
		}
		// A binary code block with no selected sources carries no information.
		if len(payload) != size && !(ct == CodecBinary && len(payload) == 0) {
			return nil, errors.New("incorrect symbol length")
		}
	}
	crc, err := strconv.ParseUint(parts[8], 16, 32)
	last := strings.LastIndexByte(packet[:sep], '/')
	if err != nil || len(parts[8]) != 8 || uint32(crc) != frameCRC(packet[:last], payload) {
		return nil, errors.New("frame checksum mismatch")
	}
	f := &Frame{Session: parts[1], BlockCode: code, ChunkLen: chunk, Total: total, Codec: ct, Name: name, Payload: payload}
	copy(f.SHA256[:], digest)
	return f, nil
}

func (f *Frame) SameTransfer(other *Frame) bool {
	return f != nil && other != nil && f.Session == other.Session && f.SHA256 == other.SHA256 &&
		f.ChunkLen == other.ChunkLen && f.Total == other.Total && f.Codec == other.Codec
}
