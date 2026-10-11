package openairesponse

import (
	"context"
	"errors"
	"host-scout/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeStreamKeepsReasoningAndFunctionCall(t *testing.T) {
	stream := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"id\":\"rs_1\",\"encrypted_content\":\"opaque\"},{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"inspect_system\",\"arguments\":\"{}\"}]}}\n\n"
	result, err := decodeStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || len(result.Calls) != 1 {
		t.Fatalf("unexpected output: %#v", result)
	}
	if !strings.Contains(string(result.Items[0].ProviderData), `"encrypted_content":"opaque"`) {
		t.Fatalf("reasoning was not preserved: %s", result.Items[0].ProviderData)
	}
	if result.Calls[0].CallID != "call_1" || string(result.Calls[0].Arguments) != "{}" {
		t.Fatalf("function call was not decoded: %#v", result.Calls[0])
	}
}

func TestContextLimitUsesOnlyExplicitBoundedErrorCode(t *testing.T) {
	const secret = "test-secret-key"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   model.ErrorKind
	}{
		{"context", 400, `{"error":{"code":"context_length_exceeded","message":"` + secret + `"}}`, model.ContextLimit},
		{"bad request", 400, `{"error":{"code":"invalid_request_error","message":"` + secret + `"}}`, model.HTTP},
		{"oversized body", 400, strings.Repeat("x", maxErrorBytes) + `{"error":{"code":"context_length_exceeded"}}`, model.HTTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := Client{BaseURL: server.URL, Model: "test", APIKey: secret}
			response, err := client.Create(context.Background(), model.Request{}, nil)
			assertKind(t, err, tc.want)
			if strings.Contains(err.Error(), secret) || len(response.Items) != 0 || len(response.Calls) != 0 {
				t.Fatalf("unsafe error or partial response: %+v %v", response, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		stream string
		want   model.ErrorKind
	}{
		{"error event", `{"type":"error","error":{"code":"context_length_exceeded","message":"` + secret + `"}}`, model.ContextLimit},
		{"failed response", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}}}`, model.ContextLimit},
		{"ordinary failure", `{"type":"response.failed","response":{"error":{"code":"other"}}}`, model.RemoteFailed},
		{"output limit", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, model.Incomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := decodeStream(strings.NewReader("data: "+tc.stream+"\n\n"), nil)
			var modelErr *model.Error
			if !errors.As(err, &modelErr) || modelErr.Kind != tc.want || strings.Contains(err.Error(), secret) || len(result.Items) != 0 || len(result.Calls) != 0 {
				t.Fatalf("unsafe error or partial response: %+v %v", result, err)
			}
		})
	}
}

func TestDecodeStreamRejectsIncompleteAndFailedResponses(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{"missing completion", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", "without response.completed"},
		{"incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", "incomplete"},
		{"failed", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"model unavailable\"}}}\n\n", "remote_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeStream(strings.NewReader(test.stream), nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got error %v, want %q", err, test.want)
			}
		})
	}
}
