// Package agent advances a single sequential Turn, owns in-memory history,
// dispatches tools and passes cancellation through the current operation.
// Checkpoints commit accepted users, validated complete responses and each
// terminal tool result; partial model responses never commit. A zero step
// limit means no count limit, while a positive limit counts Client.Create calls
// per Turn rather than HTTP attempts. Hooks are synchronous and must not
// re-enter Ask. The agent does not parse HTTP, read terminal input, enforce a
// diagnostic workflow or plan a multi-stage investigation. No provider
// switching or replay occurs within a conversation. A context-limit error
// preserves history and tool results; only an explicit Clear drops them.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"host-scout/internal/model"
	"host-scout/internal/policy"
	tools "host-scout/pkg/tool"
	"io"
	"strings"
	"sync"
	"time"
)

var ErrBusy = errors.New("session is busy")
var ErrStepLimit = errors.New("model step limit reached")
var ErrCleanupFailed = errors.New("command cleanup failed; managed processes may remain")

type Options struct{ Instructions string }
type Evidence struct {
	ID                 string
	CallID             string
	Tool               string
	Arguments          json.RawMessage
	ArgumentsTruncated bool
	CreatedAt          time.Time
	StartedAt          time.Time
	FinishedAt         time.Time
	Result             tools.Result
	Output             string
}
type EventKind string

const (
	TurnStarted       EventKind = "turn_started"
	ModelRequested    EventKind = "model_requested"
	TextDelta         EventKind = "text_delta"
	ToolProposed      EventKind = "tool_proposed"
	ApprovalRequested EventKind = "approval_requested"
	ApprovalResolved  EventKind = "approval_resolved"
	ToolStarted       EventKind = "tool_started"
	ToolFinished      EventKind = "tool_finished"
	TurnFinished      EventKind = "turn_finished"
)

type Event struct {
	Kind     EventKind
	Text     string
	CallID   string
	Tool     string
	Argv     []string
	Purpose  string
	Step     int
	Approved bool
	Evidence *Evidence
	Err      error
}
type Hooks struct {
	OnEvent func(Event)
	OnText  func(string)
	OnTool  func(Evidence)
}
type Session struct {
	client       model.Client
	tools        *tools.Registry
	maxSteps     int
	instructions string
	mu           sync.Mutex
	busy         bool
	history      []model.Item
	evidence     []Evidence
	nextID       uint64
}

