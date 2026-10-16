package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"host-scout/internal/agent"
	"host-scout/internal/model"
	"host-scout/internal/policy"
	"host-scout/internal/providers/openairesponse"
	tools "host-scout/pkg/tool"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeClient struct {
	requests []model.Request
	create   func(context.Context, model.Request, func(string)) (model.Response, error)
}

func (c *fakeClient) Create(ctx context.Context, r model.Request, onText func(string)) (model.Response, error) {
	c.requests = append(c.requests, r)
	return c.create(ctx, r, onText)
}

type probeTool struct {
	name string
	run  func(context.Context, json.RawMessage) (any, error)
}

func (p probeTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: p.name, Description: "Probe tool for tests", Parameters: json.RawMessage("{}")}
}
func (p probeTool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	value, err := p.run(ctx, args)
	if err != nil {
		return tools.Result{}, err
	}
	if result, ok := value.(tools.Result); ok {
		return result, nil
	}
	return tools.Value(value), nil
}
func makeSession(t *testing.T, c model.Client, max int, tool ...tools.Tool) *agent.Session {
	t.Helper()
	registry, err := tools.NewRegistry(tool...)
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.New(c, registry, max, agent.Options{Instructions: "injected interactive prompt"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}
func textResponse(text string) model.Response {
	return model.Response{Items: []model.Item{{Kind: model.Assistant, Text: text}}, Text: text}
}
func callsResponse(calls ...model.ToolCall) model.Response { return model.Response{Calls: calls} }
func call(id, name string) model.ToolCall {
	return model.ToolCall{CallID: id, Name: name, Arguments: json.RawMessage("{}")}
}
func eventKinds(events []agent.Event) []agent.EventKind {
	result := make([]agent.EventKind, len(events))
	for i, e := range events {
		result[i] = e.Kind
	}
	return result
}
func balanced(t *testing.T, history []model.Item) {
	t.Helper()
	calls := map[string]int{}
	results := map[string]int{}
	for _, item := range history {
		if item.Kind == model.ToolCallItem {
			calls[item.CallID]++
		}
		if item.Kind == model.ToolResultItem {
			results[item.CallID]++
			if !strings.Contains(item.Text, "record_id") {
				t.Error("missing record ID")
			}
		}
	}
	if !reflect.DeepEqual(calls, results) {
		t.Fatalf("unbalanced calls=%v results=%v", calls, results)
	}
	for _, n := range calls {
		if n != 1 {
			t.Fatal("duplicate call/result")
		}
	}
}
func TestGenericClientTwoTurnsAndToolRoundTrip(t *testing.T) {
	c := &fakeClient{}
	executed := 0
	c.create = func(_ context.Context, r model.Request, _ func(string)) (model.Response, error) {
		if r.Instructions != "injected interactive prompt" || len(r.Tools) != 1 || r.Tools[0].Name != "probe" {
			t.Fatal("narrow client contract")
		}
		for _, item := range r.Items {
			if len(item.ProviderData) > 0 {
				t.Fatal("Responses payload in generic client")
			}
		}
		switch len(c.requests) {
		case 1:
			return textResponse("what would you like to inspect?"), nil
		case 2:
			if len(r.Items) != 3 || r.Items[1].Kind != model.Assistant || r.Items[2].Text != "memory please" {
				t.Fatalf("history: %+v", r.Items)
			}
			return callsResponse(call("c1", "probe")), nil
		case 3:
			balanced(t, r.Items)
			return textResponse("found 42"), nil
		}
		t.Fatal("extra request")
		return model.Response{}, nil
	}
	s := makeSession(t, c, 3, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		tools.Started(ctx)
		executed++
		return 42, nil
	}})
	var events []agent.Event
	if err := s.Ask(context.Background(), "hello", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(context.Background(), "memory please", agent.Hooks{OnEvent: func(e agent.Event) { events = append(events, e) }}); err != nil {
		t.Fatal(err)
	}
	if executed != 1 || len(c.requests) != 3 {
		t.Fatal("unexpected execution count")
	}
	want := []agent.EventKind{agent.TurnStarted, agent.ModelRequested, agent.ToolProposed, agent.ToolStarted, agent.ToolFinished, agent.ModelRequested, agent.TextDelta, agent.TurnFinished}
	if !reflect.DeepEqual(eventKinds(events), want) {
		t.Fatalf("events %v", eventKinds(events))
	}
	if err := s.Ask(context.Background(), "  ", agent.Hooks{OnEvent: func(agent.Event) { t.Fatal("empty turn event") }}); err == nil {
		t.Fatal("empty accepted")
	}
}
func TestFailedModelHistoryCheckpoints(t *testing.T) {
	c := &fakeClient{}
	n := 0
	c.create = func(_ context.Context, r model.Request, onText func(string)) (model.Response, error) {
		n++
		switch n {
		case 1:
			onText("unfinished")
			return textResponse("must not commit"), errors.New("transport")
		case 2:
			if len(r.Items) != 2 || r.Items[0].Text != "first" {
				t.Fatalf("lost accepted input %+v", r.Items)
			}
			return callsResponse(call("c1", "probe")), nil
		case 3:
			return model.Response{}, errors.New("after tool failure")
		case 4:
			balanced(t, r.Items)
			return textResponse("continued"), nil
		}
		return model.Response{}, nil
	}
	executions := 0
	s := makeSession(t, c, 3, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		tools.Started(ctx)
		executions++
		return 1, nil
	}})
	if err := s.Ask(context.Background(), "first", agent.Hooks{}); err == nil {
		t.Fatal("failure expected")
	}
	if len(s.History()) != 1 {
		t.Fatal("partial model committed")
	}
	if err := s.Ask(context.Background(), "second", agent.Hooks{}); err == nil {
		t.Fatal("later failure expected")
	}
	if len(s.Evidence()) != 1 {
		t.Fatal("completed tool lost")
	}
	if err := s.Ask(context.Background(), "follow up", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatal("reran tool")
	}
}
func TestBatchValidationBeforeCommitOrExecution(t *testing.T) {
	for _, batch := range [][]model.ToolCall{
		{call("good", "probe"), call("", "probe")},
		{call("good", "probe"), call("bad", "")},
		{call("same", "probe"), call("same", "probe")},
	} {
		c := &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
			return callsResponse(batch...), nil
		}}
		s := makeSession(t, c, 3, probeTool{"probe", func(context.Context, json.RawMessage) (any, error) { t.Fatal("partial execution"); return nil, nil }})
		if err := s.Ask(context.Background(), "test", agent.Hooks{}); err == nil {
			t.Fatal("bad batch accepted")
		}
		if len(s.History()) != 1 || len(s.Evidence()) != 0 {
			t.Fatal("invalid response committed")
		}
	}
	c := &fakeClient{}
	c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
		switch len(c.requests) {
		case 1, 3:
			return callsResponse(call("same", "probe")), nil
		default:
			return textResponse("done"), nil
		}
	}
	executed := 0
	s := makeSession(t, c, 3, probeTool{"probe", func(context.Context, json.RawMessage) (any, error) { executed++; return 1, nil }})
	if err := s.Ask(context.Background(), "one", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	before := len(s.History())
	if err := s.Ask(context.Background(), "two", agent.Hooks{}); err == nil {
		t.Fatal("historical ID reused")
	}
	if executed != 1 || len(s.History()) != before+1 {
		t.Fatal("conflicting response committed")
	}
}

