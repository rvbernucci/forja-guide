//go:build !linux && !darwin

package worker

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(*exec.Cmd) {}

func signalProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return process.Signal(signal)
}
