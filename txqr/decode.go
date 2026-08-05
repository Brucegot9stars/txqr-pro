package txqr

import (
	"fmt"
	"strings"

	fountain "github.com/google/gofountain"
	raptorq "github.com/xssnick/raptorq"
)

// Decoder represents protocol decode.
type Decoder struct {
	chunkLen  int
	codec     fountain.Codec
	fd        fountain.Decoder
	completed bool
	total     int
	cache     map[string]struct{}

	// RaptorQ state
	codecType   CodecType
	raptorQ     *raptorq.RaptorQ
	raptorQDec  *raptorq.Decoder
}

// NewDecoder creates and inits a new decoder.
func NewDecoder() *Decoder {
	return &Decoder{
		cache: make(map[string]struct{}),
	}
}

// NewDecoderSize creates and inits a new decoder for the known size.
func NewDecoderSize(size, chunkLen int, ct CodecType) *Decoder {
	d := &Decoder{
		total:     size,
		chunkLen:  chunkLen,
		cache:     make(map[string]struct{}),
		codecType: ct,
	}

	if ct == CodecRaptorQ {
		rq := raptorq.NewRaptorQ(uint32(chunkLen))
		dec, err := rq.CreateDecoder(uint32(size))
		if err != nil {
			return nil
		}
		d.raptorQ = rq
		d.raptorQDec = dec
	} else {
		numChunks := numberOfChunks(size, chunkLen)
		d.codec = gofountainCodec(numChunks, ct)
		d.fd = d.codec.NewDecoder(size)
	}
	return d
}

// Decode takes a single chunk of data and decodes it.
func (d *Decoder) Decode(chunk string) error {
	idx := strings.IndexByte(chunk, '|')
	if idx == -1 {
		return fmt.Errorf("invalid frame: \"%s\"", chunk)
	}

	header := chunk[:idx]
	if d.isCached(header) {
		return nil
	}

	payload := chunk[idx+1:]

	// Parse header: blockCode/chunkLen/total[/codec]
	parts := strings.Split(header, "/")
	if len(parts) < 3 {
		return fmt.Errorf("invalid header: %s", header)
	}

	// Metadata frame (original file name): -1/{chunkLen}/{total}/{codec}/name|{name}
	if len(parts) >= 5 && parts[0] == "-1" && parts[4] == "name" {
		return nil
	}

	var blockCode int64
	var chunkLen, total int
	if _, err := fmt.Sscanf(parts[0], "%d", &blockCode); err != nil {
		return fmt.Errorf("invalid blockCode: %v", err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &chunkLen); err != nil {
		return fmt.Errorf("invalid chunkLen: %v", err)
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &total); err != nil {
		return fmt.Errorf("invalid total: %v", err)
	}

	// Detect codec from 4th field, default to LT for backward compatibility
	ct := CodecLT
	if len(parts) >= 4 {
		ct = ParseCodec(parts[3])
	}

	// Lazy initialization
	if d.fd == nil && d.raptorQDec == nil {
		d.total = total
		d.chunkLen = chunkLen
		d.codecType = ct

		if ct == CodecRaptorQ {
			rq := raptorq.NewRaptorQ(uint32(chunkLen))
			dec, err := rq.CreateDecoder(uint32(total))
			if err != nil {
				return fmt.Errorf("raptorq create decoder: %w", err)
			}
			d.raptorQ = rq
			d.raptorQDec = dec
		} else {
			numChunks := numberOfChunks(total, chunkLen)
			d.codec = gofountainCodec(numChunks, ct)
			d.fd = d.codec.NewDecoder(total)
		}
	}

	// Add block to decoder
	if ct == CodecRaptorQ {
		ready, err := d.raptorQDec.AddSymbol(uint32(blockCode), []byte(payload))
		if err != nil {
			return fmt.Errorf("raptorq add symbol: %w", err)
		}
		if ready {
			d.completed = true
		}
	} else {
		lubyBlock := fountain.LTBlock{
			BlockCode: blockCode,
			Data:      []byte(payload),
		}
		d.completed = d.fd.AddBlocks([]fountain.LTBlock{lubyBlock})
	}

	return nil
}

// Validate checks if a given chunk of data is a valid txqr protocol packet.
func (d *Decoder) Validate(chunk string) error {
	if chunk == "" || len(chunk) < 4 {
		return fmt.Errorf("invalid frame: \"%s\"", chunk)
	}

	idx := strings.IndexByte(chunk, '|')
	if idx == -1 {
		return fmt.Errorf("invalid frame: \"%s\"", chunk)
	}

	return nil
}

// Data returns decoded data.
func (d *Decoder) Data() string {
	return string(d.DataBytes())
}

// DataBytes returns decoded data as a byte slice.
func (d *Decoder) DataBytes() []byte {
	if d.raptorQDec != nil {
		if !d.completed {
			return []byte{}
		}
		ok, data, err := d.raptorQDec.Decode()
		if err != nil || !ok {
			return []byte{}
		}
		d.completed = true
		return data
	}

	if d.fd == nil {
		return []byte{}
	}
	if !d.completed {
		return []byte{}
	}
	return d.fd.Decode()
}

// Length returns length of the decoded data.
func (d *Decoder) Length() int {
	return 0
}

// Read returns amount of currently read bytes.
func (d *Decoder) Read() int {
	return 0
}

// Total returns total amount of data.
func (d *Decoder) Total() int {
	return d.total
}

// IsCompleted reports whether the read was completed successfully or not.
func (d *Decoder) IsCompleted() bool {
	return d.completed
}

// Reset resets decoder, preparing it for the next run.
func (d *Decoder) Reset() {
	d.fd = nil
	d.completed = false
	d.chunkLen = 0
	d.total = 0
	d.cache = map[string]struct{}{}
	d.codec = nil
	d.codecType = CodecLT
	d.raptorQ = nil
	d.raptorQDec = nil
}

func (d *Decoder) isCached(header string) bool {
	if _, ok := d.cache[header]; ok {
		return true
	}
	d.cache[header] = struct{}{}
	return false
}
