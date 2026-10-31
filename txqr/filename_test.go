package txqr

import (
	"errors"
	"strings"
	"testing"
)

func TestThreeNamePacketsAndLateMetadata(t *testing.T) {
	for _, ct := range []CodecType{CodecLT, CodecBinary, CodecRaptor, CodecRaptorQ, CodecOnline} {
		for _, n := range []int{1, 4096} {
			e := NewEncoder(256, ct)
			e.SetFileName("报告 2026.zip")
			data := strings.Repeat("x", n)
			frames, err := e.Encode(data)
			if err != nil {
				t.Fatal(err)
			}
			d := NewDecoder()
			var names []string
			previous := int64(-1)
			for _, packet := range frames {
				f, err := ParseFrame(packet)
				if err != nil {
					t.Fatal(err)
				}
				if f.Name {
					names = append(names, packet)
					continue
				}
				if f.BlockCode <= previous {
					t.Fatal("data ordering changed")
				}
				previous = f.BlockCode
				if err := d.Decode(packet); err != nil {
					t.Fatal(ct, n, err)
				}
			}
			if len(names) != 3 || names[0] != names[1] || names[0] != names[2] {
				t.Fatal("expected three identical name packets")
			}
			if frames[0] != names[0] || frames[1] == names[0] {
				t.Fatal("name repeats clumped at start")
			}
			if !d.IsCompleted() || d.Data() != data || d.FileName() != "" {
				t.Fatal(ct, n, "nameless content failed")
			}
			read := d.Read()
			// First two metadata packets were lost; the third arrives after decoding.
			if err := d.Decode(names[2]); err != nil {
				t.Fatal(err)
			}
			if d.FileName() != "报告 2026.zip" || d.Read() != read || d.Data() != data {
				t.Fatal("late name changed content/accounting")
			}
			other := NewEncoder(256, ct)
			other.SetFileName("foreign.bin")
			foreign, err := other.Encode(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Decode(foreign[0]); !errors.Is(err, ErrSessionMismatch) {
				t.Fatal("foreign late name accepted")
			}
			bad := []byte(names[2])
			bad[len(bad)-1] ^= 1
			if d.Decode(string(bad)) == nil {
				t.Fatal("CRC-invalid late name accepted")
			}
			f, _ := ParseFrame(names[0])
			f.Payload = []byte("replacement.bin")
			if err := d.Decode(f.pack()); err != nil {
				t.Fatal(err)
			}
			if d.FileName() != "报告 2026.zip" {
				t.Fatal("later name overwrote the first valid name")
			}
		}
	}
}
