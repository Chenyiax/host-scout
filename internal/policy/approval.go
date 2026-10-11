// Package policy defines the authorization boundary for host actions.
package policy

import "context"

// CommandRequest describes the exact process the agent wants to start.
// Implementations must require an explicit, per-call decision.
type CommandRequest struct {
	Argv []string
}

type Approver interface {
	ApproveCommand(ctx context.Context, request CommandRequest) (bool, error)
}

type approvalKey struct{}
type Observer struct {
	Requested func(CommandRequest)
	Resolved  func(bool)
}

func WithObserver(ctx context.Context, observer Observer) context.Context {
	return context.WithValue(ctx, approvalKey{}, observer)
}

// Approve emits no resolved decision when the wait is cancelled or reaches EOF.
func Approve(ctx context.Context, approver Approver, request CommandRequest) (bool, error) {
	observer, _ := ctx.Value(approvalKey{}).(Observer)
	if observer.Requested != nil {
		observer.Requested(CommandRequest{Argv: append([]string(nil), request.Argv...)})
	}
	approved, err := approver.ApproveCommand(ctx, request)
	if err == nil && ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err == nil && observer.Resolved != nil {
		observer.Resolved(approved)
	}
	return approved, err
}
