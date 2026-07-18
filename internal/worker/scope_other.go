//go:build !linux && !darwin

package worker

import "fmt"

func materializeScopeDirectory(string, string) error {
	return fmt.Errorf("race-safe write-scope materialization is unsupported on this platform")
}
