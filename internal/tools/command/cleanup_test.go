package command

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	tools "host-scout/pkg/tool"
)

func TestCleanupFailurePreservesCompletedOutputAndExit(t *testing.T) {
	cmd, _ := New(&testApprover{allow: true})
	ops := defaultPlatformOps()
	original := ops.terminate
	ops.terminate = func(process *exec.Cmd, exited bool) error {
		_ = original(process, exited)
		return errors.New("injected termination failure")
	}
	cmd.ops = &ops
	result, err := cmd.Run(context.Background(), helperArgs(t, "hello"))
	if err != nil || result.Status != tools.Failed || result.Code != "cleanup_failed" || result.TerminationReason != "completed" ||
		result.ExitCode == nil || *result.ExitCode != 0 || result.Output != "approved" {
		t.Fatalf("lost original outcome: %+v %v", result, err)
	}
}

func TestCleanupFailureOutranksCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		cmd, _ := New(&testApprover{allow: true})
		cmd.timeout = 30 * time.Millisecond
		ops := defaultPlatformOps()
		original := ops.terminate
		ops.terminate = func(process *exec.Cmd, exited bool) error {
			_ = original(process, exited)
			return errors.New("injected termination failure")
		}
		cmd.ops = &ops
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cmd.timeout = commandTimeout
			ctx = tools.WithStart(ctx, func() { time.AfterFunc(20*time.Millisecond, cancel) })
		}
		result, err := cmd.Run(ctx, helperArgs(t, "sleep"))
		cancel()
		wantReason := "timeout"
		if cancelled {
			wantReason = "cancelled"
		}
		if err != nil || result.Status != tools.Failed || result.Code != "cleanup_failed" || result.TerminationReason != wantReason {
			t.Fatalf("cancelled=%v: %+v %v", cancelled, result, err)
		}
	}
}

func TestCleanupFailureWhenDirectWaitDoesNotFinish(t *testing.T) {
	cmd, _ := New(&testApprover{allow: true})
	cmd.timeout = 20 * time.Millisecond
	cmd.cleanupBudget = 50 * time.Millisecond
	ops := defaultPlatformOps()
	originalWait := ops.wait
	release := make(chan struct{})
	ops.wait = func(process *exec.Cmd) error {
		<-release
		return originalWait(process)
	}
	cmd.ops = &ops
	start := time.Now()
	result, err := cmd.Run(context.Background(), helperArgs(t, "sleep"))
	close(release)
	if err != nil || result.Status != tools.Failed || result.Code != "cleanup_failed" || result.TerminationReason != "timeout" || time.Since(start) > time.Second {
		t.Fatalf("unreaped direct process: %+v %v elapsed=%v", result, err, time.Since(start))
	}
}

func TestCleanupFailureWhenLivenessCheckExhaustsBudget(t *testing.T) {
	cmd, _ := New(&testApprover{allow: true})
	cmd.cleanupBudget = 50 * time.Millisecond
	ops := defaultPlatformOps()
	ops.alive = func(*exec.Cmd) (bool, error) { return true, nil }
	cmd.ops = &ops
	start := time.Now()
	result, err := cmd.Run(context.Background(), helperArgs(t, "hello"))
	if err != nil || result.Status != tools.Failed || result.Code != "cleanup_failed" || result.TerminationReason != "completed" || result.Output != "approved" || time.Since(start) > time.Second {
		t.Fatalf("cleanup deadline: %+v %v elapsed=%v", result, err, time.Since(start))
	}
}
