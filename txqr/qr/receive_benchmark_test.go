package qr_test

import (
	"bytes"
	"image"
	"image/draw"
	"math/rand"
	"testing"

	"github.com/divan/txqr"
	"github.com/divan/txqr/qr"
)

// Includes QR recognition, packet validation, fountain recovery and final SHA.
// This is sequential CPU work, not an optical goodput or display benchmark.
func BenchmarkReceiveRaptorQ46KiB(b *testing.B) {
	benchmarkReceiveRaptorQ46KiB(b, false)
}

func BenchmarkReceiveAdaptiveRaptorQ46KiB(b *testing.B) {
	benchmarkReceiveRaptorQ46KiB(b, true)
}

func benchmarkReceiveRaptorQ46KiB(b *testing.B, adaptive bool) {
	data := make([]byte, 46*1024)
	rand.New(rand.NewSource(42)).Read(data)
	encoder := txqr.NewEncoder(2048, txqr.CodecRaptorQ)
	encoder.SetFileName("46KiB.bin")
	packets, err := encoder.Encode(string(data))
	if err != nil {
		b.Fatal(err)
	}
	maximum := 0
	for _, packet := range packets {
		if len(packet) > maximum {
			maximum = len(packet)
		}
	}
	modules := qr.ModulesForBytes(maximum)
	images := make([]*image.RGBA, 0, len(packets))
	for _, packet := range packets {
		img, err := qr.EncodeVersioned(packet, modules*4, qr.Medium, (modules-25)/4, -1)
		if err != nil {
			b.Fatal(err)
		}
		rgba := image.NewRGBA(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		images = append(images, rgba)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decoder := txqr.NewDecoder()
		preferred := ""
		for _, img := range images {
			raw, err, attempts := qr.DecodeRawMultiScalePreferred(img, []float64{1, .6, 1.7}, preferred, func(payload []byte) bool {
				_, err := txqr.ParseFrame(string(payload))
				return err == nil
			})
			if err != nil {
				b.Fatal(err)
			}
			if err := decoder.Decode(string(qr.ExtractFramePayload(raw))); err != nil {
				b.Fatal(err)
			}
			if adaptive {
				preferred = attempts[len(attempts)-1].Label
			}
			if decoder.IsCompleted() {
				break
			}
		}
		if !bytes.Equal(data, decoder.DataBytes()) {
			b.Fatal("recovered file differs from source")
		}
	}
}
