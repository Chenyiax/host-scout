package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"host-scout/internal/policy"
	"host-scout/internal/tools"
)

type testApprover struct {
	allow bool
	calls []policy.CommandRequest
}

func (a *testApprover) ApproveCommand(_ context.Context, request policy.CommandRequest) (bool, error) {
	a.calls = append(a.calls, request)
	return a.allow, nil
}

func TestDeniedCommandDoesNotRun(t *testing.T) {
	approver := &testApprover{}
	tool, err := New(approver)
	if err != nil {
		t.Fatal(err)
	}
	value, err := tool.Run(context.Background(), json.RawMessage(`{"argv":["nonexistent-command-for-test"]}`))
	if err != nil || value.Status != tools.Denied {
		t.Fatalf("expected a denial, got %v", err)
	}
	if len(approver.calls) != 1 || approver.calls[0].Argv[0] != "nonexistent-command-for-test" {
		t.Fatalf("unexpected approval requests: %#v", approver.calls)
	}
}

func TestApprovedCommandRuns(t *testing.T) {
	approver := &testApprover{allow: true}
	tool, err := New(approver)
	if err != nil {
		t.Fatal(err)
	}
	args := helperArgs(t, "hello")
	value, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	result := value
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Output != "approved" {
		t.Fatalf("unexpected command result: %#v", result)
	}
}

func TestInvalidArgumentsAreRejectedBeforeApproval(t *testing.T) {
	approver := &testApprover{allow: true}
	tool, err := New(approver)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"argv":[]}`,
		`{"argv":["echo\nspoof"]}`,
		`{"argv":["echo"],"extra":true}`,
		`{"argv":["echo"]}{"argv":["other"]}`,
		`{"argv":[""]}`,
		`{"argv":["echo","\u0000"]}`,
		"null",
		`{"argv":["echo","` + strings.Repeat("x", maxArgvBytes) + `"]}`,
		`{"argv":[` + strings.Repeat(`"echo",`, 32) + `"echo"]}`,
	} {
		if value, err := tool.Run(context.Background(), json.RawMessage(raw)); err != nil || value.Code != "invalid_arguments" {
			t.Fatalf("accepted invalid arguments %s", raw)
		}
	}
	if len(approver.calls) != 0 {
		t.Fatalf("approval was requested for invalid arguments: %#v", approver.calls)
	}
}

func TestCommandHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--helper" || i+1 >= len(os.Args) {
			continue
		}
		switch os.Args[i+1] {
		case "hello":
			fmt.Fprint(os.Stdout, "approved")
			os.Exit(0)
		case "fail":
			os.Exit(7)
		case "output":
			_, _ = os.Stdout.Write([]byte(strings.Repeat("x", 40000)))
			_, _ = os.Stderr.Write([]byte("stderr"))
			os.Exit(0)
		case "env":
			if os.Getenv("HOST_SCOUT_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
				os.Exit(9)
			}
			os.Exit(0)
		case "sleep":
			time.Sleep(30 * time.Second)
			os.Exit(0)
		}
	}
}
func helperArgs(t *testing.T, mode string) json.RawMessage {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"argv": []string{exe, "-test.run=TestCommandHelper", "--", "--helper", mode}})
	return args
}
func TestStructuredCommandResults(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "test-secret")
	t.Setenv("OPENAI_API_KEY", "test-secret")
	tool, _ := New(&testApprover{allow: true})
	for _, tc := range []struct {
		mode   string
		status tools.Status
		code   string
		exit   int
	}{
		{"fail", tools.Failed, "nonzero_exit", 7}, {"output", tools.Succeeded, "", 0}, {"env", tools.Succeeded, "", 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			value, err := tool.Run(context.Background(), helperArgs(t, tc.mode))
			if err != nil {
				t.Fatal(err)
			}
			r := value
			if r.Status != tc.status || r.Code != tc.code || r.ExitCode == nil || *r.ExitCode != tc.exit {
				t.Fatalf("%+v", r)
			}
			if tc.mode == "output" && (len(r.Output) != maxOutputBytes || !r.Truncated) {
				t.Fatalf("output length %d truncated %v", len(r.Output), r.Truncated)
			}
		})
	}
	value, err := tool.Run(context.Background(), json.RawMessage(`{"argv":["nonexistent-command-for-test"]}`))
	if err != nil || value.ExitCode != nil || value.Code != "start_failed" {
		t.Fatalf("start failure: %#v %v", value, err)
	}
}
func TestTimeoutCancellationAndStartBoundary(t *testing.T) {
	tool, _ := New(&testApprover{allow: true})
	tool.timeout = 50 * time.Millisecond
	value, err := tool.Run(context.Background(), helperArgs(t, "sleep"))
	if err != nil || value.Status != tools.TimedOut || value.ExitCode != nil {
		t.Fatalf("timeout %#v %v", value, err)
	}
	tool.timeout = commandTimeout
	ctx, cancel := context.WithCancel(context.Background())
	ctx = tools.WithStart(ctx, func() { time.AfterFunc(30*time.Millisecond, cancel) })
	start := time.Now()
	value, err = tool.Run(ctx, helperArgs(t, "sleep"))
	if err != nil || value.Status != tools.Cancelled || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel %#v %v %v", value, err, time.Since(start))
	}
	starts := 0
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, _ = tool.Run(tools.WithStart(ctx, func() { starts++ }), helperArgs(t, "env"))
	_, _ = tool.Run(tools.WithStart(context.Background(), func() { starts++ }), json.RawMessage(`{"argv":[]}`))
	denied, _ := New(&testApprover{})
	_, _ = denied.Run(tools.WithStart(context.Background(), func() { starts++ }), helperArgs(t, "env"))
	if starts != 0 {
		t.Fatal("execution started before validation/approval")
	}
}

func TestCommandEnvironmentOmitsCredentials(t *testing.T) {
	t.Setenv("HOST_SCOUT_API_KEY", "secret-for-test")
	t.Setenv("OPENAI_API_KEY", "other-secret-for-test")
	for _, value := range commandEnvironment() {
		if strings.Contains(value, "secret-for-test") {
			t.Fatalf("credential leaked into command environment: %q", value)
		}
	}
	if os.Getenv("HOST_SCOUT_API_KEY") == "" {
		t.Fatal("test did not set a credential")
	}
}

func TestOutputIsBounded(t *testing.T) {
	value := &boundedOutput{limit: 4}
	if n, err := value.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("write result = %d, %v", n, err)
	}
	if value.String() != "abcd" || !value.Truncated() {
		t.Fatalf("unexpected output %q, truncated=%v", value.String(), value.Truncated())
	}
}
