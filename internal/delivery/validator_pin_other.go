//go:build !linux && !darwin

package delivery

import (
	"fmt"
	"os"
	"runtime"
)

func openValidatorSource(string) (*os.File, error) {
	return nil, fmt.Errorf("validator executable pinning is unsupported on %s", runtime.GOOS)
}
