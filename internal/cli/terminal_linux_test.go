//go:build linux

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestPTYHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--pty-cli" {
			if err := Run(os.Args[i+1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			os.Exit(0)
		}
	}
}

type ptySession struct {
	master  *os.File
	cmd     *exec.Cmd
	mu      sync.Mutex
	text    strings.Builder
	exited  chan struct{}
	exitErr error
}

func spawnPTY(t *testing.T, url, key string, extraArgs ...string) *ptySession {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cliArgs := append([]string{"-test.run=TestPTYHelper", "--", "--pty-cli", "--model", "fake", "--base-url", url}, extraArgs...)
	cmd := exec.Command(exe, cliArgs...)
	cmd.Env = append(os.Environ(), "HOST_SCOUT_API_KEY="+key)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	p := &ptySession{master: master, cmd: cmd, exited: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for ctx.Err() == nil {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, err := unix.Poll(poll, 100)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return
			}
			if poll[0].Revents == 0 {
				continue
			}
			var b [4096]byte
			n, err := unix.Read(fd, b[:])
			if err != nil || n == 0 {
				return
			}
			p.mu.Lock()
			_, _ = p.text.Write(b[:n])
			p.mu.Unlock()
		}
	}()
	go func() { p.exitErr = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			_ = cmd.Process.Kill()
			<-p.exited
		}
		cancel()
		<-readDone
		_ = master.Close()
	})
	return p
}
func (p *ptySession) output() string { p.mu.Lock(); defer p.mu.Unlock(); return p.text.String() }
func (p *ptySession) send(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(p.master, text); err != nil {
		t.Fatal(err)
	}
}
func (p *ptySession) await(t *testing.T, text string, count int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(p.output(), text) >= count {
			return
		}
		select {
		case <-p.exited:
			t.Fatalf("CLI exited early: %v\n%s", p.exitErr, p.output())
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waiting for %q x%d\n%s", text, count, p.output())
}
func (p *ptySession) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-p.exited:
		if p.exitErr != nil {
			t.Fatalf("%v\n%s", p.exitErr, p.output())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("CLI did not exit\n%s", p.output())
	}
}
func lastUser(input []map[string]json.RawMessage) string {
	var user string
	for _, item := range input {
		if string(item["role"]) == `"user"` {
			_ = json.Unmarshal(item["content"], &user)
		}
	}
	return user
}
func TestLinuxPTYInteractiveMVP(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "pty-key")
	root := t.TempDir()
	running := filepath.Join(root, "running")
	childPID := filepath.Join(root, "pid")
	var requests atomic.Int32
	var failures atomic.Int32
	var modelCancelled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var req struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := lastUser(req.Input)
		last := req.Input[len(req.Input)-1]
		if string(last["type"]) == `"function_call_output"` {
			switch user {
			case "twice":
				joined, _ := json.Marshal(req.Input)
				if !strings.Contains(string(joined), "succeeded") || !strings.Contains(string(joined), "denied") {
					t.Error("not separately approved")
				}
				sendText(w, "TWO-CALLS-EXPLAINED", false)
			default:
				t.Error("unexpected request after cancelled command")
			}
			return
		}
		switch user {
		case "hello":
			sendText(w, "HELLO-STREAM", true)
		case "correct memory not cpu":
			sendText(w, "GOAL-CORRECTED", false)
		case "twice":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"a","name":"run_command","arguments":"{\"argv\":[\"printf\",\"APPROVED\"],\"purpose\":\"检查打印输出\"}"},{"type":"function_call","call_id":"b","name":"run_command","arguments":"{\"argv\":[\"printf\",\"DENIED\"]}"}]}}`+"\n\n")
		case "modelwait":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"MODEL-WAITING"}`+"\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			modelCancelled.Store(true)
		case "approvalwait":
			sendCommand(w, "approval-cancel", []string{"printf", "MUST-NOT-EXECUTE"})
		case "long":
			sendCommand(w, "command-cancel", []string{"/bin/sh", "-c", `echo running > "$1"; sleep 30 & echo $! > "$2"; wait`, "pty-test", running, childPID})
		case "failure":
			failures.Add(1)
			http.Error(w, "pty-key", 503)
		case "followup":
			sendText(w, "RECOVERED-TURN", false)
		case "fresh":
			if len(req.Input) != 1 {
				t.Error("clear retained history")
			}
			sendText(w, "FRESH-CONTEXT", false)
		default:
			t.Errorf("unexpected user input %q", user)
			sendText(w, "unexpected", false)
		}
	}))
	// Cleanup must terminate the CLI before closing a server whose handler may
	// deliberately wait for HTTP cancellation.
	t.Cleanup(server.Close)
	p := spawnPTY(t, server.URL, "pty-key")
	prompts := 1
	p.await(t, "scout> ", prompts)
	p.send(t, "unsubmitted"+string([]byte{3}))
	prompts++
	p.await(t, "scout> ", prompts)
	if requests.Load() != 0 {
		t.Fatal("ready Ctrl+C submitted partial line")
	}
	p.send(t, "hello\n")
	p.await(t, "HELLO-STREAM", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "correct memory not cpu\n")
	p.await(t, "GOAL-CORRECTED", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "twice\n")
	p.await(t, "Allow once? [y/N]", 1)
	p.await(t, "Model purpose: 检查打印输出", 1)
	p.send(t, "Y\n")
	p.await(t, "Allow once? [y/N]", 2)
	p.send(t, "n\n")
	p.await(t, "TWO-CALLS-EXPLAINED", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "/evidence evidence-001\n")
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "/evidence evidence-002\n")
	p.await(t, "No host observation", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "modelwait\n")
	p.await(t, "MODEL-WAITING", 1)
	p.send(t, string([]byte{3}))
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "followup\n")
	p.await(t, "RECOVERED-TURN", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "approvalwait\n")
	p.await(t, "Allow once? [y/N]", 3)
	before := requests.Load()
	p.send(t, "y"+string([]byte{3}))
	prompts++
	p.await(t, "scout> ", prompts)
	if requests.Load() != before {
		t.Fatal("approval cancellation made another request")
	}
	p.send(t, "\n")
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "long\n")
	p.await(t, "Allow once? [y/N]", 4)
	p.send(t, "y\n")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(childPID); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	p.send(t, string([]byte{3}))
	prompts++
	p.await(t, "scout> ", prompts)
	if time.Since(start) > 2*time.Second {
		t.Fatal("terminal cancellation exceeded 2 seconds")
	}
	p.send(t, "failure\n")
	p.await(t, "HTTP 503", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "followup\n")
	p.await(t, "RECOVERED-TURN", 2)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "/status\n")
	p.await(t, "Execution records: 4", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "/clear\n")
	p.await(t, "Session context, records and input history cleared", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "fresh\n")
	p.await(t, "FRESH-CONTEXT", 1)
	prompts++
	p.await(t, "scout> ", prompts)
	p.send(t, "/exit\n")
	p.waitExit(t)
	if strings.Count(p.output(), "HELLO-STREAM") != 1 || strings.Contains(p.output(), "pty-key") || failures.Load() != 3 {
		t.Fatalf("unsafe/duplicate transcript\n%s", p.output())
	}
	deadline = time.Now().Add(time.Second)
	for !modelCancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !modelCancelled.Load() {
		t.Fatal("HTTP request cancellation not observed")
	}
	t.Log("Linux controlling PTY: dialogue, goal correction, per-call approve/deny, Ready/model/approval/command Ctrl+C, recovery, records, clear, exit passed")
}
func TestLinuxPTYEOFAndHiddenCredential(t *testing.T) {
	for _, mode := range []string{"ready-eof", "approval-eof", "quit", "hidden-key", "empty-key"} {
		t.Run(mode, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if mode == "approval-eof" {
					sendCommand(w, "eof-call", []string{"must-not-run"})
					return
				}
				if r.Header.Get("Authorization") != "Bearer hidden-key" {
					t.Error("hidden credential was not used")
				}
				sendText(w, "HIDDEN-OK", false)
			}))
			defer server.Close()
			key := "pty-key"
			if mode == "hidden-key" || mode == "empty-key" {
				key = ""
			}
			p := spawnPTY(t, server.URL, key)
			if mode == "hidden-key" || mode == "empty-key" {
				p.await(t, "API key: ", 1)
				deadline := time.Now().Add(time.Second)
				for {
					state, err := unix.IoctlGetTermios(int(p.master.Fd()), unix.TCGETS)
					if err == nil && state.Lflag&unix.ECHO == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("credential input was not hidden")
					}
					time.Sleep(time.Millisecond)
				}
				if mode == "empty-key" {
					p.send(t, "\n")
					select {
					case <-p.exited:
						if p.exitErr == nil {
							t.Fatal("empty credential accepted")
						}
					case <-time.After(time.Second):
						t.Fatal("empty credential did not fail")
					}
					deadline = time.Now().Add(time.Second)
					for !strings.Contains(p.output(), "API key is required") && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if count.Load() != 0 || !strings.Contains(p.output(), "API key is required") {
						t.Fatalf("empty credential: %s", p.output())
					}
					return
				}
				p.send(t, "hidden-key\n")
			}
			p.await(t, "scout> ", 1)
			switch mode {
			case "approval-eof":
				p.send(t, "command\n")
				p.await(t, "Allow once? [y/N]", 1)
				p.send(t, string([]byte{4}))
			case "quit":
				p.send(t, "/quit\n")
			case "hidden-key":
				p.send(t, "hello\n")
				p.await(t, "HIDDEN-OK", 1)
				p.await(t, "scout> ", 2)
				p.send(t, string([]byte{4}))
			default:
				p.send(t, string([]byte{4}))
			}
			p.waitExit(t)
			if mode == "approval-eof" && count.Load() != 1 {
				t.Fatal("EOF continued model loop")
			}
			if strings.Contains(p.output(), "hidden-key") {
				t.Fatal("credential echoed")
			}
		})
	}
}

