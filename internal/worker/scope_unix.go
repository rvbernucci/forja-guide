//go:build linux || darwin

package worker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// materializeScopeDirectory walks from an already authorized worktree handle.
// Holding each parent descriptor prevents a concurrent path substitution from
// redirecting mkdir outside that directory.
func materializeScopeDirectory(worktree string, scope string) error {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	current, err := unix.Open(worktree, flags, 0)
	if err != nil {
		return fmt.Errorf("open worktree without following links: %w", err)
	}
	defer func() { _ = unix.Close(current) }()

	for _, component := range strings.Split(filepath.Clean(scope), string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("invalid write scope component %q", component)
		}
		next, openErr := unix.Openat(current, component, flags, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil &&
				!errors.Is(mkdirErr, unix.EEXIST) {
				return fmt.Errorf("create component %q: %w", component, mkdirErr)
			}
			next, openErr = unix.Openat(current, component, flags, 0)
		}
		if openErr != nil {
			return fmt.Errorf("open component %q without following links: %w", component, openErr)
		}
		if err := unix.Close(current); err != nil {
			_ = unix.Close(next)
			return fmt.Errorf("close parent write-scope handle: %w", err)
		}
		current = next
	}
	return nil
}
