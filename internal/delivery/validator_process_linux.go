//go:build linux

package delivery

import (
	"os"
	"os/exec"
	"syscall"
)

func configureValidatorProcess(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	return nil
}

func signalValidatorProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, signal)
}
