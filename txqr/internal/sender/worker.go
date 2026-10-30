package sender

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/divan/txqr"
)

const workerArg = "--qrt-private-encode-worker-v1"
const workerMemoryBytes = 512 << 20
const maxSessionBytes = 256 << 20

// The same executable provides the private worker. It runs before application
// main (and before Windows UI initialization), and never starts a listener.
func init() {
	if len(os.Args) == 2 && os.Args[1] == workerArg {
		os.Exit(workerMain())
	}
}

type workerRequest struct {
	Mode                  string
	Codec                 txqr.CodecType
	Name, MediaType       string
	Split, Fit, PPM, Size int
	MemoryBytes           uint64
	CacheBytes            int64
}

type workerHeader struct {
	Error                             string
	Count, Total, Split, Modules, PPM int
	Mode                              string
	Carousel                          bool
	K, BlockLen                       int
	SessionID                         uint16
}

// Worker errors never publish a partially encoded or rendered session.
func workerMain() int {
	input := bufio.NewReaderSize(os.Stdin, 16<<10)
	line, err := readWorkerLine(input)
	if err != nil {
		return 2
	}
	var req workerRequest
	if json.Unmarshal(line, &req) != nil || req.Size <= 0 || req.Size > txqr.MaxTransferBytes ||
		req.MemoryBytes < 16<<20 || req.MemoryBytes > workerMemoryBytes || req.CacheBytes <= 0 || req.CacheBytes > maxSessionBytes ||
		len(req.Name) > 4096 || len(req.MediaType) > 1024 {
		return 2
	}
	if err = limitWorkerData(req.MemoryBytes); err != nil {
		return 2
	}
	debug.SetMemoryLimit(int64(req.MemoryBytes) * 3 / 4)
	body := make([]byte, req.Size)
	if _, err = io.ReadFull(input, body); err != nil {
		return 2
	}
	// The parent can now observe/cancel a live library call, rather than merely
	// canceling process startup. No payload is printed to stderr.
	if _, err = os.Stdout.Write([]byte{1}); err != nil {
		return 2
	}
	var sess *session
	size := frameSize{fit: req.Fit, ppm: req.PPM}
	if req.Mode == "decimen" {
		query := url.Values{"name": {req.Name}, "type": {req.MediaType}}
		request, _ := http.NewRequest("POST", "http://worker/encode?"+query.Encode(), nil)
		sess, err = encodeDecimen(request, body, size)
	} else if req.Mode == "qr" || req.Mode == "color" {
		sess, err = encodeClassic(body, req.Split, req.Mode, req.Codec, size, req.Name)
	} else {
		err = errors.New("unsupported mode")
	}
	if err == nil {
		var frameBytes int64
		for _, frame := range sess.frames {
			frameBytes += int64(len(frame))
		}
		available := req.CacheBytes - frameBytes
		if available <= 0 {
			err = errors.New("session cache limit exceeded")
		} else {
			sess.renderBudget = min(128<<20, available)
			renderFrames(sess, context.Background())
			if sess.renderExceeded.Load() {
				err = errors.New("render cache limit exceeded")
			} else if renderFailed(sess) {
				err = errors.New("QR render failed")
			}
		}
	}
	out := bufio.NewWriter(os.Stdout)
	if err != nil {
		_ = json.NewEncoder(out).Encode(workerHeader{Error: err.Error()})
		_ = out.Flush()
		return 0
	}
	header := workerHeader{Count: len(sess.frames), Total: sess.total, Split: sess.split, Modules: sess.modules, PPM: sess.ppm,
		Mode: sess.mode, Carousel: sess.carousel, K: sess.k, BlockLen: sess.blockLen, SessionID: sess.sessionID}
	if json.NewEncoder(out).Encode(header) != nil {
		return 2
	}
	// Stream one frame/PNG pair at a time; gob/JSON of a whole session would
	// create a second, potentially huge serialization buffer inside the worker.
	for i, frame := range sess.frames {
		for _, part := range [][]byte{frame, sess.pngs[i]} {
			if binary.Write(out, binary.LittleEndian, uint32(len(part))) != nil {
				return 2
			}
			if _, err = out.Write(part); err != nil {
				return 2
			}
		}
	}
	if out.Flush() != nil {
		return 2
	}
	return 0
}