func TestLinuxPTYLongTurnClarificationRetryAndContextLimit(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "pty-key")
	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := lastUser(req.Input)
		mu.Lock()
		counts[user]++
		count := counts[user]
		mu.Unlock()
		switch user {
		case "longloop":
			if count <= 9 {
				sendCommand(w, fmt.Sprintf("long-%d", count), []string{"printf", "LOOP"})
			} else {
				sendText(w, "LONG-DONE", false)
			}
		case "clarify":
			sendText(w, "Which PID?", false)
		case "PID 23":
			joined, _ := json.Marshal(req.Input)
			if !strings.Contains(string(joined), "Which PID?") {
				t.Error("clarification history missing")
			}
			sendText(w, "GOT-PID-23", false)
		case "retrywait":
			w.WriteHeader(503)
		case "context":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"type":"error","error":{"code":"context_length_exceeded"}}`+"\n\n")
		case "fresh":
			if len(req.Input) != 1 {
				t.Error("clear did not reset context")
			}
			sendText(w, "FRESH-ANSWER", false)
		default:
			t.Errorf("unexpected user %q", user)
		}
	}))
	defer server.Close()
	p := spawnPTY(t, server.URL, "pty-key")
	p.await(t, "scout> ", 1)
	p.send(t, "longloop\n")
	for i := 1; i <= 9; i++ {
		p.await(t, "Allow once? [y/N]", i)
		p.send(t, "y\n")
	}
	p.await(t, "LONG-DONE", 1)
	p.await(t, "scout> ", 2)
	p.send(t, "clarify\n")
	p.await(t, "Which PID?", 1)
	p.await(t, "scout> ", 3)
	p.send(t, "PID 23\n")
	p.await(t, "GOT-PID-23", 1)
	p.await(t, "scout> ", 4)
	p.send(t, "retrywait\n")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := counts["retrywait"]
		mu.Unlock()
		if count == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	retryCount := counts["retrywait"]
	mu.Unlock()
	if retryCount != 1 {
		t.Fatalf("retry request did not start: %d", retryCount)
	}
	start := time.Now()
	p.send(t, string([]byte{3}))
	p.await(t, "scout> ", 5)
	if time.Since(start) > time.Second {
		t.Fatal("retry backoff ignored Ctrl+C")
	}
	mu.Lock()
	retryCount = counts["retrywait"]
	longCount := counts["longloop"]
	mu.Unlock()
	if retryCount != 1 || longCount != 10 {
		t.Fatalf("retry=%d long=%d", retryCount, longCount)
	}
	p.send(t, "context\n")
	p.await(t, "Context limit reached", 1)
	p.await(t, "scout> ", 6)
	p.send(t, "/clear\n")
	p.await(t, "Session context, records and input history cleared", 1)
	p.await(t, "scout> ", 7)
	p.send(t, "fresh\n")
	p.await(t, "FRESH-ANSWER", 1)
	p.await(t, "scout> ", 8)
	p.send(t, "/exit\n")
	p.waitExit(t)
	t.Log("Linux PTY: >8 model calls, clarification, retry cancellation, context-limit recovery, clear and exit passed")
}

func TestLinuxPTYOptionalStepLimit(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "pty-key")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			sendCommand(w, "limited", []string{"printf", "NEVER-RUN"})
		} else {
			sendText(w, "FOLLOWUP-OK", false)
		}
	}))
	defer server.Close()
	p := spawnPTY(t, server.URL, "pty-key", "--max-steps", "1")
	p.await(t, "scout> ", 1)
	p.send(t, "/status\n")
	p.await(t, "Max steps per turn: 1", 1)
	p.await(t, "scout> ", 2)
	p.send(t, "limit\n")
	p.await(t, "skipped (step_limit)", 1)
	p.await(t, "scout> ", 3)
	if strings.Contains(p.output(), "Allow once? [y/N]") {
		t.Fatal("last-step command was offered for approval")
	}
	p.send(t, "next\n")
	p.await(t, "FOLLOWUP-OK", 1)
	p.await(t, "scout> ", 4)
	p.send(t, "/exit\n")
	p.waitExit(t)
	if requests != 2 {
		t.Fatalf("unexpected model calls: %d", requests)
	}
}

func TestLinuxPTYEOFDuringCleanupFailureExitsNonzero(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "pty-key")
	root := t.TempDir()
	pidfile := filepath.Join(root, "detached-pid")
	ready := filepath.Join(root, "ready")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		script := `setsid sh -c 'echo $$ > "$1"; sleep 30' detached "$1" & while [ ! -s "$1" ]; do sleep 0.01; done; echo ready > "$2"; sleep 30`
		sendCommand(w, "eof-cleanup", []string{"/bin/sh", "-c", script, "pty-cleanup", pidfile, ready})
	}))
	defer server.Close()
	p := spawnPTY(t, server.URL, "pty-key")
	p.await(t, "scout> ", 1)
	p.send(t, "run\n")
	p.await(t, "Allow once? [y/N]", 1)
	p.send(t, "y\n")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command never started: %s", p.output())
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid detached pid: %q %v", data, err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	start := time.Now()
	p.send(t, string([]byte{4}))
	select {
	case <-p.exited:
		if p.exitErr == nil {
			t.Fatal("cleanup failure exited successfully")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("EOF cleanup hung: %s", p.output())
	}
	if time.Since(start) > 2*time.Second || requests.Load() != 1 || !strings.Contains(p.output(), "original reason: cancelled") ||
		!strings.Contains(p.output(), "Managed processes may remain") || !strings.Contains(p.output(), "/evidence evidence-001") {
		t.Fatalf("cleanup failure hidden or too slow: elapsed=%v requests=%d\n%s", time.Since(start), requests.Load(), p.output())
	}
}

func TestLinuxPTYCleanupFailurePairsBatchAndStopsModel(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "pty-key")
	root := t.TempDir()
	pidfile := filepath.Join(root, "detached-pid")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNo := requests.Add(1)
		var req struct {
			Input []map[string]json.RawMessage `json:"input"`
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Tools) != 1 || string(req.Tools[0]["name"]) != `"run_command"` {
			t.Error("CLI model request advertised extra tools")
		}
		if requestNo == 1 {
			argv := []string{"/bin/sh", "-c", `setsid sh -c 'echo $$ > "$1"; sleep 30' detached "$1" & while [ ! -s "$1" ]; do sleep 0.01; done; exit 0`, "cleanup-test", pidfile}
			argsA, _ := json.Marshal(map[string]any{"argv": argv})
			argsB, _ := json.Marshal(map[string]any{"argv": []string{"printf", "NEVER-RUN"}})
			response, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{
				map[string]any{"type": "function_call", "call_id": "a", "name": "run_command", "arguments": string(argsA)},
				map[string]any{"type": "function_call", "call_id": "b", "name": "run_command", "arguments": string(argsB)},
			}}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", response)
			return
		}
		if requestNo != 2 || lastUser(req.Input) != "followup" {
			t.Error("unexpected model request after cleanup failure")
		}
		joined, _ := json.Marshal(req.Input)
		if !strings.Contains(string(joined), "cleanup_failed") || !strings.Contains(string(joined), `"call_id":"b"`) {
			t.Error("failed batch history was not paired")
		}
		sendText(w, "AFTER-FAILURE", false)
	}))
	defer server.Close()
	p := spawnPTY(t, server.URL, "pty-key")
	p.await(t, "scout> ", 1)
	p.send(t, "cleanup\n")
	p.await(t, "Allow once? [y/N]", 1)
	p.send(t, "y\n")
	p.await(t, "Managed processes may remain", 1)
	p.await(t, "scout> ", 2)
	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid detached PID: %q %v", data, err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if requests.Load() != 1 || strings.Count(p.output(), "Allow once? [y/N]") != 1 || !strings.Contains(p.output(), "skipped (cleanup_failed)") {
		t.Fatalf("continued after cleanup failure: requests=%d\n%s", requests.Load(), p.output())
	}
	p.send(t, "/evidence evidence-001\n")
	p.await(t, "original reason", 1)
	p.await(t, "scout> ", 3)
	p.send(t, "/evidence evidence-002\n")
	p.await(t, "No host observation", 1)
	p.await(t, "scout> ", 4)
	p.send(t, "followup\n")
	p.await(t, "AFTER-FAILURE", 1)
	p.await(t, "scout> ", 5)
	p.send(t, "/exit\n")
	p.waitExit(t)
	if requests.Load() != 2 {
		t.Fatalf("unexpected model requests: %d", requests.Load())
	}
}

func TestLinuxPTYUnicodeEditingHistoryAndClear(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "pty-key")
	var mu sync.Mutex
	var users []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		users = append(users, lastUser(req.Input))
		mu.Unlock()
		sendText(w, "EDIT-OK", false)
	}))
	defer server.Close()
	p := spawnPTY(t, server.URL, "pty-key")
	p.await(t, "\r\nscout> ", 1)
	p.send(t, "ab中e\u0301\x1b[D\x1b[DX\x1b[H\x1b[3~\x1b[F\x7f\n")
	p.await(t, "\r\x1b[2Kscout> bX中", 1)
	if !strings.Contains(p.output(), "\r\x1b[2Kscout> bX中") {
		t.Fatalf("edited line not rendered: %q", p.output())
	}
	p.await(t, "EDIT-OK", 1)
	p.await(t, "\r\nscout> ", 2)
	p.send(t, "草稿\x1b[A\x1b[B\n")
	p.await(t, "EDIT-OK", 2)
	p.await(t, "\r\nscout> ", 3)
	p.send(t, "/clear\n")
	p.await(t, "input history cleared", 1)
	p.await(t, "\r\nscout> ", 4)
	p.send(t, "\x1b[A\n")
	p.await(t, "\r\nscout> ", 5)
	p.send(t, "/exit\n")
	p.waitExit(t)
	mu.Lock()
	defer mu.Unlock()
	if len(users) != 2 || users[0] != "bX中" || users[1] != "草稿" {
		t.Fatalf("submitted users: %q", users)
	}
}