type decision struct {
	allow bool
	err   error
}

func (d decision) ApproveCommand(context.Context, policy.CommandRequest) (bool, error) {
	return d.allow, d.err
}
func TestApprovalAndOrdinaryFailureEvents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		allow  bool
		err    error
		status tools.Status
		start  bool
	}{
		{"approved", true, nil, tools.Succeeded, true},
		{"denied", false, nil, tools.Denied, false},
		{"failed", true, nil, tools.Failed, true},
		{"EOF", false, io.EOF, tools.Cancelled, false},
		{"cancel", false, context.Canceled, tools.Cancelled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{}
			c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
				if len(c.requests) == 1 {
					return callsResponse(call("c", "command")), nil
				}
				return textResponse("explained"), nil
			}
			tool := probeTool{"command", func(ctx context.Context, _ json.RawMessage) (any, error) {
				allow, err := policy.Approve(ctx, decision{tc.allow, tc.err}, policy.CommandRequest{Argv: []string{"exact", "argv"}})
				if err != nil {
					return nil, err
				}
				if !allow {
					return tools.Result{Status: tools.Denied, Code: "denied"}, nil
				}
				tools.Started(ctx)
				return tools.Result{Status: tc.status}, nil
			}}
			s := makeSession(t, c, 3, tool)
			var events []agent.Event
			err := s.Ask(context.Background(), "test", agent.Hooks{OnEvent: func(e agent.Event) { events = append(events, e) }})
			if tc.err != nil && err == nil {
				t.Fatal("approval cancellation should stop")
			}
			if s.Evidence()[0].Result.Status != tc.status {
				t.Fatalf("%+v", s.Evidence())
			}
			want := []agent.EventKind{agent.TurnStarted, agent.ModelRequested, agent.ToolProposed, agent.ApprovalRequested}
			if tc.err == nil {
				want = append(want, agent.ApprovalResolved)
			}
			if tc.start {
				want = append(want, agent.ToolStarted)
			}
			want = append(want, agent.ToolFinished)
			if tc.err == nil {
				want = append(want, agent.ModelRequested, agent.TextDelta)
			}
			want = append(want, agent.TurnFinished)
			if !reflect.DeepEqual(eventKinds(events), want) {
				t.Fatalf("%v want %v", eventKinds(events), want)
			}
			balanced(t, s.History())
		})
	}
}
func TestUnknownInvalidAndEmptyResponse(t *testing.T) {
	c := &fakeClient{}
	c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
		if len(c.requests) == 1 {
			return callsResponse(call("u", "inspect_system"), call("a", "probe")), nil
		}
		balanced(t, c.requests[len(c.requests)-1].Items)
		return textResponse("try another path"), nil
	}
	s := makeSession(t, c, 3, probeTool{"probe", func(context.Context, json.RawMessage) (any, error) {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}})
	if err := s.Ask(context.Background(), "test", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if len(s.Evidence()) != 2 || s.Evidence()[0].Result.Code != "unknown_tool" || !s.Evidence()[1].StartedAt.IsZero() {
		t.Fatal("bad failure records")
	}
	c = &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
		return model.Response{}, nil
	}}
	s = makeSession(t, c, 3)
	var events []agent.Event
	if err := s.Ask(context.Background(), "empty", agent.Hooks{OnEvent: func(e agent.Event) { events = append(events, e) }}); err == nil {
		t.Fatal("empty response accepted")
	}
	if len(c.requests) != 1 || len(events) != 3 {
		t.Fatal("extra request or missing terminal event")
	}
}
func TestStepLimitAndCancellationBalancePendingCalls(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancel", false: "limit"}[cancelled], func(t *testing.T) {
			c := &fakeClient{}
			c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
				if len(c.requests) == 1 {
					return callsResponse(call("a", "probe"), call("b", "probe"), call("c", "probe")), nil
				}
				balanced(t, c.requests[len(c.requests)-1].Items)
				return textResponse("new turn"), nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executed := 0
			steps := 1
			if cancelled {
				steps = 3
			}
			s := makeSession(t, c, steps, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
				tools.Started(ctx)
				executed++
				cancel()
				return tools.Result{Status: tools.Succeeded}, nil
			}})
			var events []agent.Event
			err := s.Ask(ctx, "test", agent.Hooks{OnEvent: func(e agent.Event) { events = append(events, e) }})
			if cancelled && !errors.Is(err, context.Canceled) || !cancelled && !errors.Is(err, agent.ErrStepLimit) {
				t.Fatal(err)
			}
			if len(s.Evidence()) != 3 {
				t.Fatal("pending calls not recorded")
			}
			finishes := 0
			for _, e := range events {
				if e.Kind == agent.ToolFinished {
					finishes++
				}
			}
			if finishes != 3 || events[len(events)-1].Kind != agent.TurnFinished {
				t.Fatal("terminal events")
			}
			if cancelled {
				if executed != 1 || s.Evidence()[0].Result.Status != tools.Succeeded || s.Evidence()[1].Result.Status != tools.Cancelled {
					t.Fatal("cancel statuses")
				}
			} else if executed != 0 || s.Evidence()[0].Result.Code != "step_limit" {
				t.Fatal("limit executed tool")
			}
			balanced(t, s.History())
			if err := s.Ask(context.Background(), "follow up", agent.Hooks{}); err != nil {
				t.Fatal(err)
			}
			if executed > 1 {
				t.Fatal("automatic rerun")
			}
		})
	}
}

