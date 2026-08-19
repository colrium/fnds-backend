package audit

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"backend/configs"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Logger writes audit entries. Its Write method returns NO error to its caller:
// a failed audit write emits one error record and is dropped, because Req 23.8
// forbids failing the underlying action over it. The signature — not a convention —
// enforces that the underlying action still completes.
type Logger struct{}

// NewLogger creates an audit Logger.
func NewLogger() *Logger {
	return &Logger{}
}

// WriteInput carries the parameters for an audit write.
type WriteInput struct {
	WorkspaceID string
	ActorUserID string
	ActorKind   string // "member" | "platform_admin" | "system"
	Action      string // must be one of the Action* constants
	TargetKind  string
	TargetID    string
	IP          string
	Previous    any // omitted when the action changes no stored value
	New         any // omitted when the action changes no stored value
}

// Write records one audit entry. It NEVER returns an error — a failed write
// emits one error-severity log and the calling action still completes (Req 23.8).
func (l *Logger) Write(ctx context.Context, in WriteInput) {
	// Validate action is in the closed set
	if !isValidAction(in.Action) {
		slog.ErrorContext(ctx, "audit: unknown action, entry dropped",
			"action", in.Action,
			"target_kind", in.TargetKind,
			"target_id", in.TargetID,
		)
		return
	}

	// Redact sensitive values from Previous and New (Req 23.7, P28)
	previous := redactSensitive(in.Previous)
	newVal := redactSensitive(in.New)

	entry := Entry{
		ID:          uuid.New().String(),
		WorkspaceID: in.WorkspaceID,
		ActorUserID: in.ActorUserID,
		ActorKind:   in.ActorKind,
		Action:      in.Action,
		TargetKind:  in.TargetKind,
		TargetID:    in.TargetID,
		IP:          in.IP,
		Previous:    previous,
		New:         newVal,
		CreatedAt:   time.Now().UTC(),
	}

	coll := configs.MI.DB.Collection("audit_log_entries")
	_, err := coll.InsertOne(ctx, entry)
	if err != nil {
		slog.ErrorContext(ctx, "audit: failed to write entry",
			"action", in.Action,
			"target_kind", in.TargetKind,
			"target_id", in.TargetID,
			"error", err.Error(),
		)
	}
}

// isValidAction checks if the action is in the closed set of Req 23.1.
func isValidAction(action string) bool {
	switch action {
	case ActionWorkspaceCreated,
		ActionWorkspaceRenamed,
		ActionWorkspaceDeletionState,
		ActionInvitationCreated,
		ActionInvitationRevoked,
		ActionInvitationAccepted,
		ActionMembershipCreated,
		ActionRoleChanged,
		ActionStatusChanged,
		ActionOwnershipTransferred,
		ActionAccountLinkCreated,
		ActionAccountLinkRemoved,
		ActionAccountLinkEmailChanged,
		ActionPaymentMethodChanged:
		return true
	default:
		return false
	}
}

// sensitiveSubstrings matches the same key-substring rule that logging.Redactor
// uses (Req 23.7, P28). A key containing any of these substrings has its value
// replaced with [REDACTED].
var sensitiveSubstrings = []string{
	"token", "secret", "password", "passwd", "authorization",
	"key", "session", "cookie", "jwt", "bearer",
}

// redactSensitive removes sensitive keys from values that might contain them.
// Uses the same key-substring rule as logging.Redactor (Req 23.7, P28).
func redactSensitive(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(t))
		for k, val := range t {
			if matchesSensitiveKey(k) {
				result[k] = "[REDACTED]"
			} else {
				result[k] = redactSensitive(val)
			}
		}
		return result
	default:
		return v
	}
}

// matchesSensitiveKey reports whether a key contains a sensitive substring,
// compared case-insensitively.
func matchesSensitiveKey(segment string) bool {
	lowered := strings.ToLower(segment)
	for _, substring := range sensitiveSubstrings {
		if strings.Contains(lowered, substring) {
			return true
		}
	}
	return false
}

// ListResult contains paginated audit entries.
type ListResult struct {
	Entries []Entry `json:"entries"`
	Total   int64   `json:"total"`
	Page    int64   `json:"page"`
	Limit   int64   `json:"limit"`
}

// List returns audit entries for a workspace in reverse chronological order
// with pagination. Returns zero entries belonging to any other workspace (Req 23.5).
//
// There is NO update and NO delete method and NO route that modifies or deletes
// an entry, so append-only is structural (Req 23.3).
// Entries are retained with NO TTL index (Req 23.6).
func (l *Logger) List(ctx context.Context, workspaceID string, page, limit int64) (ListResult, error) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	coll := configs.MI.DB.Collection("audit_log_entries")
	filter := bson.M{"workspace_id": workspaceID}

	// Count total matching entries.
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return ListResult{}, fmt.Errorf("audit.List: count: %w", err)
	}

	// Find entries in reverse chronological order with pagination.
	skip := (page - 1) * limit
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(skip).
		SetLimit(limit)

	cursor, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return ListResult{}, fmt.Errorf("audit.List: find: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var entries []Entry
	if err := cursor.All(ctx, &entries); err != nil {
		return ListResult{}, fmt.Errorf("audit.List: decode: %w", err)
	}

	if entries == nil {
		entries = []Entry{}
	}

	return ListResult{
		Entries: entries,
		Total:   total,
		Page:    page,
		Limit:   limit,
	}, nil
}
