//go:build !linux && !windows

package sender

import (
	"errors"
	"os"
	"os/exec"
)

func configureWorker(_ *os.Process, _ uint64) (func(), error) {
	return nil, errors.New("bounded encoding workers require Windows or Linux")
}
func limitWorkerData(_ uint64) error { return errors.New("unsupported encoding worker platform") }

func prepareWorkerProcess(_ *exec.Cmd) {}