func TestOptionalStepLimitAndLongSequentialTurn(t *testing.T) {
	registry, err := tools.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.New(&fakeClient{}, registry, -1); err == nil {
		t.Fatal("negative step limit accepted")
	}
	one := &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
		return textResponse("final text"), nil
	}}
	if err := makeSession(t, one, 1).Ask(context.Background(), "question", agent.Hooks{}); err != nil || len(one.requests) != 1 {
		t.Fatalf("last allowed text was rejected: %v", err)
	}

	c := &fakeClient{}
	c.create = func(_ context.Context, req model.Request, _ func(string)) (model.Response, error) {
		step := len(c.requests)
		if step <= 9 {
			if step > 1 {
				balanced(t, req.Items)
			}
			response := callsResponse(call("call-"+strconv.Itoa(step), "probe"))
			if step == 5 {
				response.Text = "continuing"
			}
			return response, nil
		}
		if step == 10 {
			return textResponse("Which PID?"), nil
		}
		balanced(t, req.Items)
		return textResponse("Thanks for the clarification."), nil
	}
	executed := 0
	s := makeSession(t, c, 0, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		tools.Started(ctx)
		executed++
		return tools.Value(executed), nil
	}})
	turns := 0
	if err := s.Ask(context.Background(), "investigate", agent.Hooks{OnEvent: func(e agent.Event) {
		if e.Kind == agent.TurnFinished {
			turns++
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if executed != 9 || len(c.requests) != 10 || turns != 1 || len(s.Evidence()) != 9 {
		t.Fatalf("long turn stopped early: executed=%d requests=%d turns=%d", executed, len(c.requests), turns)
	}
	if err := s.Ask(context.Background(), "PID 23", agent.Hooks{}); err != nil || len(c.requests) != 11 {
		t.Fatalf("clarification did not continue: %v requests=%d", err, len(c.requests))
	}
	if executed != 9 || c.requests[10].Items[len(c.requests[10].Items)-1].Text != "PID 23" {
		t.Fatal("follow-up replayed tools or lost history")
	}
}

func TestProviderRetriesCountAsOneAgentStep(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}]}}`+"\n\n")
	}))
	defer server.Close()
	executed := 0
	s := makeSession(t, &openairesponse.Client{BaseURL: server.URL, Model: "test", APIKey: "test"}, 1,
		probeTool{"probe", func(context.Context, json.RawMessage) (any, error) { executed++; return tools.Value("unexpected"), nil }})
	err := s.Ask(context.Background(), "question", agent.Hooks{})
	if !errors.Is(err, agent.ErrStepLimit) || attempts != 2 || executed != 0 || len(s.Evidence()) != 1 || s.Evidence()[0].Result.Code != "step_limit" {
		t.Fatalf("retry changed agent budget: %v attempts=%d executed=%d evidence=%+v", err, attempts, executed, s.Evidence())
	}
}

func TestPostToolModelRetryDoesNotReplayTool(t *testing.T) {
	attempts := 0
	var continuation []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, _ := io.ReadAll(r.Body)
		switch attempts {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}]}}`+"\n\n")
		case 2:
			continuation = body
			if !strings.Contains(string(body), "function_call_output") {
				t.Error("tool result missing from continuation")
			}
			w.WriteHeader(503)
		case 3:
			if string(body) != string(continuation) {
				t.Error("retry changed post-tool history")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`+"\n\n")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"next"}]}]}}`+"\n\n")
		}
	}))
	defer server.Close()
	executed := 0
	s := makeSession(t, &openairesponse.Client{BaseURL: server.URL, Model: "test", APIKey: "test"}, 0,
		probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
			tools.Started(ctx)
			executed++
			return tools.Value("observed"), nil
		}})
	if err := s.Ask(context.Background(), "first", agent.Hooks{}); err != nil || attempts != 3 || executed != 1 {
		t.Fatalf("post-tool retry: %v attempts=%d executed=%d", err, attempts, executed)
	}
	if err := s.Ask(context.Background(), "followup", agent.Hooks{}); err != nil || attempts != 4 || executed != 1 {
		t.Fatalf("follow-up replayed tool: %v attempts=%d executed=%d", err, attempts, executed)
	}
}

