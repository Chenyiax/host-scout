package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"host-scout/internal/agent"
	"host-scout/internal/model"
	tools "host-scout/pkg/tool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sendText(w http.ResponseWriter, text string, streamed bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	if streamed {
		delta, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": text})
		fmt.Fprintf(w, "data: %s\n\n", delta)
	}
	body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": text}}}}}})
	fmt.Fprintf(w, "data: %s\n\n", body)
}
func sendCommand(w http.ResponseWriter, id string, argv []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	args, _ := json.Marshal(map[string]any{"argv": argv})
	body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": id, "name": "run_command", "arguments": string(args)}}}})
	fmt.Fprintf(w, "data: %s\n\n", body)
}
func TestCLIEndToEndHistoryDenialFailureAndClear(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	var requests [][]map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, request.Input)
		switch len(requests) {
		case 1:
			sendText(w, "FIRST-ANSWER", true)
		case 2:
			if len(request.Input) != 3 || string(request.Input[1]["phase"]) != `"final_answer"` {
				t.Error("lost raw message/history")
			}
			sendCommand(w, "denied1", []string{"no-execution"})
		case 3:
			last := request.Input[len(request.Input)-1]
			if string(last["call_id"]) != `"denied1"` || !strings.Contains(string(last["output"]), "denied") {
				t.Error("missing denied feedback")
			}
			sendText(w, "DENIAL-EXPLAINED", false)
		case 4:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"PARTIAL"}`+"\n\n"+`data: {"type":"error","message":"test-key"}`+"\n\n")
		case 5:
			for _, item := range request.Input {
				if strings.Contains(string(item["content"]), "PARTIAL") {
					t.Error("partial answer committed")
				}
			}
			sendText(w, "RECOVERED", false)
		case 6:
			if len(request.Input) != 1 {
				t.Error("clear retained context")
			}
			sendText(w, "AFTER-CLEAR", false)
		default:
			t.Error("unexpected model request")
		}
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	err := Run([]string{"--model", "fake", "--base-url", server.URL}, strings.NewReader(" \n\n/unknown\n/evidence missing\nhello\ncorrect goal\nfail\nfollow up\n/evidence\n/evidence evidence-001\n/status\n/clear\nnew\n/quit\n"), &out, &errs)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"FIRST-ANSWER", "DENIAL-EXPLAINED", "RECOVERED", "AFTER-CLEAR"} {
		if strings.Count(out.String(), text) != 1 {
			t.Fatalf("missing/duplicate %s: %s", text, &out)
		}
	}
	if len(requests) != 6 || !strings.Contains(out.String(), "No host observation") || !strings.Contains(out.String(), "call=denied1") || !strings.Contains(errs.String(), "remote_failed") || strings.Contains(errs.String(), "test-key") || strings.ContainsRune(out.String(), '\x1b') {
		t.Fatalf("requests=%d\nout=%s\nerr=%s", len(requests), &out, &errs)
	}
	if !strings.Contains(out.String(), "Unknown command") || !strings.Contains(out.String(), "Evidence ID not found") {
		t.Fatal("local command errors not displayed")
	}
}
func TestCLIStepLimitRecordsAndRecovery(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			sendCommand(w, "skip1", []string{"must-not-run"})
			return
		}
		var req struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Input) != 4 || !strings.Contains(string(req.Input[2]["output"]), "step_limit") {
			t.Error("unbalanced limit history")
		}
		sendText(w, "FOLLOWUP", false)
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	err := Run([]string{"--model", "fake", "--base-url", server.URL, "--max-steps", "1"}, strings.NewReader("limit\n/evidence\nnext\n/exit\n"), &out, &errs)
	if err != nil || count != 2 || !strings.Contains(out.String(), "skipped/step_limit") || !strings.Contains(out.String(), "FOLLOWUP") || !strings.Contains(errs.String(), "step limit") {
		t.Fatalf("%v %s %s", err, &out, &errs)
	}
}
func TestStartupSecretAndErrorSafety(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "")
	t.Setenv("HOST_SCOUT_MODEL", "")
	if err := Run(nil, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("missing model accepted")
	}
	if err := Run([]string{"--model", "fake"}, strings.NewReader("secret\n"), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "HOST_SCOUT_API_KEY") {
		t.Fatal("nonterminal secret prompt")
	}
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	var errOut bytes.Buffer
	err := Run([]string{"--test-key"}, strings.NewReader(""), io.Discard, &errOut)
	if err == nil || strings.Contains(err.Error(), "test-key") || strings.Contains(errOut.String(), "test-key") {
		t.Fatalf("key in flag error: %s", &errOut)
	}
	var out bytes.Buffer
	if err := Run([]string{"--model", "fake"}, strings.NewReader("/help\n/exit\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/evidence") {
		t.Fatal("slash commands missing")
	}
}

func TestContextLimitDisplayAndExplicitClear(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"type":"error","error":{"code":"context_length_exceeded","message":"test-key"}}`+"\n\n")
			return
		}
		var req struct {
			Input []json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Input) != 1 {
			t.Error("clear did not reset history")
		}
		sendText(w, "AFTER-CLEAR", false)
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	if err := Run([]string{"--model", "fake", "--base-url", server.URL}, strings.NewReader("question\n/clear\nnew\n/exit\n"), &out, &errs); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || !strings.Contains(errs.String(), "Context limit reached") || !strings.Contains(errs.String(), "/clear") ||
		strings.Contains(errs.String(), "test-key") || !strings.Contains(out.String(), "AFTER-CLEAR") {
		t.Fatalf("context display: requests=%d out=%s errors=%s", requests, &out, &errs)
	}
}

