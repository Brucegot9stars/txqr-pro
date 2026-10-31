//go:build linux

package sender

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/divan/txqr"
)

func TestWorkerParentDeathHelper(t *testing.T) {
	if os.Getenv("QRT_TEST_PARENT_DEATH") != "1" {
		return
	}
	runtime.LockOSThread()
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	cmd := exec.Command(executable, workerArg)
	prepareWorkerProcess(cmd)
	input, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
		os.Exit(2)
	}
	body := make([]byte, 8192*32)
	req := workerRequest{Mode: "qr", Codec: txqr.CodecBinary, Split: 32, Size: len(body), MemoryBytes: workerMemoryBytes, CacheBytes: maxSessionBytes}
	if json.NewEncoder(input).Encode(req) != nil {
		os.Exit(2)
	}
	if _, err = input.Write(body); err != nil {
		os.Exit(2)
	}
	if ack, err := bufio.NewReader(out).ReadByte(); err != nil || ack != 1 {
		os.Exit(2)
	}
	fmt.Printf("worker-pid:%d\n", cmd.Process.Pid)
	// Deliberately bypass all cleanup while the child is encoding.
	os.Exit(0)
}

func TestParentDeathKillsLiveWorker(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(executable, "-test.run=^TestWorkerParentDeathHelper$")
	parent.Env = append(os.Environ(), "QRT_TEST_PARENT_DEATH=1")
	output, err := parent.Output()
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(output), "worker-pid:")))
	if err != nil {
		t.Fatal(string(output), err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", id))
		if os.IsNotExist(err) {
			return
		}
		// A zombie has terminated; reaping belongs to its new parent after our
		// intentionally crashing helper has exited.
		if closing := strings.LastIndexByte(string(status), ')'); closing >= 0 && len(status) > closing+2 && status[closing+2] == 'Z' {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker survived its parent")
}