func TestCleanupFailureStopsTurnAndPairsRemainingCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeClient{}
	c.create = func(_ context.Context, req model.Request, _ func(string)) (model.Response, error) {
		if len(c.requests) == 1 {
			return callsResponse(call("a", "probe"), call("b", "probe"), call("c", "probe")), nil
		}
		balanced(t, req.Items)
		return textResponse("continue without replay"), nil
	}
	executed := 0
	s := makeSession(t, c, 0, probeTool{"probe", func(context.Context, json.RawMessage) (any, error) {
		executed++
		cancel()
		return tools.Result{Status: tools.Failed, Code: "cleanup_failed", TerminationReason: "cancelled", Output: "partial output"}, nil
	}})
	var events []agent.Event
	busyAtFinish := false
	err := s.Ask(ctx, "run", agent.Hooks{OnEvent: func(e agent.Event) {
		events = append(events, e)
		if e.Kind == agent.TurnFinished {
			busyAtFinish = s.Busy()
		}
	}})
	if !errors.Is(err, agent.ErrCleanupFailed) || !busyAtFinish || s.Busy() || executed != 1 || len(c.requests) != 1 {
		t.Fatalf("wrong turn termination: %v busy=%v executed=%d requests=%d", err, busyAtFinish, executed, len(c.requests))
	}
	records := s.Evidence()
	if len(records) != 3 || records[0].Result.Code != "cleanup_failed" || records[0].Result.TerminationReason != "cancelled" ||
		records[1].Result.Status != tools.Skipped || records[1].Result.Code != "cleanup_failed" || records[2].Result.Status != tools.Skipped {
		t.Fatalf("calls were not paired: %+v", records)
	}
	if events[len(events)-1].Kind != agent.TurnFinished || len(s.History()) != 7 {
		t.Fatalf("terminal event or history missing: events=%v history=%+v", eventKinds(events), s.History())
	}
	if err := s.Ask(context.Background(), "follow up", agent.Hooks{}); err != nil || executed != 1 || len(c.requests) != 2 {
		t.Fatalf("follow-up replayed tool: %v executed=%d requests=%d", err, executed, len(c.requests))
	}
}

