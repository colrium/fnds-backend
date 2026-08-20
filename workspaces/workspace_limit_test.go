package workspaces

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// An employer belongs to one workspace, for now. The cap lives in
// CreateAdditional rather than in the handler so any caller is covered, and it
// counts rather than refusing outright: the same method serves an employer who
// belongs to no workspace at all (post_auth's needs_workspace), who must still be
// able to create their first.
//
// CreateForOnboarding is deliberately uncapped — it IS the first workspace — and
// capping it would block every sign-up, so that asymmetry is asserted too.

func withMockMongo(t *testing.T) *mtest.T {
	t.Helper()
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).ShareClient(true).DatabaseName("test"))
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	t.Cleanup(func() { configs.MI = previous })
	return mt
}

func membershipCountResponse(n int32) bson.D {
	return mtest.CreateCursorResponse(1, "test.memberships", mtest.FirstBatch, bson.D{
		{Key: "n", Value: n},
	})
}

func TestCreateAdditionalRejectsASecondWorkspace(t *testing.T) {
	mt := withMockMongo(t)
	svc, fake := newProvisioningTestService(t)

	mt.AddMockResponses(membershipCountResponse(1))

	_, err := svc.CreateAdditional(context.Background(), "user-1", "Second Workspace", "", "")
	if err == nil {
		t.Fatal("expected the workspace limit to reject this")
	}

	var wsErr *WorkspaceError
	if !errors.As(err, &wsErr) {
		t.Fatalf("error is %T (%v), want *WorkspaceError", err, err)
	}
	if wsErr.Status != 409 || wsErr.Code != "workspace_limit_reached" {
		t.Errorf("got %d/%s, want 409/workspace_limit_reached", wsErr.Status, wsErr.Code)
	}

	// The limit must be refused before Stytch is touched, or a rejected attempt
	// would still leave an orphan Organization behind.
	for _, op := range []string{"CreateOrganization", "CreateOrganizationFromDiscovery", "CreateMember"} {
		if n := fake.CallCount(op); n != 0 {
			t.Errorf("%s called %d times on a rejected create, want 0", op, n)
		}
	}
}

func TestCreateAdditionalAllowsTheFirstWorkspace(t *testing.T) {
	mt := withMockMongo(t)
	svc, _ := newProvisioningTestService(t)

	// Zero memberships — the needs_workspace employer. What happens after the cap
	// is irrelevant here (it proceeds to Stytch and then Mongo, which the mock is
	// not scripted for); the only claim is that the limit did not reject it.
	mt.AddMockResponses(membershipCountResponse(0))

	_, err := svc.CreateAdditional(context.Background(), "user-1", "First Workspace", "", "")

	var wsErr *WorkspaceError
	if errors.As(err, &wsErr) && wsErr.Code == "workspace_limit_reached" {
		t.Fatal("the first workspace must not be blocked by the limit")
	}
}
