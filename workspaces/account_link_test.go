package workspaces

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// Req 5.8 / 16.4: provisioning a workspace must record the Account_Link that
// names the users document and the authenticating Stytch Member. Resolver.Resolve
// rejects any session whose user holds no link (errUnlinkedMember), so a
// workspace provisioned without one is unreachable through the ordinary
// session path — the whole signup flow would dead-end at the first
// authenticated request.
func TestEnsureAccountLink_InsertsLink(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("inserts the link for a freshly provisioned workspace", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		w := &WorkspaceService{clock: newTestClock()}
		err := w.ensureAccountLink(context.Background(), "user-1", "member-1", "org-1", "user@example.com")
		if err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}
	})
}

// A retried provision must not fail: exactly one Account_Link per users
// document is the invariant (uniq_account_link_user), so hitting that index
// means the invariant already holds.
func TestEnsureAccountLink_DuplicateKeyIsNotAnError(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("duplicate key is treated as success", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		mt.AddMockResponses(mtest.CreateWriteErrorsResponse(mtest.WriteError{
			Index:   0,
			Code:    11000,
			Message: "E11000 duplicate key error collection: account_links index: uniq_account_link_user",
		}))

		w := &WorkspaceService{clock: newTestClock()}
		err := w.ensureAccountLink(context.Background(), "user-1", "member-1", "org-1", "user@example.com")
		if err != nil {
			mt.Fatalf("duplicate key should not surface as an error, got: %v", err)
		}
	})
}

// A missing member ID means there is nothing to link against; the caller's own
// validation owns that case, so this must not write a malformed link.
func TestEnsureAccountLink_SkipsWhenIdentityIncomplete(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("no write is attempted without both identifiers", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		// Deliberately zero mock responses: any attempted write would fail
		// with "no responses remaining".

		w := &WorkspaceService{clock: newTestClock()}
		if err := w.ensureAccountLink(context.Background(), "user-1", "", "org-1", "user@example.com"); err != nil {
			mt.Fatalf("missing member ID should be a no-op, got: %v", err)
		}
		if err := w.ensureAccountLink(context.Background(), "", "member-1", "org-1", "user@example.com"); err != nil {
			mt.Fatalf("missing user ID should be a no-op, got: %v", err)
		}
	})
}

// Guards the field mapping: a link written with the wrong shape would satisfy
// the insert but never match Resolver.Resolve's {user_id: ...} lookup.
func TestEnsureAccountLink_WritesExpectedFields(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("link carries user_id, member, org, email, and method", func(mt *mtest.T) {
		useResolverTestMongo(mt)
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		w := &WorkspaceService{clock: newTestClock()}
		if err := w.ensureAccountLink(context.Background(), "user-1", "member-1", "org-1", "user@example.com"); err != nil {
			mt.Fatalf("unexpected error: %v", err)
		}

		started := mt.GetStartedEvent()
		if started == nil {
			mt.Fatal("expected an insert command to have been sent")
		}
		if started.CommandName != "insert" {
			mt.Fatalf("CommandName = %q, want %q", started.CommandName, "insert")
		}

		docs, err := started.Command.LookupErr("documents")
		if err != nil {
			mt.Fatalf("insert command carried no documents: %v", err)
		}
		values, err := docs.Array().Values()
		if err != nil || len(values) != 1 {
			mt.Fatalf("expected exactly one inserted document, got %d (err=%v)", len(values), err)
		}

		var link bson.M
		if err := bson.Unmarshal(values[0].Document(), &link); err != nil {
			mt.Fatalf("could not decode inserted document: %v", err)
		}

		for field, want := range map[string]string{
			"user_id":                "user-1",
			"stytch_member_id":       "member-1",
			"stytch_organization_id": "org-1",
			"linked_email":           "user@example.com",
			"link_method":            "signup",
		} {
			if got, _ := link[field].(string); got != want {
				mt.Errorf("%s = %q, want %q", field, got, want)
			}
		}
	})
}
