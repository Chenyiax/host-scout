// Package command provides the CLI's sole built-in host tool. Every exact argv
// requires approval. A call is synchronous and owns its direct process and
// output pipe until bounded cleanup finishes. Linux also kills ordinary same
// group descendants after normal exit, cancellation or timeout. Other platforms
// guarantee only direct-process cleanup. A failed cleanup returns cleanup_failed
// with the original termination reason and possible residual-process risk.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"host-scout/internal/policy"
	tools "host-scout/pkg/tool"
)

const (
	commandTimeout = 10 * time.Second
	maxOutputBytes = 32 << 10
	maxArgvBytes   = 4 << 10
)

type Tool struct {
	approver      policy.Approver
	timeout       time.Duration
	ops           *platformOps
	cleanupBudget time.Duration
	pipeBudget    time.Duration
}

func New(approver policy.Approver) (*Tool, error) {
	if approver == nil {
		return nil, errors.New("command tool requires an approver")
	}
	return &Tool{approver: approver, timeout: commandTimeout}, nil
}

func (Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "run_command",
		Description: "Run one host command after the user explicitly approves its exact argv. No shell is added. Output is limited and sent to the model.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"argv":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":32}},"required":["argv"],"additionalProperties":false}`),
	}
}

func (t Tool) Run(ctx context.Context, arguments json.RawMessage) (tools.Result, error) {
	var args struct {
		Argv []string `json:"argv"`
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}
	if err := decoder.Decode(new(any)); err == nil {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	} else if !errors.Is(err, io.EOF) {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}
	if err := validateArgv(args.Argv); err != nil {
		return tools.Result{Status: tools.Failed, Code: "invalid_arguments"}, nil
	}
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	approved, err := policy.Approve(ctx, t.approver, policy.CommandRequest{Argv: append([]string(nil), args.Argv...)})
	if err != nil {
		return tools.Result{}, err
	}
	if !approved {
		return tools.Result{Status: tools.Denied, Code: "denied", Argv: append([]string(nil), args.Argv...)}, nil
	}
	if ctx.Err() != nil {
		return tools.Result{}, ctx.Err()
	}
	return t.execute(ctx, args.Argv), nil
}

func validateArgv(argv []string) error {
	if len(argv) == 0 || len(argv) > 32 {
		return errors.New("argv must contain 1 to 32 strings")
	}
	total := 0
	for _, arg := range argv {
		total += len(arg)
		if strings.ContainsRune(arg, 0) || strings.IndexFunc(arg, unicode.IsControl) >= 0 {
			return errors.New("argv contains a control character")
		}
	}
	if argv[0] == "" || total > maxArgvBytes {
		return errors.New("argv has an empty executable or exceeds 4 KiB")
	}
	return nil
}

func commandEnvironment() []string {
	keys := []string{"PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "TZ"}
	if runtime.GOOS == "windows" {
		keys = append(keys, "SystemRoot", "WINDIR", "PATHEXT", "ComSpec", "TEMP", "TMP", "USERPROFILE")
	}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
		}
	}
	return result
}

type boundedOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(value)
	available := w.limit - w.buffer.Len()
	if available > 0 {
		if available > n {
			available = n
		}
		_, _ = w.buffer.Write(value[:available])
	}
	if n > available {
		w.truncated = true
	}
	return n, nil
}

func (w *boundedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func (w *boundedOutput) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}