func TestContextLimitAfterToolPreservesHistoryUntilExplicitClear(t *testing.T) {
	c := &fakeClient{}
	c.create = func(_ context.Context, req model.Request, _ func(string)) (model.Response, error) {
		switch len(c.requests) {
		case 1:
			return callsResponse(call("c1", "probe")), nil
		case 2:
			balanced(t, req.Items)
			return model.Response{}, &model.Error{Kind: model.ContextLimit, Detail: "context length exceeded"}
		default:
			if len(req.Items) != 1 || req.Items[0].Text != "fresh" {
				t.Fatalf("clear did not remove old history: %+v", req.Items)
			}
			return textResponse("new answer"), nil
		}
	}
	executed := 0
	s := makeSession(t, c, 0, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		tools.Started(ctx)
		executed++
		return tools.Value("observed"), nil
	}})
	err := s.Ask(context.Background(), "first", agent.Hooks{})
	var modelErr *model.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != model.ContextLimit || executed != 1 || len(c.requests) != 2 || len(s.History()) != 3 || len(s.Evidence()) != 1 {
		t.Fatalf("context failure lost facts: %v history=%+v records=%+v", err, s.History(), s.Evidence())
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(context.Background(), "fresh", agent.Hooks{}); err != nil || executed != 1 || len(c.requests) != 3 {
		t.Fatalf("clear did not recover: %v calls=%d requests=%d", err, executed, len(c.requests))
	}
}
func TestBusyClearAndImmutableSnapshots(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c := &fakeClient{}
	c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
		if len(c.requests) == 1 {
			close(entered)
			<-release
			return callsResponse(call("c", "probe")), nil
		}
		return model.Response{Items: []model.Item{{Kind: model.Assistant, Text: "done", ProviderID: "fake", ProviderData: json.RawMessage(`{"opaque":1}`)}}, Text: "done"}, nil
	}
	exit := 0
	s := makeSession(t, c, 3, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		tools.Started(ctx)
		return tools.Result{Status: tools.Succeeded, Argv: []string{"original"}, ExitCode: &exit, Data: json.RawMessage("{}")}, nil
	}})
	done := make(chan error, 1)
	go func() { done <- s.Ask(context.Background(), "first", agent.Hooks{}) }()
	<-entered
	before := s.History()
	if !s.Busy() || !errors.Is(s.Clear(), agent.ErrBusy) {
		t.Fatal("busy Clear succeeded")
	}
	if err := s.Ask(context.Background(), "second", agent.Hooks{OnEvent: func(agent.Event) { t.Error("busy emitted event") }}); !errors.Is(err, agent.ErrBusy) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.History()) {
		t.Fatal("busy mutated history")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	records := s.Evidence()
	records[0].Result.Argv[0] = "changed"
	*records[0].Result.ExitCode = 9
	records[0].Result.Data[0] = 'x'
	history := s.History()
	history[len(history)-1].ProviderData[0] = 'x'
	if s.Evidence()[0].Result.Argv[0] != "original" || *s.Evidence()[0].Result.ExitCode != 0 || s.History()[len(history)-1].ProviderData[0] != '{' {
		t.Fatal("mutable snapshot")
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	c.create = func(_ context.Context, r model.Request, _ func(string)) (model.Response, error) {
		if len(r.Items) != 1 {
			t.Error("history survived clear")
		}
		return textResponse("cleared"), nil
	}
	if err := s.Ask(context.Background(), "new", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if len(s.Evidence()) != 0 {
		t.Fatal("records survived clear")
	}
}

func TestEvidenceRetainsBoundedInvocationAfterModelFailure(t *testing.T) {
	arguments := json.RawMessage(`{"argv":["echo","hello"],"purpose":"check"}`)
	c := &fakeClient{}
	c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
		if len(c.requests) == 1 {
			return callsResponse(model.ToolCall{CallID: "cmd-1", Name: "probe", Arguments: arguments}), nil
		}
		return model.Response{}, &model.Error{Kind: model.Transport}
	}
	s := makeSession(t, c, 0, probeTool{"probe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		tools.Started(ctx)
		return tools.Value("observed"), nil
	}})
	if err := s.Ask(context.Background(), "inspect", agent.Hooks{}); err == nil {
		t.Fatal("expected model failure")
	}
	records := s.Evidence()
	if len(records) != 1 || string(records[0].Arguments) != string(arguments) || records[0].ArgumentsTruncated {
		t.Fatalf("record lost arguments: %+v", records)
	}
	records[0].Arguments[0] = 'x'
	if string(s.Evidence()[0].Arguments) != string(arguments) {
		t.Fatal("evidence snapshot mutated internal arguments")
	}
	if len(s.History()) != 3 || !strings.Contains(s.History()[2].Text, records[0].ID) {
		t.Fatal("record ID missing from model feedback")
	}

	limitClient := &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
		return callsResponse(model.ToolCall{CallID: "cmd-2", Name: "probe", Arguments: arguments}), nil
	}}
	limited := makeSession(t, limitClient, 1, probeTool{"probe", func(context.Context, json.RawMessage) (any, error) {
		t.Fatal("step-limited tool executed")
		return nil, nil
	}})
	if !errors.Is(limited.Ask(context.Background(), "inspect", agent.Hooks{}), agent.ErrStepLimit) || string(limited.Evidence()[0].Arguments) != string(arguments) {
		t.Fatal("skipped invocation lost arguments")
	}
}

