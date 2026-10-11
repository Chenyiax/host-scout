package tools

import "host-scout/pkg/tool"

type Status = tool.Status
type Result = tool.Result

const (
	Succeeded = tool.Succeeded
	Failed    = tool.Failed
	Denied    = tool.Denied
	TimedOut  = tool.TimedOut
	Cancelled = tool.Cancelled
	Skipped   = tool.Skipped
)

var Value = tool.Value
var CloneResult = tool.CloneResult
var Bound = tool.Bound
