package main

// goldengen writes Go-encoded frames for every codec so the Kotlin receiver
// can be verified against them (cross-language golden vectors).
//
// Usage: go run ./cmd/goldengen <out-dir> [payload-size]
//   go run ./cmd/goldengen ../txqr-reader/android/app/src/test/resources/codecmatrix
// Keep the output committed: CodecMatrixCrossTest reads it.

import (
	"encoding/base64"
	"fmt"
	fountain "github.com/google/gofountain"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/divan/txqr"
	"github.com/divan/txqr/qr"
)

type caseOut struct {
	Codec  string
	Frames []string
}

func main() {
	if len(os.Args) < 2 {
		panic("usage: goldengen <output-dir> [payload-size]")
	}
	chunk := 512
	n := 8 << 10 // 8192 = 16 chunks exactly (K=16 >= 4, keeps raptor alive)
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &n)
	}
	r := rand.New(rand.NewSource(12345))
	data := make([]byte, n)
	r.Read(data)

	var cases []caseOut
	for _, ct := range []txqr.CodecType{txqr.CodecLT, txqr.CodecBinary, txqr.CodecRaptor, txqr.CodecRaptorQ, txqr.CodecOnline} {
		e := txqr.NewEncoder(chunk, ct)
		frames, err := e.Encode(string(data))
		if err != nil {
			panic(err)
		}
		c := caseOut{
			Codec: string(ct),
		}
		if c.Codec == "" {
			c.Codec = "lt"
		}
		for _, f := range frames {
			c.Frames = append(c.Frames, base64.StdEncoding.EncodeToString([]byte(f)))
		}
		cases = append(cases, c)
		fmt.Printf("generated %-8s frames=%d\n", c.Codec, len(frames))
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(dir+"/expected.txt",
		[]byte(base64.StdEncoding.EncodeToString(data)+"\n"), 0o644); err != nil {
		panic(err)
	}
	for _, c := range cases {
		var sb []byte
		for _, f := range c.Frames {
			sb = append(sb, []byte(f+"\n")...)
		}
		if err := os.WriteFile(dir+"/"+c.Codec+".txt", sb, 0o644); err != nil {
			panic(err)
		}
	}
	fmt.Println("wrote", dir)
	regressions(filepath.Join(dir, "..", "regression"))
}

func regressions(dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		panic(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
			panic(err)
		}
	}
	fixture := func(name string, data []byte, split int, ct txqr.CodecType, ids []int) {
		f, err := txqr.NewEncoder(split, ct).Encode(string(data))
		if err != nil {
			panic(err)
		}
		out := base64.StdEncoding.EncodeToString(data) + "\n"
		for _, id := range ids {
			out += base64.StdEncoding.EncodeToString([]byte(f[id])) + "\n"
		}
		write(name, []byte(out))
	}
	data := make([]byte, 1536*8)
	for i := range data {
		data[i] = byte(i * 37)
	}
	ids := []int{2748}
	for i := 0; i < 1600; i++ {
		ids = append(ids, i)
	}
	fixture("online-large.txt", data, 8, txqr.CodecOnline, ids)
	fixture("rq-rank.txt", data[:64], 8, txqr.CodecRaptorQ, []int{10, 5, 13, 3, 11, 1, 15, 9, 8})
	fixture("single.txt", data[:8], 8, txqr.CodecBinary, []int{0})
	fixture("nonmultiple.txt", data[:1111], 100, txqr.CodecLT, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23})
	var picks string
	for _, k := range []int{1536, 8192} {
		c := fountain.NewOnlineCodec(k, 0.01, 3, 42)
		for _, id := range []int64{0, 1009, 2748, 3391, 9999, 16777215} {
			indices := c.PickIndices(id)
			h := uint32(2166136261)
			for _, v := range indices {
				h ^= uint32(v)
				h *= 16777619
			}
			picks += fmt.Sprintf("%d %d %d %d\n", k, id, len(indices), h)
		}
	}
	write("online-indices.txt", []byte(picks))
	f, err := txqr.NewEncoder(512, txqr.CodecLT).Encode("ABC")
	if err != nil {
		panic(err)
	}
	write("color-payload.bin", []byte(f[0]))
	// One RGBA pixel per module keeps Android JVM tests independent of
	// desktop-only javax.imageio/ java.awt APIs.
	small, err := qr.ColorMatrixEncode([]byte(f[0]), qr.ColorMatrixSize)
	if err != nil {
		panic(err)
	}
	rgba := make([]byte, 0, qr.ColorMatrixSize*qr.ColorMatrixSize*4)
	for y := 0; y < qr.ColorMatrixSize; y++ {
		for x := 0; x < qr.ColorMatrixSize; x++ {
			red, green, blue, alpha := small.At(x, y).RGBA()
			rgba = append(rgba, byte(red>>8), byte(green>>8), byte(blue>>8), byte(alpha>>8))
		}
	}
	write("color.rgba", rgba)

	img, err := qr.ColorMatrixEncode([]byte(f[0]), qr.ColorMatrixSize*4)
	if err != nil {
		panic(err)
	}
	out, err := os.Create(filepath.Join(dir, "color.png"))
	if err != nil {
		panic(err)
	}
	if err := png.Encode(out, img); err != nil {
		panic(err)
	}
	if err := out.Close(); err != nil {
		panic(err)
	}
}
