package workspaces

import (
	"context"
	"log/slog"
)

// LogWorkspaceOp is a convenience function for structured logging within
// workspace operations. It ensures every log record carries workspace.id
// and the operation name, matching the logging convention established by
// the AccessLog middleware (Req 16.11).
//
// Usage in service methods:
//
//	LogWorkspaceOp(ctx, "CreateForOnboarding", slog.String("slug", slug))
func LogWorkspaceOp(ctx context.Context, op string, attrs ...slog.Attr) {
	allAttrs := make([]slog.Attr, 0, len(attrs)+1)
	allAttrs = append(allAttrs, slog.String("op", op))
	allAttrs = append(allAttrs, attrs...)
	slog.LogAttrs(ctx, slog.LevelInfo, "workspace operation", allAttrs...)
}

// LogWorkspaceError is the error-severity equivalent of LogWorkspaceOp.
// Used for operations that fail but do not propagate the error to the caller
// (e.g., audit write failures, notification failures, Stytch mirror failures).
func LogWorkspaceError(ctx context.Context, op string, err error, attrs ...slog.Attr) {
	allAttrs := make([]slog.Attr, 0, len(attrs)+2)
	allAttrs = append(allAttrs, slog.String("op", op))
	errMsg := "<nil>"
	if err != nil {
		errMsg = err.Error()
	}
	allAttrs = append(allAttrs, slog.String("error", errMsg))
	allAttrs = append(allAttrs, attrs...)
	slog.LogAttrs(ctx, slog.LevelError, "workspace operation failed", allAttrs...)
}

// LogWorkspaceWarn is the warning-severity equivalent for non-critical failures
// (e.g., Stytch mirror failures that don't affect authority).
func LogWorkspaceWarn(ctx context.Context, op string, msg string, attrs ...slog.Attr) {
	allAttrs := make([]slog.Attr, 0, len(attrs)+1)
	allAttrs = append(allAttrs, slog.String("op", op))
	allAttrs = append(allAttrs, attrs...)
	slog.LogAttrs(ctx, slog.LevelWarn, msg, allAttrs...)
}
