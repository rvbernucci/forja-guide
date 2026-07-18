//go:build !linux && !darwin

package delivery

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func configureValidatorProcess(*exec.Cmd) error {
	return fmt.Errorf("validator process-tree containment is unsupported on %s", runtime.GOOS)
}

func signalValidatorProcessTree(process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	return fmt.Errorf("validator process-tree signaling is unsupported on %s", runtime.GOOS)
}
