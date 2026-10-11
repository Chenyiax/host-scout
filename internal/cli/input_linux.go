//go:build linux

package cli

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
)

func terminalByte(ctx context.Context, file *os.File) (byte, error) {
	fd := int(file.Fd())
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		events := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(events, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if events[0].Revents == 0 {
			continue
		}
		var b [1]byte
		n, err := unix.Read(fd, b[:])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, os.ErrClosed
		}
		return b[0], nil
	}
}