func New(client model.Client, registry *tools.Registry, maxSteps int, options ...Options) (*Session, error) {
	if client == nil || registry == nil || maxSteps < 0 {
		return nil, errors.New("agent requires a model client, tools and a non-negative step limit")
	}
	s := &Session{client: client, tools: registry, maxSteps: maxSteps}
	if len(options) > 0 {
		s.instructions = options[0].Instructions
	}
	return s, nil
}
func (s *Session) Ask(ctx context.Context, question string, hooks Hooks) (err error) {
	if strings.TrimSpace(question) == "" {
		return errors.New("question is empty")
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return ErrBusy
	}
	s.busy = true
	s.history = append(s.history, model.Item{Kind: model.User, Text: question})
	s.mu.Unlock()
	emit := func(event Event) {
		var toolRecord Evidence
		if event.Evidence != nil {
			toolRecord = cloneEvidence(*event.Evidence)
		}
		if hooks.OnEvent != nil {
			hooks.OnEvent(event)
		}
		if event.Kind == TextDelta && hooks.OnText != nil {
			hooks.OnText(event.Text)
		}
		if event.Kind == ToolFinished && hooks.OnTool != nil {
			hooks.OnTool(toolRecord)
		}
	}
	// Busy remains set until turn_finished has been delivered.
	defer func() { emit(Event{Kind: TurnFinished, Err: err}); s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
	emit(Event{Kind: TurnStarted})
	for step := 0; s.maxSteps == 0 || step < s.maxSteps; step++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		emit(Event{Kind: ModelRequested, Step: step + 1})
		streamed := false
		response, requestErr := s.client.Create(ctx, model.Request{Instructions: s.instructions, Items: s.History(), Tools: s.tools.Definitions()}, func(delta string) {
			if delta != "" {
				streamed = true
				emit(Event{Kind: TextDelta, Text: delta})
			}
		})
		if requestErr != nil {
			return requestErr
		}
		// Validation is atomic for the whole batch, before committing or executing.
		response, requestErr = normalize(response)
		if requestErr != nil {
			return requestErr
		}
		seen := make(map[string]bool)
		for _, item := range s.History() {
			if item.Kind == model.ToolCallItem {
				seen[item.CallID] = true
			}
		}
		for _, call := range response.Calls {
			if strings.TrimSpace(call.Name) == "" || strings.TrimSpace(call.CallID) == "" || seen[call.CallID] {
				return &model.Error{Kind: model.Protocol, Detail: "invalid or duplicate tool call name/ID"}
			}
			seen[call.CallID] = true
		}
		if len(response.Calls) == 0 && response.Text == "" {
			return errors.New("model returned neither text nor tool calls")
		}
		s.mu.Lock()
		s.history = append(s.history, model.CloneItems(response.Items)...)
		s.mu.Unlock()
		if !streamed && response.Text != "" {
			emit(Event{Kind: TextDelta, Text: response.Text})
		}
		for _, call := range response.Calls {
			emit(Event{Kind: ToolProposed, Tool: call.Name, CallID: call.CallID})
		}
		var stopErr error
		if ctx.Err() != nil {
			stopErr = ctx.Err()
		} else if s.maxSteps > 0 && step == s.maxSteps-1 && len(response.Calls) > 0 {
			stopErr = ErrStepLimit
		}
		for _, call := range response.Calls {
			created := time.Now()
			var started time.Time
			var result tools.Result
			if ctx.Err() != nil && stopErr == nil {
				stopErr = ctx.Err()
			}
			switch {
			case errors.Is(stopErr, ErrStepLimit):
				result = tools.Result{Status: tools.Skipped, Code: "step_limit"}
			case errors.Is(stopErr, ErrCleanupFailed):
				result = tools.Result{Status: tools.Skipped, Code: "cleanup_failed"}
			case stopErr != nil:
				result = tools.Result{Status: tools.Cancelled, Code: "cancelled"}
			default:
				callCtx := tools.WithStart(ctx, func() {
					if started.IsZero() {
						started = time.Now()
						emit(Event{Kind: ToolStarted, Tool: call.Name, CallID: call.CallID})
					}
				})
				callCtx = policy.WithObserver(callCtx, policy.Observer{
					Requested: func(r policy.CommandRequest) {
						emit(Event{Kind: ApprovalRequested, Tool: call.Name, CallID: call.CallID, Argv: append([]string(nil), r.Argv...), Purpose: r.Purpose})
					},
					Resolved: func(approved bool) {
						emit(Event{Kind: ApprovalResolved, Tool: call.Name, CallID: call.CallID, Approved: approved})
					},
				})
				result, requestErr = s.runTool(callCtx, call)
				if requestErr != nil {
					stopErr = requestErr
				}
			}
			record := s.record(call, result, created, started)
			copyRecord := cloneEvidence(record)
			emit(Event{Kind: ToolFinished, Tool: call.Name, CallID: call.CallID, Evidence: &copyRecord})
		}
		if stopErr != nil {
			return stopErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(response.Calls) == 0 {
			return nil
		}
	}
	return ErrStepLimit
}

// Calls are an execution view of Items, not a second history representation.
// Clients may supply generic Calls/Text only; the runtime materializes items.
func normalize(r model.Response) (model.Response, error) {
	r.Items = model.CloneItems(r.Items)
	var itemCalls []model.ToolCall
	var itemText string
	hasAssistant := false
	for _, item := range r.Items {
		if item.Kind != model.Assistant && item.Kind != model.ToolCallItem && item.Kind != model.Opaque {
			return r, &model.Error{Kind: model.Protocol, Detail: "invalid model output item kind"}
		}
		if item.Kind == model.ToolCallItem {
			itemCalls = append(itemCalls, model.ToolCall{Name: item.Name, CallID: item.CallID, Arguments: item.Arguments})
		}
		if item.Kind == model.Assistant {
			hasAssistant = true
			itemText += item.Text
		}
	}
	if len(itemCalls) > 0 {
		if len(r.Calls) == 0 {
			r.Calls = itemCalls
		} else {
			if len(itemCalls) != len(r.Calls) {
				return r, &model.Error{Kind: model.Protocol, Detail: "tool call items and execution view differ"}
			}
			for i, c := range itemCalls {
				v := r.Calls[i]
				if c.Name != v.Name || c.CallID != v.CallID || string(c.Arguments) != string(v.Arguments) {
					return r, &model.Error{Kind: model.Protocol, Detail: "tool call items and execution view differ"}
				}
			}
		}
	} else {
		for _, c := range r.Calls {
			r.Items = append(r.Items, model.Item{Kind: model.ToolCallItem, Name: c.Name, CallID: c.CallID, Arguments: append(json.RawMessage(nil), c.Arguments...)})
		}
	}
	if r.Text == "" {
		r.Text = itemText
	}
	if hasAssistant && r.Text != itemText {
		return r, &model.Error{Kind: model.Protocol, Detail: "assistant items and visible text differ"}
	}
	if !hasAssistant && r.Text != "" {
		r.Items = append(r.Items, model.Item{Kind: model.Assistant, Text: r.Text})
	}
	return r, nil
}
func (s *Session) runTool(ctx context.Context, call model.ToolCall) (tools.Result, error) {
	tool, ok := s.tools.Get(call.Name)
	if !ok {
		return tools.Result{Status: tools.Failed, Code: "unknown_tool"}, nil
	}
	result, err := tool.Run(ctx, append(json.RawMessage(nil), call.Arguments...))
	if result.Status == tools.Failed && result.Code == "cleanup_failed" {
		return result, ErrCleanupFailed
	}
	if err != nil {
		if ctx.Err() != nil {
			return tools.Result{Status: tools.Cancelled, Code: "cancelled"}, ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return tools.Result{Status: tools.Cancelled, Code: "input_eof"}, io.EOF
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tools.Result{Status: tools.Cancelled, Code: "cancelled"}, err
		}
		return tools.Result{Status: tools.Failed, Code: "tool_failed"}, nil
	}
	if ctx.Err() != nil {
		if result.Status != tools.Succeeded && result.Status != tools.TimedOut {
			result.Status = tools.Cancelled
			result.Code = "cancelled"
		}
		return result, ctx.Err()
	}
	if result.Status == tools.Cancelled {
		if result.Code == "input_eof" {
			return result, io.EOF
		}
		return result, context.Canceled
	}
	return result, nil
}
func (s *Session) record(call model.ToolCall, result tools.Result, created, started time.Time) Evidence {
	result = tools.Bound(result)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	encoded, err := json.Marshal(result)
	if err != nil {
		result = tools.Result{Status: tools.Failed, Code: "encode_result"}
		encoded, _ = json.Marshal(result)
	}
	const maxDisplayedArguments = 16 << 10
	arguments := append(json.RawMessage(nil), call.Arguments...)
	argumentsTruncated := len(arguments) > maxDisplayedArguments
	if argumentsTruncated {
		arguments = arguments[:maxDisplayedArguments]
	}
	e := Evidence{ID: fmt.Sprintf("evidence-%03d", s.nextID), CallID: call.CallID, Tool: call.Name, Arguments: arguments, ArgumentsTruncated: argumentsTruncated, CreatedAt: created, StartedAt: started, FinishedAt: time.Now(), Result: tools.CloneResult(result), Output: string(encoded)}
	if len(e.Output) > 32<<10 {
		e.Output = e.Output[:32<<10] + "\n[record display truncated]"
	}
	s.evidence = append(s.evidence, e)
	feedback, _ := json.Marshal(struct {
		ID string `json:"record_id"`
		tools.Result
	}{e.ID, result})
	s.history = append(s.history, model.Item{Kind: model.ToolResultItem, CallID: call.CallID, Text: string(feedback)})
	return e
}
func cloneEvidence(e Evidence) Evidence {
	e.Result = tools.CloneResult(e.Result)
	e.Arguments = append(json.RawMessage(nil), e.Arguments...)
	return e
}
func (s *Session) Evidence() []Evidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := append([]Evidence(nil), s.evidence...)
	for i := range records {
		records[i] = cloneEvidence(records[i])
	}
	return records
}
func (s *Session) History() []model.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return model.CloneItems(s.history)
}
func (s *Session) Busy() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.busy }
func (s *Session) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return ErrBusy
	}
	s.history = nil
	s.evidence = nil
	return nil
}
