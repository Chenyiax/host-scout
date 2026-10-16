package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"host-scout/internal/policy"
)

type terminalApprover struct {
	input         *inputReader
	output        io.Writer
	managedPrompt bool
}

func (a terminalApprover) ApproveCommand(ctx context.Context, request policy.CommandRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !a.input.terminal {
		return false, nil
	}
	if !a.managedPrompt {
		printApprovalDetails(a.output, request.Purpose, request.Argv)
	}
	fmt.Fprint(a.output, "Allow once? [y/N] ")
	line, err := a.input.ReadLinePrompt(ctx, "Allow once? [y/N] ", false)
	if err != nil {
		if errors.Is(err, errInterrupted) {
			return false, context.Canceled
		}
		return false, err
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}
