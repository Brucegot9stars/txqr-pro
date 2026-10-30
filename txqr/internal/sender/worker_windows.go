//go:build windows

package sender

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var kernelWorker = syscall.NewLazyDLL("kernel32.dll")

func configureWorker(process *os.Process, limit uint64) (func(), error) {
	job, _, err := kernelWorker.NewProc("CreateJobObjectW").Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("create encode job: %w", err)
	}
	closeJob := func() { syscall.CloseHandle(syscall.Handle(job)) }
	// JOBOBJECT_EXTENDED_LIMIT_INFORMATION: native Windows ABI, including the
	// 8-byte alignment of IO_COUNTERS on both 32-bit and 64-bit Windows.
	size, offset := 112, 96
	if unsafe.Sizeof(uintptr(0)) == 8 {
		size, offset = 144, 112
	}
	info := make([]byte, size)
	binary.LittleEndian.PutUint32(info[16:], 0x100|0x2000) // PROCESS_MEMORY | KILL_ON_JOB_CLOSE
	if unsafe.Sizeof(uintptr(0)) == 8 {
		binary.LittleEndian.PutUint64(info[offset:], limit)
	} else {
		binary.LittleEndian.PutUint32(info[offset:], uint32(limit))
	}
	ok, _, err := kernelWorker.NewProc("SetInformationJobObject").Call(job, 9, uintptr(unsafe.Pointer(&info[0])), uintptr(size))
	if ok == 0 {
		closeJob()
		return nil, fmt.Errorf("limit encode job: %w", err)
	}
	handle, _, err := kernelWorker.NewProc("OpenProcess").Call(0x100|1, 0, uintptr(process.Pid)) // SET_QUOTA | TERMINATE
	if handle == 0 {
		closeJob()
		return nil, fmt.Errorf("open encode process: %w", err)
	}
	defer syscall.CloseHandle(syscall.Handle(handle))
	ok, _, err = kernelWorker.NewProc("AssignProcessToJobObject").Call(job, handle)
	if ok == 0 {
		closeJob()
		return nil, fmt.Errorf("assign encode job: %w", err)
	}
	return closeJob, nil
}

// The parent's job object is installed before any input reaches the child.
func limitWorkerData(_ uint64) error { return nil }

func prepareWorkerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
