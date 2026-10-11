//go:build linux

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tools "host-scout/pkg/tool"
)

func shellArgs(t *testing.T, script string, extra ...string) json.RawMessage {
	t.Helper()
	argv := append([]string{"/bin/sh", "-c", script, "ownership-test"}, extra...)
	raw, err := json.Marshal(map[string]any{"argv": argv})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertNotRunning(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if syscall.Kill(pid, 0) == syscall.ESRCH || strings.Contains(string(status), "State:\tZ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still running", pid)
}

func TestLinuxNormalExitCleansDescendantsAndPipes(t *testing.T) {
	for _, exit := range []int{0, 7} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			cmd, _ := New(&testApprover{allow: true})
			start := time.Now()
			result, err := cmd.Run(context.Background(), shellArgs(t, `sleep 30 & echo $! > "$1"; exit "$2"`, pidfile, strconv.Itoa(exit)))
			if err != nil || result.ExitCode == nil || *result.ExitCode != exit || time.Since(start) > 2*time.Second {
				t.Fatalf("result=%+v err=%v elapsed=%v", result, err, time.Since(start))
			}
			if exit == 0 && (result.Status != tools.Succeeded || result.TerminationReason != "completed") ||
				exit != 0 && (result.Status != tools.Failed || result.Code != "nonzero_exit" || result.TerminationReason != "nonzero_exit") {
				t.Fatalf("wrong terminal result: %+v", result)
			}
			data, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if pid <= 0 {
				t.Fatal("missing child PID")
			}
			assertNotRunning(t, pid)
		})
	}
}

func TestLinuxOwnershipDoesNotKillOtherGroupOrAccumulateFDs(t *testing.T) {
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := New(&testApprover{allow: true})
	for i := 0; i < 12; i++ {
		result, err := cmd.Run(context.Background(), shellArgs(t, `sleep 30 & exit 0`))
		if err != nil || result.Status != tools.Succeeded {
			t.Fatalf("call %d: %+v %v", i, result, err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+3 {
		t.Fatalf("file descriptors accumulated: before=%d after=%d", len(before), len(after))
	}
	if err := syscall.Kill(other.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated process killed: %v", err)
	}
	result, err := cmd.Run(context.Background(), json.RawMessage(`{"argv":["/nonexistent-command-for-test"]}`))
	if err != nil || result.Code != "start_failed" || syscall.Kill(other.Process.Pid, 0) != nil {
		t.Fatalf("start failure affected other process: %+v %v", result, err)
	}
}

func TestLinuxRepeatedCancellationHasOneResult(t *testing.T) {
	cmd, _ := New(&testApprover{allow: true})
	ctx, cancel := context.WithCancel(context.Background())
	ctx = tools.WithStart(ctx, func() { cancel(); cancel() })
	result, err := cmd.Run(ctx, shellArgs(t, `sleep 30 & wait`))
	if err != nil || result.Status != tools.Cancelled || result.TerminationReason != "cancelled" {
		t.Fatalf("repeated cancel: %+v %v", result, err)
	}
}

func TestLinuxDetachedPipeHolderHasBoundedFailure(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "detached-pid")
	cmd, _ := New(&testApprover{allow: true})
	start := time.Now()
	result, err := cmd.Run(context.Background(), shellArgs(t, `setsid sh -c 'echo $$ > "$1"; sleep 30' detached "$1" & while [ ! -s "$1" ]; do sleep 0.01; done; exit 0`, pidfile))
	data, readErr := os.ReadFile(pidfile)
	if readErr == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			defer syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if err != nil || result.Status != tools.Failed || result.Code != "cleanup_failed" || result.TerminationReason != "completed" ||
		time.Since(start) < 800*time.Millisecond || time.Since(start) > 2*time.Second {
		t.Fatalf("pipe wait was not bounded: %+v %v elapsed=%v pidErr=%v", result, err, time.Since(start), readErr)
	}
}
