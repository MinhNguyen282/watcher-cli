package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/crush/internal/permission"
)

// ApprovalOutcome is the closed set of results an approval can produce.
// Only ApprovalAllowedOnce permits a tool body to run. Every other value,
// including an unavailable answerer, fails closed.
type ApprovalOutcome int

const (
	// ApprovalAllowedOnce permits exactly this tool call to run.
	ApprovalAllowedOnce ApprovalOutcome = iota
	// ApprovalRejected means the user or a policy denied the call.
	ApprovalRejected
	// ApprovalCancelled means the answerer went away before deciding.
	ApprovalCancelled
	// ApprovalUnavailable means no answerer was wired up.
	ApprovalUnavailable
)

func (o ApprovalOutcome) String() string {
	switch o {
	case ApprovalAllowedOnce:
		return "allowed-once"
	case ApprovalRejected:
		return "rejected"
	case ApprovalCancelled:
		return "cancelled"
	case ApprovalUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// Approver resolves one tool-call approval. It is injected into the tool
// context by the dispatch pipeline so tool bodies never talk to the
// permission service directly.
type Approver func(context.Context, permission.CreatePermissionRequest) (ApprovalOutcome, error)

type approverKey struct{}

// WithApprover returns a context carrying the pipeline approval stage.
func WithApprover(ctx context.Context, approver Approver) context.Context {
	return context.WithValue(ctx, approverKey{}, approver)
}

// RequestApproval asks the pipeline's approval stage to decide a tool call.
// When no pipeline is present it falls back to the supplied service so
// tools remain usable in tests and headless callers. A missing service is
// an error, never an implicit allow.
func RequestApproval(ctx context.Context, svc permission.Service, req permission.CreatePermissionRequest) (ApprovalOutcome, error) {
	if approver, ok := ctx.Value(approverKey{}).(Approver); ok && approver != nil {
		return approver(ctx, req)
	}
	if svc == nil {
		return ApprovalUnavailable, errors.New("permission service unavailable; denying tool call")
	}
	granted, err := svc.Request(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ApprovalCancelled, fmt.Errorf("approval cancelled: %w", err)
		}
		return ApprovalUnavailable, fmt.Errorf("approval failed: %w", err)
	}
	if !granted {
		return ApprovalRejected, nil
	}
	return ApprovalAllowedOnce, nil
}
