package txqr

import (
	"fmt"
	"math/rand"
	"strings"

	fountain "github.com/google/gofountain"
)

// Encoder represents protocol encoder.
type Encoder struct {
	chunkLen         int
	redundancyFactor float64
	codec            CodecType
	onlineEpsilon    float64
	onlineQuality    int
	fileName         string
}

// NewEncoder creates and inits a new encoder for the given chunk length and codec.
func NewEncoder(n int, codec CodecType) *Encoder {
	return &Encoder{
		chunkLen:         n,
		redundancyFactor: 2.0,
		codec:            codec,
		onlineEpsilon:    0.01,
		onlineQuality:    3,
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

// SetOnlineParams sets the Online codec parameters.
func (e *Encoder) SetOnlineParams(epsilon float64, quality int) {
	e.onlineEpsilon = epsilon
	e.onlineQuality = quality
}

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
	codec := e.createGofountainCodec(numChunks)

	var msg = make([]byte, total)
	copy(msg, data)
	idsToEncode := ids(int(float64(numChunks) * e.redundancyFactor))
	lubyBlocks := fountain.EncodeLTBlocks(msg, idsToEncode, codec)

	for _, block := range lubyBlocks {
		ret = append(ret, e.frame(block.BlockCode, total, block.Data, string(e.codec)))
	}
	return ret, nil
}

// nameFrame builds the metadata frame carrying the original file name.
// Format: -1/{chunkLen}/{total}/{codec}/name|{fileName}
func (e *Encoder) nameFrame(total int) string {
	name := strings.ReplaceAll(strings.ReplaceAll(e.fileName, "/", "_"), "|", "_")
	return fmt.Sprintf("-1/%d/%d/%s/name|%s", e.chunkLen, total, string(e.codec), name)
}

func (e *Encoder) createGofountainCodec(numChunks int) fountain.Codec {
	switch e.codec {
	case CodecBinary:
		return fountain.NewBinaryCodec(numChunks)
	case CodecRaptor:
		return fountain.NewRaptorCodec(numChunks, 4)
	case CodecOnline:
		return fountain.NewOnlineCodec(numChunks, e.onlineEpsilon, e.onlineQuality, 42)
	default:
		return fountain.NewLubyCodec(numChunks,
			rand.New(fountain.NewMersenneTwister(200)),
			solitonDistribution(numChunks))
	}
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
