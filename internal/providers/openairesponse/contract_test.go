package openairesponse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"host-scout/internal/model"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const completedText = `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}]}}`

func assertKind(t *testing.T, err error, kind model.ErrorKind) {
	t.Helper()
	var e *model.Error
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("got %v want %s", err, kind)
	}
}
func TestSSEFramingAndTerminalBoundary(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("%q", newline), func(t *testing.T) {
			stream := strings.Join([]string{
				": comment", "event: ignored", "id: 1", `data: {"type":"unknown.nonterminal"}`, "",
				`data: {"type":"response.refusal.delta","delta":"n"}`, "",
				`data: {"type":"response.refusal.delta","delta":"o"}`, "",
				`data: {"type":"response.function_call_arguments.delta","delta":"untrusted partial args"}`, "",
				`data: {"type":"response.completed",`,
				`data: "response":{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}}`, "", "",
			}, newline)
			var text string
			result, err := decodeStream(strings.NewReader(stream), func(s string) { text += s })
			if err != nil || result.Text != "no" || text != "no" || len(result.Calls) != 0 {
				t.Fatalf("%+v %v %q", result, err, text)
			}
		})
	}
	// An indefinitely open SSE connection must not delay a completed response.
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := decodeStream(pr, nil); done <- err }()
	_, _ = io.WriteString(pw, "data: "+completedText+"\n\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waited for EOF")
	}
	_ = pr.Close()
	_ = pw.Close()
}
func TestSSEProtocolBoundaries(t *testing.T) {
	for _, stream := range []string{
		"data: [DONE]\n\n",
		`data: {"type":"response.function_call_arguments.delta","delta":"{}"}` + "\n\ndata: [DONE]\n\n",
		"data: " + completedText,
		"data: not-json\n\n",
		`data: {"type":"response.completed","response":{"status":"incomplete","output":[]}}` + "\n\n",
		"data: " + strings.Repeat("x", maxEventBytes+1) + "\n\n",
		"data: " + strings.Repeat("x", maxEventBytes/2) + "\ndata: " + strings.Repeat("x", maxEventBytes/2) + "\n\n",
	} {
		result, err := decodeStream(strings.NewReader(stream), nil)
		assertKind(t, err, model.Protocol)
		if len(result.Items) != 0 || len(result.Calls) != 0 {
			t.Fatal("partial response returned")
		}
	}
	// Exactly 8 MiB data is allowed, including the newline between data fields.
	prefix := `{"type":"unknown","padding":"`
	payload := prefix + strings.Repeat("x", maxEventBytes-len(prefix)-2) + `"}`
	_, err := decodeStream(strings.NewReader("data: "+payload+"\n\ndata: "+completedText+"\n\n"), nil)
	if err != nil {
		t.Fatalf("exact limit: %v", err)
	}
}
func TestItemOwnershipAndRawReplay(t *testing.T) {
	raw := `{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs","encrypted_content":"opaque"},{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"thinking"}]},{"type":"function_call","id":"fc1","call_id":"c1","name":"a","arguments":"{}"},{"type":"function_call","id":"fc2","call_id":"c2","name":"b","arguments":"{}"}]}}`
	result, err := decodeStream(strings.NewReader("data: "+raw+"\n\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 || len(result.Calls) != 2 || result.Items[2].Kind != model.ToolCallItem {
		t.Fatalf("%+v", result)
	}
	for i, item := range result.Items {
		encoded, err := encodeItem(item)
		if err != nil || string(encoded) != string(item.ProviderData) {
			t.Fatalf("item %d lost raw fields: %s %v", i, encoded, err)
		}
	}
	for _, item := range []model.Item{
		{Kind: model.User, Text: "hello"}, {Kind: model.Assistant, Text: "hi"},
		{Kind: model.ToolCallItem, Name: "a", CallID: "c", Arguments: json.RawMessage("{}")},
		{Kind: model.ToolResultItem, CallID: "c", Text: "result"},
	} {
		if _, err := encodeItem(item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := encodeItem(model.Item{Kind: model.Opaque, ProviderID: "other", ProviderData: json.RawMessage("{}")}); err == nil {
		t.Fatal("foreign payload accepted")
	}
}
func TestHTTPContractAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		media, stream string
		kind          model.ErrorKind
	}{
		{"http", 401, "application/json", "secret-key", model.HTTP},
		{"redirect", 307, "text/event-stream", "", model.HTTP},
		{"non-sse", 200, "application/json", completedText, model.Protocol},
		{"truncated", 200, "text/event-stream", "data: " + completedText, model.Protocol},
		{"error", 200, "text/event-stream", `data: {"type":"error","message":"secret-key"}` + "\n\n", model.RemoteFailed},
		{"failed", 200, "text/event-stream", `data: {"type":"response.failed"}` + "\n\n", model.RemoteFailed},
		{"incomplete", 200, "text/event-stream", `data: {"type":"response.incomplete"}` + "\n\n", model.Incomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer secret-key" || r.Header.Get("Accept") != "text/event-stream" {
					t.Error("request contract")
				}
				var request map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&request)
				var defs []struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
				}
				_ = json.Unmarshal(request["tools"], &defs)
				if len(defs) != 1 || defs[0].Type != "function" || defs[0].Name != "probe" || !defs[0].Strict || string(request["model"]) != `"test"` || string(request["instructions"]) != `"policy"` {
					t.Error("model/instructions/function definitions contract")
				}
				for k, v := range map[string]string{"stream": "true", "store": "false", "parallel_tool_calls": "false"} {
					if string(request[k]) != v {
						t.Errorf("%s=%s", k, request[k])
					}
				}
				if strings.Contains(string(request["input"]), "secret-key") {
					t.Error("key in model history")
				}
				w.Header().Set("Content-Type", tc.media)
				w.Header().Set("Location", "/v1/responses")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.stream)
			}))
			defer server.Close()
			client := Client{BaseURL: server.URL + "/v1", APIKey: "secret-key", Model: "test", HTTPClient: server.Client()}
			result, err := client.Create(context.Background(), model.Request{Instructions: "policy", Items: []model.Item{{Kind: model.User, Text: "hello"}}, Tools: []model.ToolDefinition{{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}}}, nil)
			assertKind(t, err, tc.kind)
			if strings.Contains(err.Error(), "secret-key") || count.Load() != 1 || len(result.Calls) != 0 || len(result.Items) != 0 {
				t.Fatalf("unsafe/partial/retried response: %+v %v %d", result, err, count.Load())
			}
		})
	}
}
func TestHTTPCancelAndCompletionWithoutEOF(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			connected := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if complete {
					fmt.Fprint(w, "data: "+completedText+"\n\n")
				} else {
					fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"partial"}`+"\n\n")
				}
				w.(http.Flusher).Flush()
				close(connected)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				result, err := (&Client{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}).Create(ctx, model.Request{}, nil)
				if !complete && len(result.Items) > 0 {
					t.Error("partial history")
				}
				done <- err
			}()
			<-connected
			if !complete {
				cancel()
			}
			select {
			case err := <-done:
				if complete && err != nil {
					t.Fatal(err)
				}
				if !complete {
					assertKind(t, err, model.Cancelled)
					if !errors.Is(err, context.Canceled) {
						t.Fatal("lost cancellation cause")
					}
				}
			case <-time.After(time.Second):
				t.Fatal("request did not return")
			}
		})
	}
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	_, err := (&Client{BaseURL: url}).Create(context.Background(), model.Request{}, nil)
	assertKind(t, err, model.Transport)
}
