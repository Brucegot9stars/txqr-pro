//go:build linux

package sender

import (
	"os"
	"os/exec"
	"syscall"
)

func configureWorker(_ *os.Process, _ uint64) (func(), error) { return func() {}, nil }

// RLIMIT_DATA limits writable private mappings (including Go heap growth).
// It deliberately does not cap reserved PROT_NONE address space or claim an
// exact RSS limit; a Go runtime reserves much more virtual space than it uses.
func limitWorkerData(limit uint64) error {
	var inherited syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_DATA, &inherited); err != nil {
		return err
	}
	if inherited.Max < limit {
		limit = inherited.Max
	}
	return syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: limit, Max: limit})
}

func prepareWorkerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
