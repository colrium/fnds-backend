package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/users"

	"github.com/labstack/echo/v4"
)

// Req 17.5 requires suspending a user to revoke their Stytch workspace
// sessions. RevokeAllSessionsForUser lives on workspaces.MembershipService and
// had no caller at all; SetSuspendHook is the seam that connects it. These
// tests pin the properties that make the wiring correct: it fires on
// suspension, only on suspension, and tolerates never being registered.
//
// They exercise notifySuspension rather than the handler because every path
// through HandleUpdateUserStatus that reaches the hook first writes to and
// reads from Mongo, which is unavailable here.

// setSuspendHookForTest installs a hook and restores the previous value, so the
// package-level var does not leak between tests.
func setSuspendHookForTest(t *testing.T, hook SuspendHook) {
	t.Helper()
	previous := onUserSuspended
	onUserSuspended = hook
	t.Cleanup(func() { onUserSuspended = previous })
}

func TestNotifySuspensionFiresOnlyForSuspension(t *testing.T) {
	cases := []struct {
		status string
		want   []string
	}{
		{users.StatusSuspended, []string{"user-1"}},
		{users.StatusActive, nil},
		{users.StatusPending, nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.status, func(t *testing.T) {
			var calledWith []string
			setSuspendHookForTest(t, func(userID string) {
				calledWith = append(calledWith, userID)
			})

			notifySuspension(testCase.status, "user-1")

			if len(calledWith) != len(testCase.want) {
				t.Fatalf("hook calls = %v, want %v", calledWith, testCase.want)
			}
			for i, want := range testCase.want {
				if calledWith[i] != want {
					t.Errorf("hook call %d = %q, want %q", i, calledWith[i], want)
				}
			}
		})
	}
}

func TestNotifySuspensionWithNoHookRegistered(t *testing.T) {
	// Without the nil guard this would panic, taking down every suspension in
	// any deployment that does not run Stytch employer auth.
	setSuspendHookForTest(t, nil)
	notifySuspension(users.StatusSuspended, "user-1")
}

func TestSuspendHookNotCalledWhenStatusRejected(t *testing.T) {
	var called bool
	setSuspendHookForTest(t, func(string) { called = true })

	e := echo.New()
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/admin/users/user-1/status",
		strings.NewReader(`{"status":"not-a-status"}`),
	)
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	ctx.SetParamNames("id")
	ctx.SetParamValues("user-1")

	if err := HandleUpdateUserStatus(ctx); err != nil {
		t.Fatalf("HandleUpdateUserStatus() error = %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
	if called {
		t.Error("suspend hook must not fire when the request is rejected as invalid")
	}
}
