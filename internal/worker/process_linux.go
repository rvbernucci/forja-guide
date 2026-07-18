//go:build linux

package worker

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

func signalProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, signal)
}
