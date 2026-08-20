package workspaces

import (
	"context"
	"strings"
	"testing"
	"time"

	"backend/audit"
	"backend/configs"
	"backend/stytch"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// The side effects wired onto the MembershipService mutation paths are
// best-effort by design (Req 7.15): the Fundis document is authoritative, so it
// is written first and a Stytch or notification failure must never fail the
// operation or roll the document back.

// useSideEffectTestMongo swaps configs.MI to point at the mtest database and
// restores the original on cleanup. Mirrors usePostAuthTestMongo.
func useSideEffectTestMongo(mt *mtest.T) {
	mt.Helper()
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.DB}
	mt.Cleanup(func() { configs.MI = previous })
}

// newSideEffectService builds a MembershipService wired to a stytch.Fake that
// already holds the organization and member the test memberships point at, plus
// a NotificationSpy in place of the production email notifier.
func newSideEffectService(t testing.TB) (*MembershipService, *stytch.Fake, *stytch.NotificationSpy) {
	t.Helper()
	clock := newTestClock()
	fake := stytch.NewFake(clock)
	fake.Orgs["org-1"] = &stytch.FakeOrg{
		Organization: stytch.Organization{OrganizationID: "org-1"},
		Members: map[string]stytch.Member{
			"member-1": {MemberID: "member-1", OrganizationID: "org-1", Email: "member@example.com"},
		},
		Sessions: map[string]stytch.Session{},
	}
	adapter := stytch.NewWithClient(stytch.Config{
		ProjectID:   "project-test",
		Secret:      "secret-test",
		Environment: "test",
		Timeout:     5 * time.Second,
		MaxAttempts: 3,
		Clock:       clock,
	}, fake)

	spy := stytch.NewNotificationSpy(clock)
	svc := NewMembershipService(adapter, audit.NewLogger(), clock, false)
	svc.notifier = spy
	return svc, fake, spy
}

// sideEffectMembershipDoc is a non-owner membership pointing at the fake's
// organization member, so the Stytch mirror calls resolve.
func sideEffectMembershipDoc(role, status string) bson.D {
	now := time.Now()
	return bson.D{
		{Key: "_id", Value: "m-1"},
		{Key: "workspace_id", Value: "ws-1"},
		{Key: "user_id", Value: "user-1"},
		{Key: "role", Value: role},
		{Key: "status", Value: status},
		{Key: "authority_version", Value: int64(1)},
		{Key: "stytch_member_id", Value: "member-1"},
		{Key: "last_active_at", Value: now},
		{Key: "joined_at", Value: now},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
	}
}

func sideEffectUserDoc() bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "first_name", Value: "Amina"},
		{Key: "last_name", Value: "Wanjiru"},
		{Key: "email", Value: "member@example.com"},
		{Key: "status", Value: "active"},
	}
}

// countResponseIn mocks a CountDocuments result for a collection in mt's test DB.
// Named apart from retention_test.go's countResponse, which takes a fully
// qualified namespace string instead of resolving it from mt.
func countResponseIn(mt *mtest.T, coll string, n int32) bson.D {
	return mtest.CreateCursorResponse(1, mt.DB.Name()+"."+coll, mtest.FirstBatch, bson.D{{Key: "n", Value: n}})
}

func TestChangeRole_MirrorsRoleToStytchAndNotifiesMember(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("successful role change mirrors to Stytch and emails the member", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, spy := newSideEffectService(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			// ValidateRoleChange loads the target membership.
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, sideEffectMembershipDoc("hr", "active")),
			// The authoritative role write.
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			// The audit entry insert.
			mtest.CreateSuccessResponse(),
			// The workspace load for the side effects.
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
			// The user load for the notification.
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, sideEffectUserDoc()),
		)

		err := svc.ChangeRole(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleAdmin, "user-caller")
		if err != nil {
			mt.Fatalf("ChangeRole returned %v, want nil", err)
		}

		if got := fake.CallCount("UpdateMemberRoles"); got != 1 {
			mt.Errorf("UpdateMemberRoles call count = %d, want 1", got)
		}
		if got := spy.EmailCount(); got != 1 {
			mt.Fatalf("email count = %d, want 1", got)
		}
		email, _ := spy.LastEmail()
		if email.To != "member@example.com" {
			mt.Errorf("email sent to %q, want the affected member", email.To)
		}
		if !strings.Contains(email.Body, "hr") || !strings.Contains(email.Body, "admin") {
			mt.Errorf("email body %q should name both the previous and the new role", email.Body)
		}
	})
}

