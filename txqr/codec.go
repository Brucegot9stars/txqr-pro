package txqr

import (
	"fmt"
	"math/rand"

	fountain "github.com/google/gofountain"
	raptorq "github.com/xssnick/raptorq"
)

// CodecType identifies the fountain code scheme used for encoding/decoding.
type CodecType string

const (
	// CodecLT is the default Luby Transform (LT) code, the original scheme.
	CodecLT CodecType = ""
	// CodecBinary is the Random Binary Fountain code.
	CodecBinary CodecType = "binary"
	// CodecRaptor is the R10 Raptor code per RFC 5053.
	CodecRaptor CodecType = "raptor"
	// CodecRaptorQ is the RaptorQ code per RFC 6330.
	CodecRaptorQ CodecType = "raptorq"
	// CodecOnline is the Online Codes by Maymounkov & Mazieres.
	CodecOnline CodecType = "online"
)

// ParseCodec converts a string to a CodecType. Unknown strings default to CodecLT.
func ParseCodec(s string) CodecType {
	switch CodecType(s) {
	case CodecBinary:
		return CodecBinary
	case CodecRaptor:
		return CodecRaptor
	case CodecRaptorQ:
		return CodecRaptorQ
	case CodecOnline:
		return CodecOnline
	default:
		return CodecLT
	}
}

// gofountainCodec creates a gofountain.Codec for the given codec type.
// Used by both Encoder and Decoder for gofountain-based schemes.
func gofountainCodec(numChunks int, ct CodecType) fountain.Codec {
	switch ct {
	case CodecBinary:
		return fountain.NewBinaryCodec(numChunks)
	case CodecRaptor:
		return fountain.NewRaptorCodec(numChunks, 4)
	case CodecOnline:
		return fountain.NewOnlineCodec(numChunks, 0.01, 3, 42)
	default:
		return fountain.NewLubyCodec(numChunks,
			rand.New(fountain.NewMersenneTwister(200)),
			solitonDistribution(numChunks))
	}
}

// isGofountainCodec returns true if the codec type uses the gofountain library.
func isGofountainCodec(ct CodecType) bool {
	return ct != CodecRaptorQ
}

// raptorQEncode encodes data using RaptorQ and returns frames.
// symbolSize is the fixed size of each RaptorQ symbol (typically = split).
func raptorQEncode(data []byte, split int, total int) ([]string, error) {
	rq := raptorq.NewRaptorQ(uint32(split))
	enc, err := rq.CreateEncoder(data)
	if err != nil {
		return nil, fmt.Errorf("raptorq create encoder: %w", err)
	}

	K := int(enc.BaseSymbolsNum())
	// Generate systematic symbols (IDs 0..K-1) + repair symbols
	// Total frames = K * redundancyFactor (repair symbols start at ID K)
	numRepair := int(float64(K) * 1.05) // ~5% overhead for RaptorQ
	frames := make([]string, 0, K+numRepair)

	// Systematic symbols
	for i := 0; i < K; i++ {
		sym := enc.GenSymbol(uint32(i))
		frames = append(frames, raptorQFrame(i, split, total, sym))
	}
	// Repair symbols
	for i := 0; i < numRepair; i++ {
		sym := enc.GenSymbol(uint32(K + i))
		frames = append(frames, raptorQFrame(K+i, split, total, sym))
	}
	return frames, nil
}

func raptorQFrame(blockCode int, chunkLen, total int, data []byte) string {
	return fmt.Sprintf("%d/%d/%d/raptorq|%s", blockCode, chunkLen, total, string(data))
}
