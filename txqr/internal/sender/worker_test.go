package sender

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/divan/txqr"
)

func TestIsolatedWorkerRoundTrip(t *testing.T) {
	for _, mode := range []string{"qr", "color", "decimen"} {
		body := bytes.Repeat([]byte("worker payload"), 50)
		s, err := encodeIsolated(context.Background(), workerRequest{Mode: mode, Codec: txqr.CodecRaptorQ, Split: 512,
			Name: "worker.bin", Size: len(body), MemoryBytes: workerMemoryBytes, CacheBytes: 8 << 20}, body, nil)
		if err != nil {
			t.Fatal(mode, err)
		}
		if renderFailed(s) || s.modules == 0 {
			t.Fatal("incomplete worker output")
		}
		if mode != "decimen" {
			d := txqr.NewDecoder()
			for _, f := range s.frames {
				if err := d.Decode(string(f)); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(d.DataBytes(), body) {
				t.Fatal("worker changed source")
			}
		}
	}
}

func TestCancelLiveWorkerKillsLibraryCall(t *testing.T) {
	body := make([]byte, 8192*32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := false
	start := time.Now()
	_, err := encodeIsolated(ctx, workerRequest{Mode: "qr", Codec: txqr.CodecBinary, Split: 32, Size: len(body),
		MemoryBytes: workerMemoryBytes, CacheBytes: 8 << 20}, body, func() { entered = true; cancel() })
	if !entered || !errors.Is(err, context.Canceled) {
		t.Fatal("live worker not canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("worker cancellation was not prompt")
	}
	// The next worker must start immediately, with no abandoned encode holding admission.
	TestIsolatedWorkerRoundTrip(t)
}

func TestWorkerOutputLengthValidatedBeforeAllocation(t *testing.T) {
	var out bytes.Buffer
	json.NewEncoder(&out).Encode(workerHeader{Count: 1, Total: 1, Split: 1, Modules: 29, PPM: 4})
	binary.Write(&out, binary.LittleEndian, uint32(1<<30))
	if _, err := readWorkerSession(bufio.NewReader(&out), 1<<20); err == nil {
		t.Fatal("unbounded worker allocation")
	}
}

func TestWorkerMemoryLimitDoesNotKillParent(t *testing.T) {
	// RaptorQ's intermediate matrix needs substantially more than 16 MiB at this
	// K. The worker must fail under its OS limit, and the parent must stay usable.
	body := make([]byte, 8192*8)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := encodeIsolated(ctx, workerRequest{Mode: "qr", Codec: txqr.CodecRaptorQ, Split: 8, Size: len(body),
		MemoryBytes: 16 << 20, CacheBytes: 8 << 20}, body, nil)
	if err == nil {
		t.Fatal("worker exceeded its data/commit memory limit")
	}
	if ctx.Err() != nil {
		t.Fatal("memory-limit test timed out")
	}
}
