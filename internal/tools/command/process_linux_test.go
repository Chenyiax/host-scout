//go:build linux

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"host-scout/internal/tools"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLinuxCancellationKillsProcessGroup(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			// The child shares its parent's process group and does not daemonize.
			args, _ := json.Marshal(map[string]any{"argv": []string{"/bin/sh", "-c", `sleep 30 & echo $! > "$1"; wait`, "group-test", pidfile}})
			tool, _ := New(&testApprover{allow: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if timeout {
				tool.timeout = 250 * time.Millisecond
			}
			result := make(chan tools.Result, 1)
			go func() {
				v, err := tool.Run(ctx, args)
				if err != nil {
					result <- tools.Result{Code: "test_error"}
					return
				}
				result <- v
			}()
			deadline := time.Now().Add(time.Second)
			var pid int
			for time.Now().Before(deadline) {
				b, err := os.ReadFile(pidfile)
				if err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
					if pid > 0 {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid <= 0 {
				t.Fatal("child did not start")
			}
			start := time.Now()
			if !timeout {
				cancel()
			}
			select {
			case r := <-result:
				expected := tools.Cancelled
				if timeout {
					expected = tools.TimedOut
				}
				if r.Status != expected || r.ExitCode != nil {
					t.Fatalf("%+v", r)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation exceeded 2 seconds")
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("slow cancellation")
			}
			// WSL's minimal init may leave a killed child zombie briefly. A zombie
			// cannot execute; treat ESRCH or Z state as terminated, not as a live child.
			deadline = time.Now().Add(time.Second)
			for {
				err := syscall.Kill(pid, 0)
				status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
				if err == syscall.ESRCH || strings.Contains(string(status), "State:\tZ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("child %d still running: %s", pid, status)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
