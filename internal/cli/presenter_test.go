package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"host-scout/internal/agent"
	"host-scout/internal/model"
	tools "host-scout/pkg/tool"
)

func TestPresenterMultipleCallsAndSingleConclusion(t *testing.T) {
	var out bytes.Buffer
	p := &turnPresenter{out: &out}
	now := time.Now()
	p.OnEvent(agent.Event{Kind: agent.TurnStarted})
	p.OnEvent(agent.Event{Kind: agent.ModelRequested, Step: 1})
	p.OnEvent(agent.Event{Kind: agent.TextDelta, Text: "hello "})
	p.OnEvent(agent.Event{Kind: agent.TextDelta, Text: "world"})
	for i, status := range []tools.Status{tools.Succeeded, tools.Denied} {
		id := string(rune('a' + i))
		p.OnEvent(agent.Event{Kind: agent.ToolProposed, Tool: "run_command", CallID: id})
		if i == 0 {
			p.OnEvent(agent.Event{Kind: agent.ApprovalRequested, CallID: id, Argv: []string{"echo", "ok"}})
			p.OnEvent(agent.Event{Kind: agent.ToolStarted, Tool: "run_command", CallID: id})
		}
		record := agent.Evidence{ID: "evidence-" + id, Tool: "run_command", CallID: id, Result: tools.Result{Status: status}}
		if i == 0 {
			record.StartedAt, record.FinishedAt = now, now.Add(time.Second)
		}
		p.OnEvent(agent.Event{Kind: agent.ToolFinished, Evidence: &record})
	}
	p.OnEvent(agent.Event{Kind: agent.ModelRequested, Step: 2})
	p.OnEvent(agent.Event{Kind: agent.TurnFinished})
	p.OnEvent(agent.Event{Kind: agent.TurnFinished})
	got := out.String()
	if strings.Count(got, "hello world") != 1 || strings.Count(got, "Turn completed.") != 1 || strings.Count(got, "Not executed; no host observation") != 1 || strings.Count(got, "Waiting for model response") != 2 {
		t.Fatal(got)
	}
}

func TestPresenterCancellationAndFailureNeverShowCompletion(t *testing.T) {
	for _, err := range []error{context.Canceled, &model.Error{Kind: model.Transport}} {
		var out bytes.Buffer
		p := &turnPresenter{out: &out}
		p.OnEvent(agent.Event{Kind: agent.TurnStarted})
		p.OnEvent(agent.Event{Kind: agent.ModelRequested, Step: 1})
		p.OnEvent(agent.Event{Kind: agent.TurnFinished, Err: err})
		reportTurnError(err, nil, &out)
		if strings.Contains(out.String(), "Turn completed") || strings.Count(out.String(), "Turn stopped:") != 1 {
			t.Fatal(out.String())
		}
	}
}

func TestTurnErrorRecoveryHints(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&model.Error{Kind: model.ContextLimit}, "use /clear"},
		{&model.Error{Kind: model.Transport}, "Check the endpoint and network"},
		{&model.Error{Kind: model.Cancelled}, "You can enter a new question"},
		{context.Canceled, "You can enter a new question"},
		{agent.ErrCleanupFailed, "Inspect /evidence e1"},
	} {
		var out bytes.Buffer
		item := agent.Evidence{ID: "e1", Result: tools.Result{Status: tools.Failed, Code: "cleanup_failed"}}
		reportTurnError(tc.err, []agent.Evidence{item}, &out)
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v: %s", tc.err, out.String())
		}
	}
}

func TestUntrustedDisplayDoesNotMutateEvidence(t *testing.T) {
	raw := "line\x1b[2J\rnext"
	item := agent.Evidence{ID: "e1", Tool: "run_command", CallID: "c", Arguments: []byte(`{"argv":["echo","x"],"purpose":"check\u001b[31m"}`), StartedAt: time.Now(), FinishedAt: time.Now(), Result: tools.Result{Status: tools.Succeeded, Output: raw}}
	var out bytes.Buffer
	printEvidence(&out, item)
	if strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), "\\x1B[2J\\x0D") || !strings.Contains(out.String(), "Model purpose: check\\x1B[31m") || item.Result.Output != raw {
		t.Fatal(out.String())
	}
}

func TestPresenterEscapesSplitUTF8AndBoundsPreview(t *testing.T) {
	var out bytes.Buffer
	p := &turnPresenter{out: &out}
	text := []byte("中文")
	p.OnEvent(agent.Event{Kind: agent.TextDelta, Text: string(text[:1])})
	p.OnEvent(agent.Event{Kind: agent.TextDelta, Text: string(text[1:]) + "\x1b[31m\r"})
	p.OnEvent(agent.Event{Kind: agent.ToolFinished, Evidence: &agent.Evidence{
		ID: "evidence-001", Tool: "run_command", Result: tools.Result{Status: tools.Succeeded, Output: strings.Repeat("x", 1200) + "\x1b[2J"},
		StartedAt: time.Now(), FinishedAt: time.Now(),
	}})
	got := out.String()
	if !strings.Contains(got, "中文\\x1B[31m\\x0D") || strings.Contains(got, "\x1b") || !strings.Contains(got, "Preview abbreviated") || strings.Contains(got, "[2J") {
		t.Fatal(got)
	}
}

func TestCommandCardsExecutedAndUnexecuted(t *testing.T) {
	var out bytes.Buffer
	p := &turnPresenter{out: &out}
	now := time.Now()
	code := 7
	for _, item := range []agent.Evidence{
		{ID: "e1", Tool: "run_command", CallID: "a", Arguments: []byte(`{"argv":["sh","-c","exit 7"],"purpose":"检查状态"}`), StartedAt: now, FinishedAt: now.Add(250 * time.Millisecond), Result: tools.Result{Status: tools.Failed, Code: "exit_nonzero", ExitCode: &code, Output: "error"}},
		{ID: "e2", Tool: "run_command", CallID: "b", Arguments: []byte(`{"argv":["echo","x"],"purpose":"解释"}`), Result: tools.Result{Status: tools.Denied, Code: "approval_denied"}},
		{ID: "e3", Tool: "run_command", CallID: "c", Result: tools.Result{Status: tools.Skipped, Code: "step_limit"}},
		{ID: "e4", Tool: "run_command", CallID: "d", StartedAt: now, FinishedAt: now.Add(time.Second), Result: tools.Result{Status: tools.Failed, Code: "cleanup_failed"}},
	} {
		p.OnEvent(agent.Event{Kind: agent.ToolFinished, Evidence: &item})
	}
	got := out.String()
	for _, want := range []string{"Model purpose: 检查状态", `"sh" "-c" "exit 7"`, "Its output will be sent to the model.", "Duration: 250ms", "Exit code: 7", "record e1", "denied (approval_denied)", "skipped (step_limit)", "failed (cleanup_failed)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Count(got, "Not executed; no host observation") != 2 {
		t.Fatal(got)
	}
}
