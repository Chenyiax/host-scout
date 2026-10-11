package tool

import "encoding/json"

type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Denied    Status = "denied"
	TimedOut  Status = "timed_out"
	Cancelled Status = "cancelled"
	Skipped   Status = "skipped"
)

// Result records an execution outcome, not a conclusion about the host.
// Data must contain valid JSON. ExitCode is nil when no exit status exists.
type Result struct {
	Status            Status          `json:"status"`
	Code              string          `json:"code,omitempty"`
	Data              json.RawMessage `json:"data,omitempty"`
	Argv              []string        `json:"argv,omitempty"`
	ExitCode          *int            `json:"exit_code"`
	Output            string          `json:"output,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	TerminationReason string          `json:"termination_reason,omitempty"`
}

func Value(value any) Result {
	data, err := json.Marshal(value)
	if err != nil {
		return Result{Status: Failed, Code: "encode_result"}
	}
	return Result{Status: Succeeded, Data: data}
}

func CloneResult(r Result) Result {
	r.Argv = append([]string(nil), r.Argv...)
	r.Data = append(json.RawMessage(nil), r.Data...)
	if r.ExitCode != nil {
		v := *r.ExitCode
		r.ExitCode = &v
	}
	return r
}

// Bound limits model feedback and in-memory observations to 32 KiB.
func Bound(r Result) Result {
	const limit = 32 << 10
	switch r.Status {
	case Succeeded, Failed, Denied, TimedOut, Cancelled, Skipped:
	default:
		return Result{Status: Failed, Code: "invalid_result"}
	}
	if len(r.Data) > 0 && !json.Valid(r.Data) {
		return Result{Status: Failed, Code: "invalid_result"}
	}
	if len(r.Data) > limit {
		if r.Output == "" {
			r.Output = string(r.Data[:limit])
		}
		r.Data = nil
		r.Truncated = true
	}
	available := limit - len(r.Data)
	if len(r.Output) > available {
		r.Output = r.Output[:available]
		r.Truncated = true
	}
	return r
}
