package command

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	tools "host-scout/pkg/tool"
)

// platformOps are private so tests can exercise failed termination and liveness
// checks without claiming that a fake is a Linux cancellation acceptance test.
type platformOps struct {
	terminate func(*exec.Cmd, bool) error
	alive     func(*exec.Cmd) (bool, error)
	wait      func(*exec.Cmd) error
}

func (t Tool) execute(parent context.Context, argv []string) tools.Result {
	result := tools.Result{Status: tools.Succeeded, Argv: append([]string(nil), argv...)}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = commandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = commandEnvironment()
	configureProcess(cmd)
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		result.Status, result.Code = tools.Failed, "pipe_failed"
		return result
	}
	cmd.Stdout, cmd.Stderr = writePipe, writePipe
	output := &boundedOutput{limit: maxOutputBytes}
	// The hook represents an actual start attempt. Validation and approval have
	// already succeeded; no process exists before Start returns successfully.
	tools.Started(ctx)
	err = cmd.Start()
	_ = writePipe.Close()
	if err != nil {
		_ = readPipe.Close()
		result.Status, result.Code = tools.Failed, "start_failed"
		result.TerminationReason = "start_failed"
		return result
	}
	defer readPipe.Close()
	ops := defaultPlatformOps()
	if t.ops != nil {
		ops = *t.ops
	}
	waitDone := make(chan error, 1)
	copyDone := make(chan error, 1)
	go func() { waitDone <- ops.wait(cmd) }()
	go func() { _, copyErr := io.Copy(output, readPipe); copyDone <- copyErr }()

	var waitErr error
	mainExited := false
	select {
	case waitErr = <-waitDone:
		mainExited = true
		if waitErr == nil {
			result.TerminationReason = "completed"
		} else if _, ok := waitErr.(*exec.ExitError); ok {
			result.TerminationReason = "nonzero_exit"
		} else {
			result.TerminationReason = "wait_failed"
		}
	case <-ctx.Done():
		if parent.Err() != nil {
			result.TerminationReason = "cancelled"
		} else {
			result.TerminationReason = "timeout"
		}
	}
	naturallyExited := mainExited

	// Every started call owns a finite cleanup. On Linux this also runs after a
	// normal direct-process exit, because descendants may hold the pipe open.
	cleanupBudget := t.cleanupBudget
	if cleanupBudget <= 0 {
		cleanupBudget = 2 * time.Second
	}
	deadline := time.Now().Add(cleanupBudget)
	termErr := ops.terminate(cmd, mainExited)
	cleanupFailed := termErr != nil
	if !mainExited {
		select {
		case waitErr = <-waitDone:
			mainExited = true
		case <-time.After(time.Until(deadline)):
			cleanupFailed = true
		}
	}
	if naturallyExited && cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			result.ExitCode = &code
		}
	}
	for time.Now().Before(deadline) {
		alive, checkErr := ops.alive(cmd)
		if checkErr != nil {
			cleanupFailed = true
			break
		}
		if !alive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if alive, checkErr := ops.alive(cmd); checkErr != nil || alive {
		cleanupFailed = true
	}
	pipeBudget := t.pipeBudget
	if pipeBudget <= 0 {
		pipeBudget = time.Second
	}
	pipeDeadline := deadline
	if pipeLimit := time.Now().Add(pipeBudget); pipeLimit.Before(pipeDeadline) {
		pipeDeadline = pipeLimit
	}
	select {
	case copyErr := <-copyDone:
		if copyErr != nil {
			cleanupFailed = true
		}
	case <-time.After(max(0, time.Until(pipeDeadline))):
		cleanupFailed = true
		_ = readPipe.Close()
	}
	result.Output, result.Truncated = output.String(), output.Truncated()
	if cleanupFailed || !mainExited {
		result.Status, result.Code = tools.Failed, "cleanup_failed"
		return result
	}
	switch result.TerminationReason {
	case "cancelled":
		result.Status, result.Code = tools.Cancelled, "cancelled"
	case "timeout":
		result.Status, result.Code = tools.TimedOut, "timeout"
	case "nonzero_exit":
		result.Status, result.Code = tools.Failed, "nonzero_exit"
	case "wait_failed":
		result.Status, result.Code = tools.Failed, "wait_failed"
	default:
		if waitErr != nil && !errors.Is(waitErr, os.ErrProcessDone) {
			result.Status, result.Code = tools.Failed, "wait_failed"
		}
	}
	return result
}
