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
	"math/rand"
	"os"

	"github.com/divan/txqr"
)

type caseOut struct {
	Codec  string
	Frames []string
}

func main() {
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
}
