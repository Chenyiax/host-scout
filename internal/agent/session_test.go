package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"host-scout/internal/agent"
	"host-scout/internal/model"
	"host-scout/internal/providers/openairesponse"
	tools "host-scout/pkg/tool"
)

type sampleTool struct{}

func (sampleTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "sample", Description: "Return a sample diagnostic value.",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
	}
}

func (sampleTool) Run(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
	tools.Started(ctx)
	return tools.Value(map[string]int{"value": 42}), nil
}

func TestResponsesToolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var inputs []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "wrong key", http.StatusUnauthorized)
			return
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		mu.Lock()
		inputs = append(inputs, request)
		turn := len(inputs)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"sample\",\"arguments\":\"{}\"}]}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"The value is 42.\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"The value is 42.\"}]}]}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	registry, err := tools.NewRegistry(sampleTool{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.New(&openairesponse.Client{
		BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "test-key", HTTPClient: server.Client(),
	}, registry, 4)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	if err := session.Ask(context.Background(), "What is the value?", agent.Hooks{OnText: func(delta string) { text.WriteString(delta) }}); err != nil {
		t.Fatal(err)
	}
	if text.String() != "The value is 42." {
		t.Fatalf("unexpected streamed answer: %q", text.String())
	}
	evidence := session.Evidence()
	if len(evidence) != 1 || evidence[0].ID != "evidence-001" || !strings.Contains(evidence[0].Output, "42") {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inputs) != 2 {
		t.Fatalf("wanted 2 API calls, got %d", len(inputs))
	}
	if string(inputs[0]["store"]) != "false" || string(inputs[0]["stream"]) != "true" {
		t.Fatalf("unexpected request options: %s", inputs[0])
	}
	var continued []map[string]json.RawMessage
	if err := json.Unmarshal(inputs[1]["input"], &continued); err != nil {
		t.Fatal(err)
	}
	if len(continued) != 3 || string(continued[1]["type"]) != `"function_call"` || string(continued[2]["call_id"]) != `"call_1"` {
		t.Fatalf("function call was not continued correctly: %s", inputs[1]["input"])
	}
}

func TestResponsesMultipleCallsReplayAllOutputItems(t *testing.T) {
	var inputs []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		inputs = append(inputs, request.Input)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(inputs) == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"opaque\"},{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"sample\",\"arguments\":\"{}\"},{\"type\":\"function_call\",\"id\":\"fc_2\",\"call_id\":\"call_2\",\"name\":\"sample\",\"arguments\":\"{}\"},{\"type\":\"message\",\"phase\":\"commentary\",\"content\":[{\"type\":\"output_text\",\"text\":\"Checking twice.\"}]}]}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Both observations are 42.\"}]}]}}\n\n")
	}))
	defer server.Close()

	registry, err := tools.NewRegistry(sampleTool{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := agent.New(&openairesponse.Client{BaseURL: server.URL, Model: "test", APIKey: "test", HTTPClient: server.Client()}, registry, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "Check twice", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if len(session.Evidence()) != 2 || len(inputs) != 2 {
		t.Fatalf("expected two tool results and two requests, got %d and %d", len(session.Evidence()), len(inputs))
	}
	var continued []map[string]json.RawMessage
	if err := json.Unmarshal(inputs[1], &continued); err != nil {
		t.Fatal(err)
	}
	if len(continued) != 7 || string(continued[1]["type"]) != `"reasoning"` || string(continued[4]["phase"]) != `"commentary"` || string(continued[5]["call_id"]) != `"call_1"` || string(continued[6]["call_id"]) != `"call_2"` {
		t.Fatalf("unexpected continuation: %s", inputs[1])
	}
	if err := session.Ask(context.Background(), "What did you find?", agent.Hooks{}); err != nil {
		t.Fatal(err)
	}
	var nextTurn []map[string]json.RawMessage
	if err := json.Unmarshal(inputs[2], &nextTurn); err != nil {
		t.Fatal(err)
	}
	if len(nextTurn) != 9 || string(nextTurn[7]["type"]) != `"message"` || string(nextTurn[8]["role"]) != `"user"` {
		t.Fatalf("conversation history was not replayed: %s", inputs[2])
	}
}
