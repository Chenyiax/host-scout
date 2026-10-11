package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"host-scout/internal/agent"
	"host-scout/internal/model"
	"host-scout/internal/policy"
	"host-scout/internal/providers/openairesponse"
	"host-scout/internal/tools/command"
	tools "host-scout/pkg/tool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type integrationApprover struct{ wait bool }

func (a integrationApprover) ApproveCommand(ctx context.Context, _ policy.CommandRequest) (bool, error) {
	if a.wait {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return true, nil
}

func commandResponse(arguments json.RawMessage) model.Response {
	return model.Response{Calls: []model.ToolCall{{Name: "run_command", CallID: "c1", Arguments: arguments}}}
}

func TestAgentCommandHelper(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "--agent-command-sleep" {
			time.Sleep(30 * time.Second)
			return
		}
	}
}

func TestCancellationDuringApprovalAndCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"argv": []string{exe, "-test.run=TestAgentCommandHelper", "--", "--agent-command-sleep"}})
	for _, duringApproval := range []bool{true, false} {
		t.Run(map[bool]string{true: "approval", false: "command"}[duringApproval], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
				return commandResponse(args), nil
			}}
			cmd, err := command.New(integrationApprover{wait: duringApproval})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := tools.NewRegistry(cmd)
			if err != nil {
				t.Fatal(err)
			}
			s, err := agent.New(client, registry, 0)
			if err != nil {
				t.Fatal(err)
			}
			var events []agent.EventKind
			busyAtFinish := false
			start := time.Now()
			err = s.Ask(ctx, "run", agent.Hooks{OnEvent: func(e agent.Event) {
				events = append(events, e.Kind)
				if duringApproval && e.Kind == agent.ApprovalRequested {
					cancel()
				}
				if !duringApproval && e.Kind == agent.ToolStarted {
					time.AfterFunc(30*time.Millisecond, cancel)
				}
				if e.Kind == agent.TurnFinished {
					busyAtFinish = s.Busy()
				}
			}})
			if !errors.Is(err, context.Canceled) || len(client.requests) != 1 || len(s.Evidence()) != 1 ||
				s.Evidence()[0].Result.Status != tools.Cancelled || events[len(events)-1] != agent.TurnFinished || !busyAtFinish || s.Busy() || time.Since(start) > 2*time.Second {
				t.Fatalf("cancellation failed: %v events=%v records=%+v elapsed=%v", err, events, s.Evidence(), time.Since(start))
			}
			started := false
			for _, event := range events {
				if event == agent.ToolStarted {
					started = true
				}
			}
			if duringApproval && started || !duringApproval && !started {
				t.Fatalf("wrong start event: %v", events)
			}
		})
	}
}

func TestCancellationDuringModelRetryStopsBeforeAnotherRequest(t *testing.T) {
	first := make(chan struct{})
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			close(first)
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"unexpected"}]}]}}`+"\n\n")
	}))
	defer server.Close()
	registry, _ := tools.NewRegistry()
	s, err := agent.New(&openairesponse.Client{BaseURL: server.URL, Model: "test", APIKey: "secret"}, registry, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Ask(ctx, "question", agent.Hooks{}) }()
	<-first
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		var modelErr *model.Error
		if !errors.As(err, &modelErr) || modelErr.Kind != model.Cancelled || attempts != 1 || time.Since(start) > time.Second || len(s.History()) != 1 {
			t.Fatalf("retry cancellation: %v attempts=%d history=%+v", err, attempts, s.History())
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
	if strings.Contains(strings.ToLower(s.History()[0].Text), "unexpected") {
		t.Fatal("failed model result committed")
	}
}
