//go:build !linux && !windows

package cli

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
)

func terminalByte(ctx context.Context, file *os.File) (byte, error) {
	// BSD/macOS select has a bounded wait, so closing input does not leak a reader.
	fd := int(file.Fd())
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var read unix.FdSet
		read.Set(fd)
		timeout := unix.Timeval{Usec: 100000}
		n, err := unix.Select(fd+1, &read, nil, nil, &timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		var b [1]byte
		n, err = unix.Read(fd, b[:])
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, os.ErrClosed
		}
		return b[0], nil
	}
}