func readWorkerLine(in *bufio.Reader) ([]byte, error) {
	line, err := in.ReadSlice('\n') // the bounded reader prevents an unbounded metadata allocation
	if err != nil {
		return nil, err
	}
	return line, nil
}

// encodeIsolated gives uninterruptible third-party calls an OS cancellation
// boundary. Killing and waiting for the child also releases all of its memory.
func encodeIsolated(ctx context.Context, req workerRequest, body []byte, started func()) (*session, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// Keep the Linux parent thread alive for PR_SET_PDEATHSIG until Wait.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := exec.CommandContext(ctx, executable, workerArg)
	prepareWorkerProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	var stderr cappedErrorOutput
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	defer stdin.Close()
	// The child waits for stdin until its Windows job limit has been installed.
	release, err := configureWorker(cmd.Process, req.MemoryBytes)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	defer release()
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	if err = json.NewEncoder(stdin).Encode(req); err == nil {
		_, err = stdin.Write(body)
	}
	stdin.Close()
	in := bufio.NewReader(stdout)
	if err == nil {
		var ack byte
		ack, err = in.ReadByte()
		if err == nil && ack != 1 {
			err = errors.New("invalid worker handshake")
		}
		if err == nil && started != nil {
			started()
		}
	}
	var sess *session
	if err == nil {
		sess, err = readWorkerSession(in, req.CacheBytes)
	}
	if err != nil {
		cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	waited = true
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("encode worker: %w", err)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("encode worker stopped (memory limit or process failure): %w", waitErr)
	}
	return sess, nil
}

func readWorkerSession(in *bufio.Reader, budget int64) (*session, error) {
	line, err := readWorkerLine(in)
	if err != nil {
		return nil, err
	}
	var h workerHeader
	if err = json.Unmarshal(line, &h); err != nil {
		return nil, err
	}
	if h.Error != "" {
		return nil, errors.New(h.Error)
	}
	if h.Count <= 0 || h.Count > 65536 || h.Total <= 0 || h.Total > txqr.MaxTransferBytes+49+2*65535 ||
		h.Split <= 0 || h.Split > 2933 || h.Modules <= 0 || h.Modules > 185 || h.PPM < minPPM || h.PPM > maxPPM {
		return nil, errors.New("invalid worker result")
	}
	s := &session{frames: make([][]byte, h.Count), pngs: make([][]byte, h.Count), total: h.Total, split: h.Split, modules: h.Modules,
		ppm: h.PPM, mode: h.Mode, carousel: h.Carousel, k: h.K, blockLen: h.BlockLen, sessionID: h.SessionID, createdAt: time.Now()}
	for i := 0; i < h.Count; i++ {
		for _, target := range []*[]byte{&s.frames[i], &s.pngs[i]} {
			var length uint32
			if err = binary.Read(in, binary.LittleEndian, &length); err != nil {
				return nil, err
			}
			// Validate before allocating, including cumulative frame and PNG bytes.
			if length == 0 || int64(length) > budget || length > 16<<20 {
				return nil, errors.New("worker output exceeds cache budget")
			}
			budget -= int64(length)
			*target = make([]byte, int(length))
			if _, err = io.ReadFull(in, *target); err != nil {
				return nil, err
			}
		}
	}
	// Require EOF as well as a successful process exit; never retain extra output.
	if _, err = in.ReadByte(); err != io.EOF {
		return nil, errors.New("unexpected worker output")
	}
	return s, nil
}

type cappedErrorOutput struct{ bytes.Buffer }

func (b *cappedErrorOutput) Write(p []byte) (int, error) {
	n := len(p)
	if left := 4096 - b.Len(); left > 0 {
		b.Buffer.Write(p[:min(left, n)])
	}
	return n, nil
}
