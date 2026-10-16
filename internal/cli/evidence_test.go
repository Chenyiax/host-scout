package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"host-scout/internal/agent"
	tools "host-scout/pkg/tool"
)

func TestEvidenceDetailDistinguishesExecutedAndUnexecutedCommands(t *testing.T) {
	start := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	arguments := json.RawMessage(`{"argv":["echo","hello"],"purpose":"检查状态"}`)
	zero := 0
	for _, tc := range []struct {
		name   string
		record agent.Evidence
		want   []string
		absent []string
	}{
		{"executed", agent.Evidence{ID: "evidence-001", CallID: "c1", Tool: "run_command", Arguments: arguments, CreatedAt: start, StartedAt: start, FinishedAt: start.Add(time.Second), Result: tools.Result{Status: tools.Succeeded, ExitCode: &zero, Output: "hello"}}, []string{"Model purpose: 检查状态", `Command: "echo" "hello"`, "Duration: 1s", "Exit code: 0", "Output:\nhello"}, []string{"not executed"}},
		{"denied", agent.Evidence{ID: "evidence-002", CallID: "c2", Tool: "run_command", Arguments: arguments, CreatedAt: start, FinishedAt: start, Result: tools.Result{Status: tools.Denied, Code: "denied"}}, []string{"denied/denied (not executed)", "No host observation"}, []string{"Duration:", "Exit code:", "Output:"}},
		{"skipped", agent.Evidence{ID: "evidence-003", CallID: "c3", Tool: "run_command", Arguments: arguments, CreatedAt: start, FinishedAt: start, Result: tools.Result{Status: tools.Skipped, Code: "step_limit"}}, []string{"skipped/step_limit (not executed)", "Model purpose: 检查状态"}, []string{"Duration:", "Exit code:"}},
		{"invalid", agent.Evidence{ID: "evidence-004", CallID: "c4", Tool: "run_command", Arguments: json.RawMessage(`{"argv":`), CreatedAt: start, FinishedAt: start, Result: tools.Result{Status: tools.Failed, Code: "invalid_arguments"}}, []string{"Command arguments: unavailable or invalid", "not executed"}, []string{"Duration:", "Exit code:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printEvidence(&out, tc.record)
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q:\n%s", want, out.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out.String(), absent) {
					t.Fatalf("unexpected %q:\n%s", absent, out.String())
				}
			}
		})
	}
}