func TestChangeRole_MirrorsThePostWriteRoleNotThePreImage(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("the role sent to Stytch is the new role", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, _ := newSideEffectService(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, sideEffectMembershipDoc("hr", "active")),
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			mtest.CreateSuccessResponse(),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, sideEffectUserDoc()),
		)

		if err := svc.ChangeRole(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleFinance, "user-caller"); err != nil {
			mt.Fatalf("ChangeRole returned %v, want nil", err)
		}

		var mirrored []string
		for _, call := range fake.Calls {
			if call.Op != "UpdateMemberRoles" {
				continue
			}
			args, ok := call.Args.(map[string]any)
			if !ok {
				mt.Fatalf("UpdateMemberRoles args = %T, want map[string]any", call.Args)
			}
			roles, ok := args["roles"].([]string)
			if !ok {
				mt.Fatalf("UpdateMemberRoles roles = %T, want []string", args["roles"])
			}
			mirrored = roles
		}
		if len(mirrored) != 1 || mirrored[0] != string(RoleFinance) {
			mt.Errorf("mirrored roles = %v, want [%s]", mirrored, RoleFinance)
		}
	})
}

func TestChangeRole_StytchMirrorFailureDoesNotFailTheOperation(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("a failed mirror leaves the Fundis document alone and returns nil", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, spy := newSideEffectService(mt)
		fake.FailAt["UpdateMemberRoles"] = []stytch.FailSpec{
			{Attempt: 1, Kind: stytch.KindNotFound, Status: 404},
		}

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, ns("memberships"), mtest.FirstBatch, sideEffectMembershipDoc("hr", "active")),
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			mtest.CreateSuccessResponse(),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, sideEffectUserDoc()),
		)

		if err := svc.ChangeRole(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleAdmin, "user-caller"); err != nil {
			mt.Fatalf("ChangeRole returned %v, want nil despite the Stytch failure", err)
		}

		// No compensating write may follow a failed mirror: the only writes
		// issued are the role update and the audit insert.
		for _, ev := range mt.GetAllStartedEvents() {
			if ev.CommandName == "update" {
				mt.Errorf("a failed mirror must not trigger a rollback update, got %v", ev.Command)
			}
		}
		// The notification still goes out — it does not depend on Stytch.
		if got := spy.EmailCount(); got != 1 {
			mt.Errorf("email count = %d, want 1", got)
		}
	})
}

func TestSetStatus_RemovalRevokesSessionsDeletesMemberAndNotifies(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("removal revokes sessions, deletes the Stytch member, and emails the person", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, spy := newSideEffectService(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			// ValidateStatusChange counts active members (sole-member guard).
			countResponseIn(mt, "memberships", 3),
			// The authoritative status write.
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			// The audit entry insert.
			mtest.CreateSuccessResponse(),
			// The workspace load for the side effects.
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
			// The user load for the notification.
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, sideEffectUserDoc()),
		)

		err := svc.SetStatus(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleHR, MembershipRemoved, "user-caller")
		if err != nil {
			mt.Fatalf("SetStatus returned %v, want nil", err)
		}

		if got := fake.CallCount("RevokeMemberSessions"); got != 1 {
			mt.Errorf("RevokeMemberSessions call count = %d, want 1", got)
		}
		if got := fake.CallCount("DeleteMember"); got != 1 {
			mt.Errorf("DeleteMember call count = %d, want 1", got)
		}
		if got := spy.EmailCount(); got != 1 {
			mt.Fatalf("email count = %d, want 1", got)
		}
		email, _ := spy.LastEmail()
		if email.To != "member@example.com" {
			mt.Errorf("email sent to %q, want the removed member", email.To)
		}
	})
}

