//go:build !linux

package command

import (
	"errors"
	"os"
	"os/exec"
)

// Development mode guarantees only the direct child's cancellation and wait.
func configureProcess(cmd *exec.Cmd) {}

func defaultPlatformOps() platformOps {
	return platformOps{
		wait: func(cmd *exec.Cmd) error { return cmd.Wait() },
		terminate: func(cmd *exec.Cmd, mainExited bool) error {
			if mainExited || cmd.Process == nil {
				return nil
			}
			err := cmd.Process.Kill()
			if errors.Is(err, os.ErrProcessDone) {
				return nil
			}
			return err
		},
		alive: func(*exec.Cmd) (bool, error) { return false, nil },
	}
}
