package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"host-scout/internal/policy"
)

type terminalApprover struct {
	input  *inputReader
	output io.Writer
}

func (a terminalApprover) ApproveCommand(ctx context.Context, request policy.CommandRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !a.input.terminal {
		return false, nil
	}
	quoted := make([]string, len(request.Argv))
	for i, arg := range request.Argv {
		quoted[i] = strconv.Quote(arg)
	}
	fmt.Fprintf(a.output, "\nCommand requested: %s\nIts output will be sent to the model. Allow once? [y/N] ", strings.Join(quoted, " "))
	line, err := a.input.ReadLine(ctx)
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