func TestSetStatus_SuspensionRevokesSessionsButRetainsTheStytchMember(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("suspension revokes sessions and does not delete the member (Req 13.11)", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, spy := newSideEffectService(mt)

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			mtest.CreateSuccessResponse(),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
		)

		err := svc.SetStatus(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleHR, MembershipSuspended, "user-caller")
		if err != nil {
			mt.Fatalf("SetStatus returned %v, want nil", err)
		}

		if got := fake.CallCount("RevokeMemberSessions"); got != 1 {
			mt.Errorf("RevokeMemberSessions call count = %d, want 1", got)
		}
		if got := fake.CallCount("DeleteMember"); got != 0 {
			mt.Errorf("DeleteMember call count = %d, want 0 — suspension retains the Stytch member", got)
		}
		// Req 13.6 scopes the notification to removal, not suspension.
		if got := spy.EmailCount(); got != 0 {
			mt.Errorf("email count = %d, want 0 for a suspension", got)
		}
	})
}

func TestSetStatus_ReactivationDoesNotRevokeSessions(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("reinstating a membership performs no session revocation", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, _ := newSideEffectService(mt)

		mt.AddMockResponses(
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "suspended")}),
			mtest.CreateSuccessResponse(),
		)

		err := svc.SetStatus(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleHR, MembershipActive, "user-caller")
		if err != nil {
			mt.Fatalf("SetStatus returned %v, want nil", err)
		}

		if got := fake.CallCount("RevokeMemberSessions"); got != 0 {
			mt.Errorf("RevokeMemberSessions call count = %d, want 0 on reactivation", got)
		}
		if got := fake.CallCount("DeleteMember"); got != 0 {
			mt.Errorf("DeleteMember call count = %d, want 0 on reactivation", got)
		}
	})
}

func TestSetStatus_StytchDeleteFailureFlagsPendingDeleteAndStillReturnsNil(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("a failed DeleteMember sets stytch_member_delete_pending for the retry job (Req 13.4)", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, _ := newSideEffectService(mt)
		fake.FailAt["DeleteMember"] = []stytch.FailSpec{
			{Attempt: 1, Kind: stytch.KindNotFound, Status: 404},
		}

		ns := func(coll string) string { return mt.DB.Name() + "." + coll }
		mt.AddMockResponses(
			countResponseIn(mt, "memberships", 3),
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			mtest.CreateSuccessResponse(),
			mtest.CreateCursorResponse(0, ns("workspaces"), mtest.FirstBatch, workspaceDoc("ws-1")),
			// The pending-delete flag write.
			mtest.CreateSuccessResponse(),
			mtest.CreateCursorResponse(0, ns("users"), mtest.FirstBatch, sideEffectUserDoc()),
		)

		err := svc.SetStatus(context.Background(), "ws-1", "m-1", "m-caller", RoleOwner, RoleHR, MembershipRemoved, "user-caller")
		if err != nil {
			mt.Fatalf("SetStatus returned %v, want nil despite the Stytch delete failure", err)
		}

		var flagged bool
		for _, ev := range mt.GetAllStartedEvents() {
			if ev.CommandName == "update" && strings.Contains(ev.Command.String(), "stytch_member_delete_pending") {
				flagged = true
			}
		}
		if !flagged {
			mt.Error("expected an update setting stytch_member_delete_pending so RetryPendingStytchDeletes has something to retry")
		}
	})
}

func TestSetStatusFromWebhook_DoesNotMirrorBackToStytch(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("a Stytch-initiated status change performs no outbound Stytch calls", func(mt *mtest.T) {
		useSideEffectTestMongo(mt)
		svc, fake, spy := newSideEffectService(mt)

		mt.AddMockResponses(
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: sideEffectMembershipDoc("hr", "active")}),
			mtest.CreateSuccessResponse(),
		)

		err := svc.SetStatusFromWebhook(context.Background(), "m-1", MembershipRemoved, time.Now())
		if err != nil {
			mt.Fatalf("SetStatusFromWebhook returned %v, want nil", err)
		}

		if got := len(fake.Calls); got != 0 {
			mt.Errorf("webhook-driven change made %d Stytch calls, want 0 — mirroring back would be a feedback loop", got)
		}
		if got := spy.EmailCount(); got != 0 {
			mt.Errorf("email count = %d, want 0 for a webhook-driven change", got)
		}
	})
}
