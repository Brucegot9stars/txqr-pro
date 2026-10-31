package txqr

import (
	"strings"
	"testing"
)

func TestEstimatedFrameSizeCoversEncodedFrames(t *testing.T) {
	for _, codec := range []CodecType{CodecLT, CodecBinary, CodecRaptor, CodecRaptorQ, CodecOnline} {
		for _, size := range []int{1, 512, 513, 5000} {
			e := NewEncoder(512, codec)
			e.SetFileName(strings.Repeat("报告🧪", 100))
			estimate, err := e.MaxFrameSize(size)
			if err != nil {
				t.Fatal(codec, size, err)
			}
			frames, err := e.Encode(strings.Repeat("A", size))
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range frames {
				if len(frame) > estimate {
					t.Fatalf("%s size=%d: packet %d exceeds estimate %d", codec, size, len(frame), estimate)
				}
			}
		}
	}
}

func TestEstimatedFrameSizeRejectsUnsupportedTransfer(t *testing.T) {
	if _, err := NewEncoder(2048, CodecRaptorQ).MaxFrameSize(25_585_664); err == nil {
		t.Fatal("24.4 MiB exceeded the source-block bound without rejection")
	}
	if _, err := NewEncoder(512, "unknown").MaxFrameSize(1); err == nil {
		t.Fatal("unknown codec accepted by estimator")
	}
}
