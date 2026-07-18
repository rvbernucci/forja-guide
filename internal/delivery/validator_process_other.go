//go:build !linux && !darwin

package delivery

import (
	"os"
	"os/exec"
	"syscall"
)

func configureValidatorProcess(*exec.Cmd) {}

func signalValidatorProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return process.Signal(signal)
}
