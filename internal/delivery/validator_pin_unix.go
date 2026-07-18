//go:build linux || darwin

package delivery

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openValidatorSource(name string) (*os.File, error) {
	fd, err := unix.Open(
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open executable without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open executable: invalid file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		_ = file.Close()
		return nil, fmt.Errorf("executable is not an executable regular file")
	}
	return file, nil
}
