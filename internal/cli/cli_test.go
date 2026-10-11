package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunStartsConversationOnDevelopmentPlatform(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-key")
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var request struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Tools) != 1 || request.Tools[0].Name != "run_command" {
			http.Error(w, fmt.Sprintf("unexpected tools: %+v", request.Tools), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello from the model.\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from the model.\"}]}]}}\n\n")
	}))
	defer server.Close()

	var output, errorsOut bytes.Buffer
	err := Run([]string{"--model", "test-model", "--base-url", server.URL + "/v1"}, strings.NewReader("hello\n/tools\n/exit\n"), &output, &errorsOut)
	if err != nil {
		t.Fatalf("CLI did not run: %v; stderr: %s", err, &errorsOut)
	}
	if !called.Load() || !strings.Contains(output.String(), "Hello from the model.") || !strings.Contains(output.String(), "run_command:") {
		t.Fatalf("unexpected CLI output: %s", &output)
	}
	if strings.Contains(output.String(), "inspect_system:") || strings.Contains(output.String(), "list_processes:") || strings.Contains(output.String(), "Prefer built-in read-only") {
		t.Fatalf("old tool or prompt was advertised: %s", &output)
	}
}
