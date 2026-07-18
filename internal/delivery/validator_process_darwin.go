//go:build darwin

package delivery

import (
	"os"
	"os/exec"
	"syscall"
)

func configureValidatorProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalValidatorProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, signal)
}
