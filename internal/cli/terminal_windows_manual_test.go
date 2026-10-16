//go:build windows

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Run under a Windows terminal with HOST_SCOUT_WINDOWS_INTERACTIVE=1.
// This keeps the regular automated suite non-interactive.
func TestWindowsTerminalManual(t *testing.T) {
	if os.Getenv("HOST_SCOUT_WINDOWS_INTERACTIVE") != "1" {
		t.Skip("manual terminal validation")
	}
	t.Setenv("HOST_SCOUT_API_KEY", "terminal-test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Input[len(req.Input)-1]
		if string(last["type"]) == `"function_call_output"` {
			sendText(w, "WINDOWS-COMMAND-DONE", false)
			return
		}
		var user string
		for _, item := range req.Input {
			if string(item["role"]) == `"user"` {
				_ = json.Unmarshal(item["content"], &user)
			}
		}
		switch user {
		case "run":
			args, _ := json.Marshal(map[string]any{"argv": []string{"cmd", "/c", "echo approved"}, "purpose": "验证 Windows 命令审批"})
			body, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": "win-call", "name": "run_command", "arguments": string(args)}}}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", body)
		case "wait":
			<-r.Context().Done()
		default:
			sendText(w, "WINDOWS-RECEIVED:"+user, false)
		}
	}))
	defer server.Close()
	if err := Run([]string{"--model", "fake", "--base-url", server.URL}, os.Stdin, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
}