// Keep the race detector checking independent snapshot reads during a turn.
func TestConcurrentSnapshots(t *testing.T) {
	c := &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
		return textResponse("ok"), nil
	}}
	s := makeSession(t, c, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = s.History()
			_ = s.Evidence()
			_ = s.Busy()
		}
	}()
	for i := 0; i < 20; i++ {
		if err := s.Ask(context.Background(), "hello", agent.Hooks{}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestCancelWhileSecondApprovalWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeClient{}
	c.create = func(context.Context, model.Request, func(string)) (model.Response, error) {
		if len(c.requests) == 1 {
			return callsResponse(call("first", "read"), call("second", "command"), call("third", "command")), nil
		}
		balanced(t, c.requests[len(c.requests)-1].Items)
		return textResponse("followup"), nil
	}
	reads, commands := 0, 0
	s := makeSession(t, c, 3,
		probeTool{"read", func(ctx context.Context, _ json.RawMessage) (any, error) { tools.Started(ctx); reads++; return 42, nil }},
		probeTool{"command", func(ctx context.Context, _ json.RawMessage) (any, error) {
			_, err := policy.Approve(ctx, waitForCancel{}, policy.CommandRequest{Argv: []string{"never"}})
			if err != nil {
				return nil, err
			}
			commands++
			return nil, nil
		}},
	)
	var events []agent.Event
	err := s.Ask(ctx, "test", agent.Hooks{OnEvent: func(e agent.Event) {
		events = append(events, e)
		if e.Kind == agent.ApprovalRequested {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) || reads != 1 || commands != 0 || len(c.requests) != 1 {
		t.Fatalf("%v reads=%d commands=%d requests=%d", err, reads, commands, len(c.requests))
	}
	records := s.Evidence()
	if len(records) != 3 || records[0].Result.Status != tools.Succeeded || records[1].Result.Status != tools.Cancelled || records[2].Result.Status != tools.Cancelled {
		t.Fatalf("%+v", records)
	}
	for _, e := range events {
		if e.Kind == agent.ApprovalResolved {
			t.Fatal("fabricated authorization decision")
		}
	}
	balanced(t, s.History())
	if err := s.Ask(context.Background(), "continue", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || commands != 0 {
		t.Fatal("reran cancelled batch")
	}
}

type waitForCancel struct{}

func (waitForCancel) ApproveCommand(ctx context.Context, _ policy.CommandRequest) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func TestCancellationWinsAndMalformedGenericResponseIsNotCommitted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
		cancel()
		return callsResponse(call("c", "never")), nil
	}}
	s := makeSession(t, c, 1)
	if err := s.Ask(ctx, "test", agent.Hooks{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Evidence()[0].Result.Status != tools.Cancelled {
		t.Fatal("cancel misreported as step limit")
	}
	c = &fakeClient{create: func(context.Context, model.Request, func(string)) (model.Response, error) {
		return model.Response{Items: []model.Item{{Kind: model.Assistant, Text: "one"}}, Text: "different"}, nil
	}}
	s = makeSession(t, c, 1)
	var modelErr *model.Error
	err := s.Ask(context.Background(), "test", agent.Hooks{})
	if !errors.As(err, &modelErr) || modelErr.Kind != model.Protocol || len(s.History()) != 1 {
		t.Fatal("invalid response committed")
	}
}
