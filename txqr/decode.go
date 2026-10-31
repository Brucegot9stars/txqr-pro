package txqr

import (
	"crypto/sha256"
	"fmt"

	fountain "github.com/google/gofountain"
	raptorq "github.com/xssnick/raptorq"
)

// Decoder owns one verified transfer. It is not safe for concurrent use.
type Decoder struct {
	chunkLen   int
	codecType  CodecType
	codec      fountain.Codec
	fd         fountain.Decoder
	raptorQDec *raptorq.Decoder
	total      int
	manifest   *Frame
	seen       map[int64]struct{}
	result     []byte
	fileName   string
	read       int
	failed     error
	completed  bool
}

func NewDecoder() *Decoder { return &Decoder{seen: make(map[int64]struct{})} }

func NewDecoderSize(size, chunkLen int, ct CodecType) *Decoder {
	if validateParameters(size, chunkLen, ct) != nil {
		return nil
	}
	d := NewDecoder()
	d.total, d.chunkLen, d.codecType = size, chunkLen, ct
	return d
}

// Decode rejects corruption and foreign sessions before touching the equations.
// RaptorQ's AddSymbol boolean means "try Decode", not "file is complete".
func (d *Decoder) Decode(packet string) error {
	f, err := ParseFrame(packet)
	if err != nil {
		return err
	}
	if d.manifest != nil && !d.manifest.SameTransfer(f) {
		return ErrSessionMismatch
	}
	if d.total != 0 && (d.total != f.Total || d.chunkLen != f.ChunkLen || d.codecType != f.Codec) {
		return ErrSessionMismatch
	}
	if d.failed != nil {
		return d.failed
	}
	if d.manifest == nil {
		d.manifest = f
		d.total, d.chunkLen, d.codecType = f.Total, f.ChunkLen, f.Codec
	}
	if f.Name {
		if d.fileName == "" {
			d.fileName = string(f.Payload)
		}
		return nil
	}
	if d.completed {
		return nil
	}
	if _, ok := d.seen[f.BlockCode]; ok {
		return nil
	}
	if d.fd == nil && d.raptorQDec == nil {
		if d.codecType == CodecRaptorQ {
			d.raptorQDec, err = raptorq.NewRaptorQ(uint32(d.chunkLen)).CreateDecoder(uint32(d.total))
			if err != nil {
				return fmt.Errorf("create raptorq decoder: %w", err)
			}
		} else {
			d.codec = gofountainCodec(numberOfChunks(d.total, d.chunkLen), d.codecType)
			d.fd = d.codec.NewDecoder(d.total)
		}
	}
	ready := false
	if d.raptorQDec != nil {
		ready, err = d.raptorQDec.AddSymbol(uint32(f.BlockCode), f.Payload)
		if err != nil {
			return fmt.Errorf("add raptorq symbol: %w", err)
		}
	} else {
		ready = d.fd.AddBlocks([]fountain.LTBlock{{BlockCode: f.BlockCode, Data: f.Payload}})
	}
	d.seen[f.BlockCode] = struct{}{}
	d.read += len(f.Payload)
	if !ready {
		return nil
	}
	var result []byte
	if d.raptorQDec != nil {
		var ok bool
		ok, result, err = d.raptorQDec.Decode()
		if err != nil {
			return fmt.Errorf("decode raptorq: %w", err)
		}
		if !ok {
			return nil
		}
	} else {
		result = d.fd.Decode()
	}
	if len(result) != d.total || sha256.Sum256(result) != d.manifest.SHA256 {
		d.failed = ErrIntegrity
		return d.failed
	}
	d.result = result
	d.completed = true
	return nil
}

func (d *Decoder) Validate(packet string) error { _, err := ParseFrame(packet); return err }
func (d *Decoder) Data() string                 { return string(d.DataBytes()) }
func (d *Decoder) DataBytes() []byte            { return append([]byte(nil), d.result...) }
func (d *Decoder) Length() int                  { return len(d.result) }

// Read is received unique encoded payload bytes, not source-file goodput.
func (d *Decoder) Read() int         { return d.read }
func (d *Decoder) Total() int        { return d.total }
func (d *Decoder) FileName() string  { return d.fileName }
func (d *Decoder) IsCompleted() bool { return d.completed }
func (d *Decoder) Reset()            { *d = *NewDecoder() }