func TestCleanupFailureDisplayNamesOriginalReasonAndRecord(t *testing.T) {
	var out bytes.Buffer
	reportTurnError(agent.ErrCleanupFailed, []agent.Evidence{{ID: "evidence-007", Result: tools.Result{
		Status: tools.Failed, Code: "cleanup_failed", TerminationReason: "cancelled", Output: "partial",
	}}}, &out)
	if !strings.Contains(out.String(), "original reason: cancelled") || !strings.Contains(out.String(), "may remain") || !strings.Contains(out.String(), "/evidence evidence-007") {
		t.Fatalf("cleanup risk hidden: %s", &out)
	}
	out.Reset()
	reportTurnError(&model.Error{Kind: model.ContextLimit}, nil, &out)
	if !strings.Contains(out.String(), "/clear") {
		t.Fatal("context recovery hint missing")
	}
}

func TestPipeEOFWhileModelRunsCancelsWithoutNextAction(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	entered := make(chan struct{})
	release := make(chan struct{})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); server.Close() }()
	pr, pw := io.Pipe()
	defer pr.Close()
	var out, errs bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Run([]string{"--model", "fake", "--base-url", server.URL}, pr, &out, &errs) }()
	if _, err := io.WriteString(pw, "question\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("model request never started")
	}
	start := time.Now()
	_ = pw.Close()
	select {
	case err := <-done:
		if err != nil || requests != 1 || time.Since(start) > time.Second {
			t.Fatalf("EOF did not stop active turn: %v requests=%d out=%s errors=%s", err, requests, &out, &errs)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF did not cancel model")
	}
}

func TestCLIStatusShowsUnlimitedAndSingleTool(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	var out, errs bytes.Buffer
	if err := Run([]string{"--model", "fake"}, strings.NewReader("/status\n/tools\n/exit\n"), &out, &errs); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "run_command:") != 1 || !strings.Contains(out.String(), "Tools: run_command  Max steps: unlimited") ||
		!strings.Contains(out.String(), "Max steps per turn: unlimited") || strings.Contains(out.String(), "inspect_system") || strings.Contains(out.String(), "list_processes") {
		t.Fatalf("status and tools disagree: %s", &out)
	}
}

func TestCLIStatusShowsEffectiveConfigAndExplicitZero(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	t.Setenv("HOST_SCOUT_MODEL", "")
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("model = 'file-model'\nmax_steps = 12\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args  []string
		label string
	}{
		{[]string{"--config", path}, "Max steps per turn: 12"},
		{[]string{"--config", path, "--max-steps", "0"}, "Max steps per turn: unlimited"},
	} {
		t.Setenv("HOST_SCOUT_API_KEY", "test-key")
		var out bytes.Buffer
		if err := Run(tc.args, strings.NewReader("/status\n/exit\n"), &out, io.Discard); err != nil || !strings.Contains(out.String(), tc.label) {
			t.Fatalf("effective config: %v %s", err, &out)
		}
	}
}
