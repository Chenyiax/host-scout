//go:build linux

package command

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The child gets a fresh process group. Cleanup kills ordinary descendants
// even after direct-process exit. Detached descendants are outside this
// guarantee; already performed host side effects cannot be rolled back.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func defaultPlatformOps() platformOps {
	return platformOps{
		wait: func(cmd *exec.Cmd) error { return cmd.Wait() },
		terminate: func(cmd *exec.Cmd, _ bool) error {
			if cmd.Process == nil {
				return nil
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return nil
			}
			return err
		},
		alive: func(cmd *exec.Cmd) (bool, error) {
			if cmd.Process == nil {
				return false, nil
			}
			entries, err := os.ReadDir("/proc")
			if err != nil {
				return false, err
			}
			for _, entry := range entries {
				if _, err := strconv.Atoi(entry.Name()); err != nil {
					continue
				}
				stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
				if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
					continue
				}
				if err != nil {
					return false, err
				}
				closeName := strings.LastIndexByte(string(stat), ')')
				if closeName < 0 {
					continue
				}
				fields := strings.Fields(string(stat[closeName+1:]))
				if len(fields) < 3 {
					continue
				}
				group, _ := strconv.Atoi(fields[2])
				if group == cmd.Process.Pid && fields[0] != "Z" && fields[0] != "X" {
					return true, nil
				}
			}
			return false, nil
		},
	}
}
