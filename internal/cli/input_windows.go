//go:build windows

package cli

import (
	"context"
	"golang.org/x/sys/windows"
	"os"
)

func terminalByte(ctx context.Context, file *os.File) (byte, error) {
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		event, err := windows.WaitForSingleObject(windows.Handle(file.Fd()), 100)
		if err != nil {
			return 0, err
		}
		if event == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		var b [1]byte
		n, err := file.Read(b[:])
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, os.ErrClosed
		}
		return b[0], nil
	}
}
