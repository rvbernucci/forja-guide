//go:build darwin

package worker

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, signal)
}
