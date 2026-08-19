package workspaces

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"backend/contextutil"
	"backend/responses"
)

// RequirePermission returns an Echo middleware that enforces the given Permission
// before the handler runs. It reads the SessionContext from the echo context
// (set by the session resolver middleware) and evaluates in this exact order:
//
// 1. Unresolved credential -> 401 fixed body
// 2. users.status == "suspended" -> 403
// 3. No membership for (user, workspace) or status == "removed" -> 404
// 4. status == "suspended" -> 403 membership_suspended
// 5. deletion_state == "scheduled" AND write AND permission != workspace.delete -> 409 workspace_deletion_scheduled
// 6. !sc.Grants(required) -> 403 permission_denied with details.missing_permission
// 7. Otherwise: set workspace_id in context and call the handler
//
// Order 3 before 4: removed is indistinguishable from nonexistent (404)
// Order 2 before 3: Req 8.9 applies "regardless of Workspace_Role"
// Zero authorization decisions based on user_type (Req 8.11)
// Every early return runs the handler zero times
func RequirePermission(p Permission) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			sc, ok := FromEcho(c)

			// Step 1: Unresolved credential -> 401
			if !ok {
				return responses.SendError(c, http.StatusUnauthorized, "Unauthorized")
			}

			// Step 2: User suspended -> 403 (Req 8.9: applies regardless of Workspace_Role)
			if sc.UserStatus == "suspended" {
				return responses.SendError(c, http.StatusForbidden, "Account suspended")
			}

			// Step 3: No membership or removed -> 404
			// A removed membership is indistinguishable from a nonexistent workspace.
			if sc.MembershipID == "" || sc.MembershipStatus == MembershipRemoved {
				return responses.SendError(c, http.StatusNotFound, "Not found")
			}

			// Step 4: Membership suspended -> 403 membership_suspended
			if sc.MembershipStatus == MembershipSuspended {
				return responses.SendErrorWithCode(c, http.StatusForbidden,
					"membership_suspended",
					"Membership is suspended",
					"", "", nil,
				)
			}

			// Step 5: Workspace deletion scheduled on a write whose permission
			// is not workspace.delete -> 409 workspace_deletion_scheduled (Req 18.4)
			if sc.DeletionState == DeletionScheduled && isWriteMethod(c) && p != PermWorkspaceDelete {
				details := map[string]any{}
				if sc.DeletionEffectiveAt != nil {
					details["effective_at"] = sc.DeletionEffectiveAt
				}
				return responses.SendErrorWithCode(c, http.StatusConflict,
					"workspace_deletion_scheduled",
					"Workspace deletion is scheduled",
					"", "", details,
				)
			}

			// Step 6: Permission check -> 403 permission_denied
			if !sc.Grants(p) {
				return responses.SendErrorWithCode(c, http.StatusForbidden,
					"permission_denied",
					"Insufficient permissions",
					"", "",
					map[string]any{"missing_permission": string(p)},
				)
			}

			// Step 7: Set workspace ID in context and proceed
			contextutil.SetWorkspaceID(c, sc.WorkspaceID)
			return next(c)
		}
	}
}

// isWriteMethod returns true for HTTP methods that mutate state.
func isWriteMethod(c echo.Context) bool {
	switch c.Request().Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}
