package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"host-scout/internal/agent"
	tools "host-scout/pkg/tool"
)

type commandArguments struct {
	Argv    []string `json:"argv"`
	Purpose string   `json:"purpose"`
}

func evidenceCommandArguments(item agent.Evidence) (commandArguments, bool) {
	if item.Tool != "run_command" || item.ArgumentsTruncated || !json.Valid(item.Arguments) {
		return commandArguments{}, false
	}
	var args commandArguments
	if err := json.Unmarshal(item.Arguments, &args); err != nil || len(args.Argv) == 0 {
		return commandArguments{}, false
	}
	return args, true
}

func quotedArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = strconv.Quote(safeText(arg))
	}
	return strings.Join(quoted, " ")
}

func printApprovalDetails(output io.Writer, purpose string, argv []string) {
	if purpose == "" {
		purpose = "Not provided"
	}
	fmt.Fprintf(output, "Model purpose: %s\nCommand requested: %s\nIts output will be sent to the model.\n", safeText(purpose), quotedArgv(argv))
}

func printEvidence(output io.Writer, item agent.Evidence) {
	fmt.Fprintf(output, "Record: %s\nTool: %s  Call: %s\nStatus: %s", safeText(item.ID), safeText(item.Tool), safeText(item.CallID), item.Result.Status)
	if item.Result.Code != "" {
		fmt.Fprintf(output, "/%s", safeText(item.Result.Code))
	}
	if item.StartedAt.IsZero() {
		fmt.Fprint(output, " (not executed)")
	}
	fmt.Fprintln(output)
	if args, ok := evidenceCommandArguments(item); ok {
		purpose := args.Purpose
		if purpose == "" {
			purpose = "Not provided"
		}
		fmt.Fprintf(output, "Model purpose: %s\nCommand: %s\n", safeText(purpose), quotedArgv(args.Argv))
	} else if item.Tool == "run_command" {
		fmt.Fprintln(output, "Command arguments: unavailable or invalid")
	}
	fmt.Fprintf(output, "Proposed: %s\n", item.CreatedAt.Format(time.RFC3339))
	if !item.StartedAt.IsZero() {
		fmt.Fprintf(output, "Started: %s\nFinished: %s\nDuration: %s\n", item.StartedAt.Format(time.RFC3339), item.FinishedAt.Format(time.RFC3339), item.FinishedAt.Sub(item.StartedAt).Round(time.Millisecond))
		if item.Result.ExitCode != nil {
			fmt.Fprintf(output, "Exit code: %d\n", *item.Result.ExitCode)
		}
		if item.Result.Output != "" {
			fmt.Fprintf(output, "Output:\n%s\n", safeText(item.Result.Output))
		}
		if len(item.Result.Data) > 0 {
			fmt.Fprintf(output, "Data: %s\n", safeText(string(item.Result.Data)))
		}
		if item.Result.Truncated {
			fmt.Fprintln(output, "Output was truncated.")
		}
	} else {
		fmt.Fprintf(output, "Finished: %s\nNo host observation: this request was not executed.\n", item.FinishedAt.Format(time.RFC3339))
	}
	if item.Result.Code == "cleanup_failed" && item.Result.Status == tools.Failed {
		fmt.Fprintf(output, "Cleanup failed after %s; managed processes may remain.\n", safeText(item.Result.TerminationReason))
	}
}
