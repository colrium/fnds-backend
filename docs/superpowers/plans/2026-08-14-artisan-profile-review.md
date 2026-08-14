# Artisan Profile Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a standalone admin approval gate on an artisan's profile — separate from the existing assessment journey pipeline — that must be `approved` before the artisan can start an assessment journey or apply for jobs/vacancies.

**Architecture:** A new `profileapproval` package (leaf, like the existing `assessments` package) owns the `pending_review`/`approved`/`rejected` state machine on four new `users.User` fields. `assessments.StartJourney` and `jobrequests.HandleCreateJobApplication` each gain one extra check against `user.ProfileStatus`. A tiny standalone helper in the `users` package (not `profileapproval`, to avoid an import cycle — see Task 2) reverts `approved` back to `pending_review` whenever `profiles.UpdateProfile` runs. New `adminapi` routes expose the review queue and approve/reject actions; a new `admin/` frontend page consumes them.

**Tech Stack:** Go 1.21+, Echo v4, MongoDB (`go.mongodb.org/mongo-driver` v1.17, tested via `mtest`), React 18 + TypeScript + Vite + TailwindCSS + TanStack Query (`admin/`).

**Spec:** `backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md`

## Global Constraints

- No case conversion in the `admin/` frontend — backend JSON is already `snake_case`; TypeScript types mirror the Go struct's `json` tags verbatim (see `AdminUser` in `admin/src/types/user.ts` for the existing precedent).
- All new Mongo writes go through `users.UpdateUser(id string, update bson.M)` — never a raw `UpdateOne` call from another package.
- All new admin HTTP routes are registered on the existing `adminGroup` in `backend/main.go` (`JWTAuth` + `IsAdmin`), via `adminapi.RegisterAdminRoutes` — no new middleware.
- Every new Go file must pass `gofmt -l` (zero output) and the package must pass `go vet`.
- Every backend task's tests use `go.mongodb.org/mongo-driver/mongo/integration/mtest`, following the exact patterns already established in `adminapi/users_test.go` and `adminapi/admin_group_test.go` — reuse those files' helpers (`useAdminMockMongo`, `adminHandlerContext`, `queueUserLookup`, `buildAdminGroup`) rather than redefining them.
- `CountDocuments` under mongo-driver v1.17 issues an aggregate command internally, so any mock for it must be `mtest.CreateCursorResponse`, never `mtest.CreateSuccessResponse` — this was discovered empirically in the prior admin-dashboard work (`adminapi/users_test.go`'s comment above its second `AddMockResponses` call) and applies again here.
- Frontend: no automated tests for `admin/` (standing decision from the admin-dashboard plan, `admin/.planning/plans/admin-dashboard.md` §8). `npm run build` (`tsc -b && vite build`) and `npm run lint` are the enforced gates instead.
- Commit after each task, not each step within a task.

---

### Task 1: Add `ProfileStatus` fields and constants to `users.User`

**Files:**
- Modify: `backend/users/model.go:14-49` (const block and `User` struct)
- Create: `backend/users/profile_status_test.go`

**Interfaces:**
- Produces: `users.ProfileStatusNotSubmitted`, `users.ProfileStatusPendingReview`, `users.ProfileStatusApproved`, `users.ProfileStatusRejected` (all `string` constants); `User.ProfileStatus string`, `User.ProfileSubmittedAt *time.Time`, `User.ProfileReviewedAt *time.Time`, `User.ProfileReviewedBy string`, `User.ProfileRejectReason string`; `users.EffectiveProfileStatus(u *User) string`.

- [ ] **Step 1: Write the failing test for `EffectiveProfileStatus`**

Create `backend/users/profile_status_test.go`:

```go
package users

import "testing"

func TestEffectiveProfileStatusDefaultsEmptyToNotSubmitted(t *testing.T) {
	u := &User{ProfileStatus: ""}
	if got := EffectiveProfileStatus(u); got != ProfileStatusNotSubmitted {
		t.Errorf("EffectiveProfileStatus() = %q, want %q", got, ProfileStatusNotSubmitted)
	}
}

func TestEffectiveProfileStatusPassesThroughSetValue(t *testing.T) {
	u := &User{ProfileStatus: ProfileStatusApproved}
	if got := EffectiveProfileStatus(u); got != ProfileStatusApproved {
		t.Errorf("EffectiveProfileStatus() = %q, want %q", got, ProfileStatusApproved)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./users/... -run TestEffectiveProfileStatus -v`
Expected: FAIL with `undefined: EffectiveProfileStatus` (and `undefined: ProfileStatusNotSubmitted`, `undefined: ProfileStatusApproved`)

- [ ] **Step 3: Add the constants, struct fields, and helper**

In `backend/users/model.go`, change the const block at line 15-18 from:

```go
const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusSuspended = "suspended"
)
```

to:

```go
const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusSuspended = "suspended"

	// ProfileStatus values gate whether an artisan may start an assessment
	// journey or apply for jobs. See backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md.
	ProfileStatusNotSubmitted  = "not_submitted"
	ProfileStatusPendingReview = "pending_review"
	ProfileStatusApproved      = "approved"
	ProfileStatusRejected      = "rejected"
)
```

In the `User` struct (currently ending at line 49 with `UpdatedAt time.Time`), add five fields right before `CreatedAt`:

```go
	ProfileStatus       string     `json:"profile_status" bson:"profile_status"`
	ProfileSubmittedAt  *time.Time `json:"profile_submitted_at,omitempty" bson:"profile_submitted_at,omitempty"`
	ProfileReviewedAt   *time.Time `json:"profile_reviewed_at,omitempty" bson:"profile_reviewed_at,omitempty"`
	ProfileReviewedBy   string     `json:"profile_reviewed_by,omitempty" bson:"profile_reviewed_by,omitempty"`
	ProfileRejectReason string     `json:"profile_reject_reason,omitempty" bson:"profile_reject_reason,omitempty"`
	CreatedAt          time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at" bson:"updated_at"`
```

At the end of `backend/users/model.go`, add:

```go
// EffectiveProfileStatus returns u.ProfileStatus, treating an empty stored
// value (the Go zero value, which should not occur post-migration but is a
// legitimate state for any user created before this field existed and not
// yet backfilled) as ProfileStatusNotSubmitted. Every comparison against
// ProfileStatus should go through this function rather than comparing the
// raw field directly, so callers never have to repeat the empty-string
// fallback themselves.
func EffectiveProfileStatus(u *User) string {
	if u == nil || u.ProfileStatus == "" {
		return ProfileStatusNotSubmitted
	}
	return u.ProfileStatus
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./users/... -run TestEffectiveProfileStatus -v`
Expected: PASS (both subtests)

- [ ] **Step 5: Run `gofmt` and the full `users` package test suite**

Run: `cd backend && gofmt -w users/ && go test ./users/... -v`
Expected: All PASS, no regressions in existing `users` tests.

- [ ] **Step 6: Commit**

```bash
cd backend
git add users/model.go users/profile_status_test.go
git commit -m "feat(users): add profile_status fields for the artisan profile review gate"
```

---

### Task 2: `users.RevertProfileStatusToPendingIfApproved` — the edit-triggers-re-review hook

**Files:**
- Create: `backend/users/profile_status.go`
- Create: `backend/users/profile_status_reversion_test.go`

**Interfaces:**
- Consumes: `users.UpdateUser(id string, update bson.M) error` (existing, `users/model.go:211`).
- Produces: `users.RevertProfileStatusToPendingIfApproved(ctx context.Context, user *User) error`.

This lives in `users`, not `profileapproval`, on purpose: `profiles/handlers.go` needs to call it after every profile edit, but `profileapproval` will need to import `profiles` (Task 4, for `GetFullProfile`/`GetProfileCompletion`). If this hook lived in `profileapproval` and `profiles` called it, that would be `profiles → profileapproval → profiles`, an import cycle Go refuses to compile. `users` imports nothing domain-side today (confirmed: its only import is `backend/configs`), so a `users`-only helper that just reads/writes `User` fields has no such problem.

- [ ] **Step 1: Write the failing tests**

Create `backend/users/profile_status_reversion_test.go`:

```go
package users

import (
	"context"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useUsersPackageMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.T.Cleanup(func() { configs.MI = previous })
}

func TestRevertProfileStatusToPendingIfApprovedNoOpWhenNotApproved(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("not submitted", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		// No mock response queued: a real UpdateUser call here would panic
		// with "no responses remaining", proving this is a true no-op.
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusNotSubmitted}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
	})
	mt.Run("pending review", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusPendingReview}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
	})
	mt.Run("rejected", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusRejected}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
	})
}

func TestRevertProfileStatusToPendingIfApprovedRevertsWhenApproved(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("approved reverts to pending_review", func(mt *mtest.T) {
		useUsersPackageMockMongo(mt)
		mt.AddMockResponses(mtest.CreateSuccessResponse(
			bson.E{Key: "n", Value: 1},
			bson.E{Key: "nModified", Value: 1},
		))
		user := &User{ID: "user-1", ProfileStatus: ProfileStatusApproved}
		if err := RevertProfileStatusToPendingIfApproved(context.Background(), user); err != nil {
			mt.Fatalf("RevertProfileStatusToPendingIfApproved() error = %v", err)
		}
		event := mt.GetStartedEvent()
		if event == nil || event.CommandName != "update" {
			mt.Fatalf("started event = %#v, want an update command", event)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./users/... -run TestRevertProfileStatusToPendingIfApproved -v`
Expected: FAIL with `undefined: RevertProfileStatusToPendingIfApproved`

- [ ] **Step 3: Implement**

Create `backend/users/profile_status.go`:

```go
package users

import "context"

// RevertProfileStatusToPendingIfApproved is called after every successful
// profile edit (see profiles.HandleUpdateProfile). An admin's prior approval
// covered a specific snapshot of profile content; once that content changes,
// the approval no longer speaks to what is now on file, so it must be
// re-checked. It is a no-op for every other status: pending_review has
// nothing to revert from, rejected requires the artisan to explicitly
// resubmit (see profileapproval.SubmitForReview) rather than being silently
// re-queued on every edit.
func RevertProfileStatusToPendingIfApproved(ctx context.Context, user *User) error {
	if user == nil || user.ProfileStatus != ProfileStatusApproved {
		return nil
	}
	return UpdateUser(user.ID, map[string]interface{}{
		"profile_status": ProfileStatusPendingReview,
	})
}
```

Note: `UpdateUser`'s signature takes `bson.M`, which is `map[string]interface{}` under the hood (`go.mongodb.org/mongo-driver/bson.M` is defined as exactly that type alias), so the literal above satisfies it without importing `bson` in this file.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./users/... -run TestRevertProfileStatusToPendingIfApproved -v`
Expected: PASS (all 4 subtests)

- [ ] **Step 5: Run `gofmt` and the full `users` package suite**

Run: `cd backend && gofmt -w users/ && go test ./users/... -v`
Expected: All PASS.

- [ ] **Step 6: Commit**

```bash
cd backend
git add users/profile_status.go users/profile_status_reversion_test.go
git commit -m "feat(users): revert approved profile_status to pending_review on edit"
```

---

### Task 3: Backfill migration script

**Files:**
- Create: `backend/scripts/backfillprofilestatus/main.go`
- Create: `backend/scripts/backfillprofilestatus/main_test.go`

**Interfaces:**
- Consumes: `users.ProfileStatusApproved`, `users.ProfileStatusNotSubmitted`, `users.UpdateUser`, `profiles.GetFullProfile`, `profiles.GetProfileCompletion` (all existing/from Task 1).
- Produces: `backfillprofilestatus.ComputeStatus(complete bool) string` (the pure, unit-testable part); a `main()` that is not itself unit-tested (matches `scripts/importusers/main.go`'s convention — the DB-wiring `main` is a thin wrapper, exercised only manually).

This must run before Task 4 lands, in principle (Task 4 makes `StartJourney` and job applications start enforcing `ProfileStatus == approved`), but since this is local development and no production data exists yet in this environment, running it is a manual step documented here rather than a blocking automated step. Flagged explicitly so it is not forgotten before deploying.

- [ ] **Step 1: Write the failing test for the pure decision function**

Create `backend/scripts/backfillprofilestatus/main_test.go`:

```go
package main

import (
	"testing"

	"backend/users"
)

func TestComputeStatus(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		want     string
	}{
		{name: "complete profile backfills to approved", complete: true, want: users.ProfileStatusApproved},
		{name: "incomplete profile backfills to not_submitted", complete: false, want: users.ProfileStatusNotSubmitted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeStatus(tt.complete); got != tt.want {
				t.Errorf("ComputeStatus(%v) = %q, want %q", tt.complete, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./scripts/backfillprofilestatus/... -v`
Expected: FAIL with `undefined: ComputeStatus`

- [ ] **Step 3: Implement**

Create `backend/scripts/backfillprofilestatus/main.go`:

```go
// Command backfillprofilestatus is a one-time backfill for the new
// users.ProfileStatus field (see backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md).
//
// Rules:
//   - Every artisan with a currently-complete profile (per
//     profiles.GetProfileCompletion) is grandfathered in as "approved" —
//     this feature must not retroactively block artisans already using the
//     platform, including those already mid-assessment-journey or already
//     certified.
//   - Every other artisan (incomplete profile, including one with no
//     profiles-collection row at all) is set to "not_submitted".
//   - Only touches users whose profile_status is currently empty, so
//     running this twice is safe and does not clobber a status set by real
//     usage after the first run.
//
// Usage:
//
//	go run ./scripts/backfillprofilestatus            # dry run, prints counts only
//	go run ./scripts/backfillprofilestatus -write      # actually writes
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"backend/configs"
	"backend/profiles"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
)

// ComputeStatus is the pure decision rule, extracted for unit testing
// without a database.
func ComputeStatus(complete bool) string {
	if complete {
		return users.ProfileStatusApproved
	}
	return users.ProfileStatusNotSubmitted
}

func main() {
	write := flag.Bool("write", false, "actually write changes (default is dry-run)")
	flag.Parse()

	configs.ConnectDB()
	ctx := context.Background()

	cursor, err := configs.MI.DB.Collection("users").Find(ctx, bson.M{
		"user_type":      "artisan",
		"profile_status": bson.M{"$in": bson.A{"", nil}},
	})
	if err != nil {
		log.Fatalf("querying artisans: %v", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var toApprove, toNotSubmitted int
	for cursor.Next(ctx) {
		var u users.User
		if err := cursor.Decode(&u); err != nil {
			log.Printf("decoding user: %v", err)
			continue
		}
		profile, err := profiles.GetFullProfile(ctx, u.ID)
		if err != nil {
			log.Printf("loading profile for user %s: %v", u.ID, err)
			continue
		}
		status := ComputeStatus(profiles.GetProfileCompletion(profile).Complete)
		if status == users.ProfileStatusApproved {
			toApprove++
		} else {
			toNotSubmitted++
		}
		if *write {
			if err := users.UpdateUser(u.ID, bson.M{"profile_status": status}); err != nil {
				log.Printf("updating user %s: %v", u.ID, err)
			}
		}
	}

	mode := "DRY RUN"
	if *write {
		mode = "WROTE"
	}
	fmt.Printf("%s: %d users -> approved, %d users -> not_submitted\n", mode, toApprove, toNotSubmitted)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./scripts/backfillprofilestatus/... -v`
Expected: PASS

- [ ] **Step 5: Build check and `gofmt`**

Run: `cd backend && gofmt -w scripts/backfillprofilestatus/ && go build ./scripts/backfillprofilestatus/...`
Expected: builds cleanly, no output from gofmt.

- [ ] **Step 6: Commit**

```bash
cd backend
git add scripts/backfillprofilestatus/
git commit -m "feat(scripts): add profile_status backfill for existing artisans"
```

---

### Task 4: `profileapproval` package — submit, approve, reject

**Files:**
- Create: `backend/profileapproval/service.go`
- Create: `backend/profileapproval/service_test.go`

**Interfaces:**
- Consumes: `profiles.GetFullProfile(ctx, userID) (*profiles.FullProfile, error)`, `profiles.GetProfileCompletion(profile) profiles.ProfileCompletion` (both existing); `users.UpdateUser`, `users.EffectiveProfileStatus`, `users.ProfileStatus*` constants (Task 1).
- Produces: `profileapproval.ErrProfileIncomplete`, `profileapproval.ErrAlreadyPending`, `profileapproval.ErrNotPending`, `profileapproval.ErrReasonRequired` (all `error`); `profileapproval.SubmitForReview(ctx, userID string) error`; `profileapproval.Approve(ctx, userID, reviewedBy string) error`; `profileapproval.Reject(ctx, userID, reviewedBy, reason string) error`.

This package deliberately does **not** call `notifications` itself — every existing caller of `notifications.NewDefaultService()` in this codebase (`jobrequests/routes.go:220,622,933`, `quotations/routes.go:78,81`) calls it directly from the HTTP handler, not from the domain/service layer, and `notifications.NewDefaultService()` panics if `ConfigureDefaultStore` was never called (true in every unit test process). Keeping `profileapproval`'s functions notification-free means their tests never risk that panic. The `adminapi` handlers in Task 6 own firing the notification, exactly like `jobrequests.HandleDecideJobApplication` does today.

- [ ] **Step 1: Write the failing tests**

Create `backend/profileapproval/service_test.go`:

```go
package profileapproval

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useProfileApprovalMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.T.Cleanup(func() { configs.MI = previous })
}

// completeArtisanDocument returns a users collection document for an
// artisan whose profile is complete enough that profiles.GetProfileCompletion
// reports Complete: true, so tests can drive SubmitForReview's completeness
// gate without needing five separate empty-collection round-trips beyond
// what profiles.GetFullProfile always makes.
func userDocument(id, profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: id},
		{Key: "user_type", Value: "artisan"},
		{Key: "profile_status", Value: profileStatus},
	}
}

func completeProfileDocument(userID string) bson.D {
	return bson.D{
		{Key: "user_id", Value: userID},
		{Key: "id_number", Value: "12345678"},
		{Key: "date_of_birth", Value: "1990-01-01"},
		{Key: "gender", Value: "female"},
		{Key: "address", Value: "123 Main St"},
		{Key: "skills", Value: bson.A{"plumbing"}},
		{Key: "skill_acquisition_mode", Value: "apprenticeship"},
		{Key: "next_of_kin_name", Value: "Jane Doe"},
		{Key: "next_of_kin_phone_number", Value: "+254700000000"},
		{Key: "next_of_kin_relationship", Value: "sister"},
		{Key: "locations", Value: bson.A{"Nairobi"}},
	}
}

func completeReferenceDocument(userID string) bson.D {
	return bson.D{
		{Key: "_id", Value: "ref-1"},
		{Key: "user_id", Value: userID},
		{Key: "name", Value: "Referee Name"},
		{Key: "phone_number", Value: "+254711111111"},
	}
}

func completeWorkingHourDocument(userID string) bson.D {
	return bson.D{
		{Key: "_id", Value: "wh-1"},
		{Key: "user_id", Value: userID},
		{Key: "day", Value: "monday"},
		{Key: "opening_time", Value: "08:00"},
		{Key: "closing_time", Value: "17:00"},
	}
}

// queueCompleteProfileLookups queues every Mongo round-trip
// profiles.GetFullProfile makes, in the exact order it makes them, so the
// resulting profile is Complete per profiles.GetProfileCompletion:
// users.GetUserByID, profiles.GetProfileByUserID, then Find on
// working_hours, past_employers, language_proficiencies,
// competency_references in that order.
func queueCompleteProfileLookups(mt *mtest.T, userID, profileStatus string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument(userID, profileStatus)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, completeProfileDocument(userID)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch, completeWorkingHourDocument(userID)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch, completeReferenceDocument(userID)))
}

func queueIncompleteProfileLookups(mt *mtest.T, userID, profileStatus string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument(userID, profileStatus)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch)) // no profiles row
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch))
}

func TestSubmitForReviewIncompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("incomplete", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		queueIncompleteProfileLookups(mt, "user-1", "")

		err := SubmitForReview(context.Background(), "user-1")
		if !errors.Is(err, ErrProfileIncomplete) {
			mt.Errorf("SubmitForReview() error = %v, want ErrProfileIncomplete", err)
		}
	})
}

func TestSubmitForReviewAlreadyPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("already pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		// Only the initial users.GetUserByID lookup is needed: the
		// already-pending check happens before GetFullProfile runs.
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "pending_review")))

		err := SubmitForReview(context.Background(), "user-1")
		if !errors.Is(err, ErrAlreadyPending) {
			mt.Errorf("SubmitForReview() error = %v, want ErrAlreadyPending", err)
		}
	})
}

func TestSubmitForReviewHappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete profile, not pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "")))
		queueCompleteProfileLookups(mt, "user-1", "")
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

		if err := SubmitForReview(context.Background(), "user-1"); err != nil {
			mt.Fatalf("SubmitForReview() error = %v", err)
		}
		event := mt.GetStartedEvent()
		if event == nil || event.CommandName != "update" {
			mt.Fatalf("started event = %#v, want an update command", event)
		}
	})
}

func TestApproveNotPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("not pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "approved")))

		err := Approve(context.Background(), "user-1", "admin-1")
		if !errors.Is(err, ErrNotPending) {
			mt.Errorf("Approve() error = %v, want ErrNotPending", err)
		}
	})
}

func TestApproveHappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("pending to approved", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "pending_review")))
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

		if err := Approve(context.Background(), "user-1", "admin-1"); err != nil {
			mt.Fatalf("Approve() error = %v", err)
		}
	})
}

func TestRejectRequiresReason(t *testing.T) {
	if err := Reject(context.Background(), "user-1", "admin-1", "   "); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("Reject() error = %v, want ErrReasonRequired", err)
	}
}

func TestRejectNotPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("not pending", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "rejected")))

		err := Reject(context.Background(), "user-1", "admin-1", "missing documents")
		if !errors.Is(err, ErrNotPending) {
			mt.Errorf("Reject() error = %v, want ErrNotPending", err)
		}
	})
}

func TestRejectHappyPath(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("pending to rejected", func(mt *mtest.T) {
		useProfileApprovalMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, userDocument("user-1", "pending_review")))
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

		if err := Reject(context.Background(), "user-1", "admin-1", "missing documents"); err != nil {
			mt.Fatalf("Reject() error = %v", err)
		}
	})
}

var _ = time.Now // silence unused import if a future edit removes the time.Time usage above
```

Remove the trailing `var _ = time.Now` line and the `"time"` import if unused once you finish
writing this file — it is a placeholder guard only needed if you trim the test bodies above
during review; as written, `time` is unused, so delete both the import and this line before
running the tests.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./profileapproval/... -v`
Expected: FAIL with `no Go files in .../profileapproval` (package does not exist yet)

- [ ] **Step 3: Implement**

Create `backend/profileapproval/service.go`:

```go
// Package profileapproval implements the standalone admin approval gate on
// an artisan's profile: pending_review -> approved | rejected. It is
// deliberately independent of the assessments.Journey pipeline (see
// backend/docs/superpowers/specs/2026-08-14-artisan-profile-review-design.md
// section 1 for why the two cannot share state) and does not call
// notifications itself — see the file-level comment in this plan's Task 4
// header for why that responsibility stays in the HTTP handler layer.
package profileapproval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"backend/profiles"
	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
)

var (
	ErrProfileIncomplete = errors.New("complete your profile before submitting it for review")
	ErrAlreadyPending    = errors.New("your profile is already pending review")
	ErrNotPending        = errors.New("this profile is not currently pending review")
	ErrReasonRequired    = errors.New("a rejection reason is required")
)

// SubmitForReview moves an artisan's profile into pending_review. The
// profile must be complete (the same completeness gate assessments.StartJourney
// and jobrequests' job-application handler already use, so "complete" means
// one thing across the product) and must not already be pending.
func SubmitForReview(ctx context.Context, userID string) error {
	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if users.EffectiveProfileStatus(user) == users.ProfileStatusPendingReview {
		return ErrAlreadyPending
	}

	profile, err := profiles.GetFullProfile(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading profile: %w", err)
	}
	if !profiles.GetProfileCompletion(profile).Complete {
		return ErrProfileIncomplete
	}

	now := time.Now().UTC()
	return users.UpdateUser(userID, bson.M{
		"profile_status":        users.ProfileStatusPendingReview,
		"profile_submitted_at":  now,
		"profile_reject_reason": "",
	})
}

// Approve moves a pending_review profile to approved. reviewedBy is the
// acting admin's user ID.
func Approve(ctx context.Context, userID, reviewedBy string) error {
	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if users.EffectiveProfileStatus(user) != users.ProfileStatusPendingReview {
		return ErrNotPending
	}

	now := time.Now().UTC()
	return users.UpdateUser(userID, bson.M{
		"profile_status":      users.ProfileStatusApproved,
		"profile_reviewed_at": now,
		"profile_reviewed_by": reviewedBy,
	})
}

// Reject moves a pending_review profile to rejected, recording reason so
// the artisan knows what to fix. The artisan may edit their profile and call
// SubmitForReview again — rejection is not final.
func Reject(ctx context.Context, userID, reviewedBy, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrReasonRequired
	}

	user, err := users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if users.EffectiveProfileStatus(user) != users.ProfileStatusPendingReview {
		return ErrNotPending
	}

	now := time.Now().UTC()
	return users.UpdateUser(userID, bson.M{
		"profile_status":        users.ProfileStatusRejected,
		"profile_reviewed_at":   now,
		"profile_reviewed_by":   reviewedBy,
		"profile_reject_reason": reason,
	})
}
```

Note `Reject` checks `reason` before doing any Mongo lookup at all — this is why
`TestRejectRequiresReason` above needs no `mtest` setup: it fails fast, with zero
database round-trips.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./profileapproval/... -v`
Expected: PASS (all 7 test functions)

If `TestSubmitForReviewHappyPath` or `TestApproveHappyPath`/`TestRejectHappyPath` fail with
"no responses remaining" or an unmarshal error, double check the mock response order in
`queueCompleteProfileLookups`/`queueIncompleteProfileLookups` exactly matches the six-call
sequence inside `profiles.GetFullProfile` (`backend/profiles/service.go:143-208`): `users`
find, `profiles` find, then `working_hours`, `past_employers`, `language_proficiencies`,
`competency_references` finds, in that order — `mtest`'s mock queue is strictly ordered
across the whole shared client regardless of which collection each call targets.

- [ ] **Step 5: Run `gofmt` and `go vet`**

Run: `cd backend && gofmt -w profileapproval/ && go vet ./profileapproval/...`
Expected: gofmt produces no output, vet is clean.

- [ ] **Step 6: Commit**

```bash
cd backend
git add profileapproval/
git commit -m "feat(profileapproval): add submit/approve/reject state machine"
```

---

### Task 5: Gate `assessments.StartJourney` and `jobrequests.HandleCreateJobApplication`

**Files:**
- Modify: `backend/assessments/service.go:44-51` (var block), `backend/assessments/service.go:109-137` (`StartJourney`)
- Modify: `backend/assessments/handlers.go:63-83` (`HandleStartJourney`)
- Modify: `backend/jobrequests/routes.go:528-547` (`HandleCreateJobApplication`)
- Create: `backend/assessments/start_journey_test.go`
- Modify: `backend/jobrequests/jobrequests_test.go` (or the file already covering `HandleCreateJobApplication`, if one exists — check with `grep -rn "HandleCreateJobApplication" backend/jobrequests/*_test.go` first; if none exists, create `backend/jobrequests/job_application_profile_status_test.go`)

**Interfaces:**
- Consumes: `users.ProfileStatusApproved`, `users.EffectiveProfileStatus` (Task 1); nothing from Task 4 (this task reads `user.ProfileStatus` directly off the already-loaded `*profiles.FullProfile`/`*users.User`, it does not call into `profileapproval`).
- Produces: `assessments.ErrProfileNotApproved` (`error`).

- [ ] **Step 1: Write the failing test for `StartJourney`**

First check for an existing `StartJourney` test:

Run: `cd backend && grep -rln "StartJourney" assessments/*_test.go`

If that prints nothing (expected — no test currently exercises `StartJourney`), create
`backend/assessments/start_journey_test.go`:

```go
package assessments

import (
	"context"
	"errors"
	"testing"

	"backend/configs"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useStartJourneyMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.T.Cleanup(func() { configs.MI = previous })
}

func startJourneyUserDocument(profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "user_type", Value: "artisan"},
		{Key: "profile_status", Value: profileStatus},
	}
}

func startJourneyCompleteProfileDocument() bson.D {
	return bson.D{
		{Key: "user_id", Value: "user-1"},
		{Key: "id_number", Value: "12345678"},
		{Key: "date_of_birth", Value: "1990-01-01"},
		{Key: "gender", Value: "female"},
		{Key: "address", Value: "123 Main St"},
		{Key: "skills", Value: bson.A{"plumbing"}},
		{Key: "skill_acquisition_mode", Value: "apprenticeship"},
		{Key: "next_of_kin_name", Value: "Jane Doe"},
		{Key: "next_of_kin_phone_number", Value: "+254700000000"},
		{Key: "next_of_kin_relationship", Value: "sister"},
		{Key: "locations", Value: bson.A{"Nairobi"}},
	}
}

func queueStartJourneyCompleteProfile(mt *mtest.T, profileStatus string) {
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, startJourneyUserDocument(profileStatus)))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, startJourneyCompleteProfileDocument()))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch, bson.D{
		{Key: "_id", Value: "wh-1"}, {Key: "user_id", Value: "user-1"},
		{Key: "day", Value: "monday"}, {Key: "opening_time", Value: "08:00"}, {Key: "closing_time", Value: "17:00"},
	}))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch, bson.D{
		{Key: "_id", Value: "ref-1"}, {Key: "user_id", Value: "user-1"},
		{Key: "name", Value: "Referee Name"}, {Key: "phone_number", Value: "+254711111111"},
	}))
}

func TestStartJourneyRejectsUnapprovedCompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete but not approved", func(mt *mtest.T) {
		useStartJourneyMockMongo(mt)
		queueStartJourneyCompleteProfile(mt, "pending_review")

		_, err := StartJourney(context.Background(), "user-1")
		if !errors.Is(err, ErrProfileNotApproved) {
			mt.Errorf("StartJourney() error = %v, want ErrProfileNotApproved", err)
		}
	})
}

func TestStartJourneyAllowsApprovedCompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete and approved", func(mt *mtest.T) {
		useStartJourneyMockMongo(mt)
		queueStartJourneyCompleteProfile(mt, "approved")
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		journey, err := StartJourney(context.Background(), "user-1")
		if err != nil {
			mt.Fatalf("StartJourney() error = %v", err)
		}
		if journey.CurrentStage != StageProfileReview {
			mt.Errorf("CurrentStage = %q, want %q", journey.CurrentStage, StageProfileReview)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./assessments/... -run TestStartJourney -v`
Expected: FAIL with `undefined: ErrProfileNotApproved`

- [ ] **Step 3: Implement the `StartJourney` gate**

In `backend/assessments/service.go`, the current var block (lines 44-51) starts:

```go
var (
	ErrProfileIncomplete   = errors.New("complete your profile before starting assessments")
```

Add one line directly after it:

```go
var (
	ErrProfileIncomplete   = errors.New("complete your profile before starting assessments")
	ErrProfileNotApproved  = errors.New("your profile must be approved before starting assessments")
```

Then in `StartJourney` (lines 109-137), change:

```go
	if !profiles.GetProfileCompletion(profile).Complete {
		return nil, ErrProfileIncomplete
	}
```

to:

```go
	if !profiles.GetProfileCompletion(profile).Complete {
		return nil, ErrProfileIncomplete
	}
	if users.EffectiveProfileStatus(profile.User) != users.ProfileStatusApproved {
		return nil, ErrProfileNotApproved
	}
```

`profile.User` works because `profiles.FullProfile` embeds `*users.User` (confirmed in
`backend/profiles/service.go:99`), so no second database lookup is needed — this reuses the
`profile` value `StartJourney` already loaded two lines above.

Add `"backend/users"` to `assessments/service.go`'s import block if it is not already
there — check first with `grep -n '"backend/users"' assessments/service.go`; if absent, add it
alongside the existing `"backend/profiles"` import line.

In `backend/assessments/handlers.go`, `HandleStartJourney` (lines 63-83) currently has:

```go
	journey, err := StartJourney(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, ErrProfileIncomplete) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
```

Add one more branch right after it:

```go
	journey, err := StartJourney(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, ErrProfileIncomplete) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, ErrProfileNotApproved) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./assessments/... -v`
Expected: All PASS, including the two new tests and every pre-existing `assessments` test.

- [ ] **Step 5: Write the failing test for job application gating**

Run: `cd backend && grep -rn "func TestHandleCreateJobApplication" jobrequests/*_test.go`

If a matching test file exists, add the new test case to it following its existing helper
conventions (read that file first to match its exact fixture style — do not invent a second,
inconsistent style in the same package). If none exists, create
`backend/jobrequests/job_application_profile_status_test.go`:

```go
package jobrequests

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/configs"
	"backend/contextutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func useJobApplicationMockMongo(mt *mtest.T) {
	previous := configs.MI
	configs.MI = configs.MongoInstance{Client: mt.Client, DB: mt.Client.Database("test")}
	mt.T.Cleanup(func() { configs.MI = previous })
}

func jobApplicationUserDocument(profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "user_type", Value: "artisan"},
		{Key: "profile_status", Value: profileStatus},
	}
}

func jobApplicationCompleteProfileDocument() bson.D {
	return bson.D{
		{Key: "user_id", Value: "user-1"},
		{Key: "id_number", Value: "12345678"},
		{Key: "date_of_birth", Value: "1990-01-01"},
		{Key: "gender", Value: "female"},
		{Key: "address", Value: "123 Main St"},
		{Key: "skills", Value: bson.A{"plumbing"}},
		{Key: "skill_acquisition_mode", Value: "apprenticeship"},
		{Key: "next_of_kin_name", Value: "Jane Doe"},
		{Key: "next_of_kin_phone_number", Value: "+254700000000"},
		{Key: "next_of_kin_relationship", Value: "sister"},
		{Key: "locations", Value: bson.A{"Nairobi"}},
	}
}

func TestHandleCreateJobApplicationRejectsUnapprovedCompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("complete but not approved", func(mt *mtest.T) {
		useJobApplicationMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, jobApplicationUserDocument("pending_review")))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch, jobApplicationCompleteProfileDocument()))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "wh-1"}, {Key: "user_id", Value: "user-1"},
			{Key: "day", Value: "monday"}, {Key: "opening_time", Value: "08:00"}, {Key: "closing_time", Value: "17:00"},
		}))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "ref-1"}, {Key: "user_id", Value: "user-1"},
			{Key: "name", Value: "Referee Name"}, {Key: "phone_number", Value: "+254711111111"},
		}))

		req := httptest.NewRequest(http.MethodPost, "/job-applications", strings.NewReader(`{"vacancy_id":"vac-1","cover_letter":"hello"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		ctx := echo.New().NewContext(req, rec)
		contextutil.SetUser(ctx, "user-1")

		if err := HandleCreateJobApplication(ctx); err != nil {
			mt.Fatalf("HandleCreateJobApplication() error = %v", err)
		}
		if rec.Code != http.StatusForbidden {
			mt.Errorf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
		}
	})
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd backend && go test ./jobrequests/... -run TestHandleCreateJobApplicationRejectsUnapprovedCompleteProfile -v`
Expected: FAIL — the request currently reaches the vacancy-lookup step and returns 404 ("Job vacancy not found") instead of 403, since there is no `ProfileStatus` check yet.

- [ ] **Step 7: Implement the job application gate**

In `backend/jobrequests/routes.go`, `HandleCreateJobApplication` (lines 528-547) currently
has:

```go
	if !profiles.GetProfileCompletion(profile).Complete {
		return responses.SendError(
			ctx,
			http.StatusForbidden,
			"Complete your professional profile before applying for vacancies",
		)
	}
```

Add immediately after it:

```go
	if users.EffectiveProfileStatus(account) != users.ProfileStatusApproved {
		return responses.SendError(
			ctx,
			http.StatusForbidden,
			"Your profile must be approved before applying for vacancies",
		)
	}
```

`account` is the `*users.User` already loaded three lines above this block by the existing
`account, err := users.GetUserByID(...)` call — no new lookup needed. `backend/jobrequests/routes.go`
already imports `"backend/users"` (confirmed at the top of that file), so no import change
is needed here.

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd backend && go test ./jobrequests/... -v`
Expected: All PASS, including the new test and every pre-existing `jobrequests` test.

- [ ] **Step 9: Run `gofmt`, `go vet`, and the full affected-package suite**

Run: `cd backend && gofmt -w assessments/ jobrequests/ && go vet ./assessments/... ./jobrequests/... && go test ./assessments/... ./jobrequests/... -v`
Expected: gofmt silent, vet clean, all tests PASS.

- [ ] **Step 10: Commit**

```bash
cd backend
git add assessments/service.go assessments/handlers.go assessments/start_journey_test.go \
        jobrequests/routes.go jobrequests/job_application_profile_status_test.go
git commit -m "feat(gating): require approved profile_status to start assessments or apply for jobs"
```

(Adjust the `git add` paths if Task 5's Step 5 found and extended an existing job-application
test file instead of creating a new one.)

---

### Task 6: Wire the edit-triggers-re-review hook into `profiles.HandleUpdateProfile`

**Files:**
- Modify: `backend/profiles/handlers.go:65-98` (`HandleUpdateProfile`)
- Modify: `backend/profiles/handlers_public_test.go` (add one new test)

**Interfaces:**
- Consumes: `users.RevertProfileStatusToPendingIfApproved(ctx, user) error` (Task 2).

- [ ] **Step 1: Write the failing test**

In `backend/profiles/handlers_public_test.go`, add (after `TestHandleUpdateProfileValidationFailureDoesNotInvalidateCache`, or anywhere in the file — Go test files do not require ordering):

```go
func artisanDocumentWithProfileStatus(slug, profileStatus string) bson.D {
	return bson.D{
		{Key: "_id", Value: "user-1"},
		{Key: "first_name", Value: "Alice"},
		{Key: "last_name", Value: "Kamau"},
		{Key: "user_type", Value: "artisan"},
		{Key: "slug", Value: slug},
		{Key: "profile_status", Value: profileStatus},
	}
}

func TestHandleUpdateProfileRevertsApprovedStatusToPendingReview(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("approved reverts on edit", func(mt *mtest.T) {
		useMockMongo(mt)
		server := useProfileTestRedis(mt.T)
		const slug = "alice-kamau-plumber-nairobi"
		seedPublicProfileCaches(mt.T, server, slug)
		mt.AddMockResponses(
			userCursorResponse(mt, artisanDocumentWithProfileStatus(slug, "approved")),
			userCursorResponse(mt, artisanDocumentWithProfileStatus(slug, "approved")),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),
		)

		recorder, err := runUpdateProfileRequest("user-1", `{}`)
		if err != nil {
			mt.Fatalf("HandleUpdateProfile() error = %v", err)
		}
		if recorder.Code != http.StatusOK {
			mt.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
		}
		event := mt.GetStartedEvent()
		if event == nil || event.CommandName != "update" {
			mt.Fatalf("started event = %#v, want the profile_status revert update", event)
		}
	})
}
```

This adds a third mock response (the `update` command) beyond the two `userCursorResponse`
calls the existing `TestHandleUpdateProfileInvalidatesPublicProfileCaches` test already uses
for an empty-body request, because this test's fixture has `ProfileStatus: "approved"`, so
`RevertProfileStatusToPendingIfApproved` will actually issue an `UpdateUser` call — unlike
the pre-existing tests, whose `artisanDocument` fixture has no `profile_status` field at all
and therefore triggers no extra call (see Task 2's design note on why that is deliberate).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./profiles/... -run TestHandleUpdateProfileRevertsApprovedStatusToPendingReview -v`
Expected: FAIL with "no responses remaining" (the handler does not yet issue the third Mongo call).

- [ ] **Step 3: Implement**

In `backend/profiles/handlers.go`, `HandleUpdateProfile` (lines 65-98) currently ends:

```go
	} else if user.Slug != "" {
		invalidateArtisanCache(ctx, user.Slug)
	}

	return responses.SendOK(ctx, nil, "Profile updated successfully")
}
```

Change to:

```go
	} else if user.Slug != "" {
		invalidateArtisanCache(ctx, user.Slug)
	}

	if user != nil {
		if err := users.RevertProfileStatusToPendingIfApproved(ctx.Request().Context(), user); err != nil {
			slog.ErrorContext(
				ctx.Request().Context(),
				"failed to revert profile_status after edit",
				"user_id",
				userID,
				"error",
				err,
			)
		}
	}

	return responses.SendOK(ctx, nil, "Profile updated successfully")
}
```

`user` may be `nil` here because the preceding block only assigns it inside the `if err ==
nil` branch of the earlier `users.GetUserByID` call — check this by reading lines 82-96
of the current file before editing, since the exact variable scoping determines whether the
`user != nil` guard above is required (it is, given the existing `if err != nil { ... } else
if user.Slug != "" { ... }` shape logs-and-continues on lookup failure rather than returning,
leaving `user` as its zero value `nil` in that branch).

Logged, not surfaced to the caller, matching the existing cache-invalidation error handling
three lines above it — the profile update itself already succeeded by this point.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./profiles/... -v`
Expected: All PASS, including the new test and every pre-existing `profiles` test (the two
tests that use the plain `artisanDocument` fixture must still pass unchanged, proving the
no-op path is undisturbed).

- [ ] **Step 5: Run `gofmt` and `go vet`**

Run: `cd backend && gofmt -w profiles/ && go vet ./profiles/...`
Expected: gofmt silent, vet clean.

- [ ] **Step 6: Commit**

```bash
cd backend
git add profiles/handlers.go profiles/handlers_public_test.go
git commit -m "feat(profiles): revert profile_status to pending_review when an approved profile is edited"
```

---

### Task 7: Artisan-facing `POST /profile/submit-review` route

**Files:**
- Modify: `backend/profiles/routes.go`
- Modify: `backend/profiles/handlers.go`
- Create: `backend/profiles/submit_review_test.go`

**Interfaces:**
- Consumes: `profileapproval.SubmitForReview`, `profileapproval.ErrProfileIncomplete`, `profileapproval.ErrAlreadyPending` (Task 4).
- Produces: `profiles.HandleSubmitProfileForReview(c echo.Context) error`, route `POST /profile/submit-review` on the authenticated group.

- [ ] **Step 1: Write the failing tests**

Create `backend/profiles/submit_review_test.go`:

```go
package profiles

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/contextutil"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func runSubmitReviewRequest(userID string) (*httptest.ResponseRecorder, error) {
	req := httptest.NewRequest(http.MethodPost, "/profile/submit-review", nil)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	if userID != "" {
		contextutil.SetUser(ctx, userID)
	}
	return rec, HandleSubmitProfileForReview(ctx)
}

func TestHandleSubmitProfileForReviewRequiresAuth(t *testing.T) {
	recorder, err := runSubmitReviewRequest("")
	if err != nil {
		t.Fatalf("HandleSubmitProfileForReview() error = %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}

func TestHandleSubmitProfileForReviewIncompleteProfile(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("incomplete", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"},
		}))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.profiles", mtest.FirstBatch)) // no profile row
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.working_hours", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.past_employers", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.language_proficiencies", mtest.FirstBatch))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.competency_references", mtest.FirstBatch))

		recorder, err := runSubmitReviewRequest("user-1")
		if err != nil {
			mt.Fatalf("HandleSubmitProfileForReview() error = %v", err)
		}
		if recorder.Code != http.StatusBadRequest {
			mt.Errorf("status = %d, want 400; body: %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestHandleSubmitProfileForReviewAlreadyPending(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("already pending", func(mt *mtest.T) {
		useMockMongo(mt)
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{
			{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"},
			{Key: "profile_status", Value: "pending_review"},
		}))

		recorder, err := runSubmitReviewRequest("user-1")
		if err != nil {
			mt.Fatalf("HandleSubmitProfileForReview() error = %v", err)
		}
		if recorder.Code != http.StatusConflict {
			mt.Errorf("status = %d, want 409; body: %s", recorder.Code, recorder.Body.String())
		}
	})
}
```

Note this file lives in package `profiles` and reuses that package's existing `useMockMongo`
test helper (already defined in `backend/profiles/slug_test.go` per the same convention
other `profiles` test files use — confirm with `grep -n "func useMockMongo" profiles/*.go`
before writing, since the exact defining file may differ from what is guessed here).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./profiles/... -run TestHandleSubmitProfileForReview -v`
Expected: FAIL with `undefined: HandleSubmitProfileForReview`

- [ ] **Step 3: Implement**

In `backend/profiles/handlers.go`, add near the end of the file (after `HandleUpdateProfile`):

```go
func HandleSubmitProfileForReview(ctx echo.Context) error {
	userID := contextutil.GetUserID(ctx)
	if userID == "" {
		return responses.SendError(ctx, 401, "Unauthorized")
	}

	if err := profileapproval.SubmitForReview(ctx.Request().Context(), userID); err != nil {
		if errors.Is(err, profileapproval.ErrProfileIncomplete) {
			return responses.SendError(ctx, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, profileapproval.ErrAlreadyPending) {
			return responses.SendError(ctx, http.StatusConflict, err.Error())
		}
		return responses.SendError(ctx, http.StatusInternalServerError, "failed to submit profile for review")
	}

	return responses.SendOK(ctx, nil, "Profile submitted for review")
}
```

Add `"net/http"` and `"backend/profileapproval"` to `backend/profiles/handlers.go`'s import
block (the file's current imports are `"errors"`, `"log/slog"`, `"regexp"`, `"backend/contextutil"`,
`"backend/middleware"`, `"backend/responses"`, `"backend/skills"`, `"backend/users"`, plus the
`echo`/`bson`/`mongo` third-party imports — `"errors"` is already present, `"net/http"` and
`"backend/profileapproval"` are not).

In `backend/profiles/routes.go`, change:

```go
func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/profile", HandleGetProfile)
	g.POST("/profile", HandleUpdateProfile)
	g.POST("/profile/public", HandleTogglePublicProfile)
}
```

to:

```go
func RegisterAuthRoutes(g *echo.Group) {
	g.GET("/profile", HandleGetProfile)
	g.POST("/profile", HandleUpdateProfile)
	g.POST("/profile/public", HandleTogglePublicProfile)
	g.POST("/profile/submit-review", HandleSubmitProfileForReview)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./profiles/... -v`
Expected: All PASS.

- [ ] **Step 5: Confirm no import cycle**

Run: `cd backend && go build ./...`
Expected: builds cleanly. `profiles` now imports `profileapproval`, and `profileapproval`
imports `profiles` — check this specific pair carefully:

Run: `cd backend && go list -deps ./profileapproval | grep backend/profiles`
Expected: prints `backend/profiles`, confirming `profileapproval` depends on `profiles`.

Run: `cd backend && go list -deps ./profiles | grep backend/profileapproval`
Expected: **prints nothing** — if this prints `backend/profileapproval`, stop and re-read
Task 2's design note: something introduced a cycle, most likely by accidentally calling
`profileapproval` from inside `profiles/handlers.go`'s `HandleUpdateProfile` instead of the
`users.RevertProfileStatusToPendingIfApproved` helper Task 6 wired in. `profiles` importing
`profileapproval` is fine in this one new file (`HandleSubmitProfileForReview` calling
`profileapproval.SubmitForReview`) — the cycle risk is specifically about `profiles` calling
back into `profileapproval` from the *edit* path, not the *submit* path. Since `go build`
already succeeded above, Go's own compiler has already ruled out an actual cycle; this
`go list` check is a documentation/confirmation step, not a new safety net.

- [ ] **Step 6: Run `gofmt` and `go vet`**

Run: `cd backend && gofmt -w profiles/ && go vet ./profiles/...`
Expected: gofmt silent, vet clean.

- [ ] **Step 7: Commit**

```bash
cd backend
git add profiles/routes.go profiles/handlers.go profiles/submit_review_test.go
git commit -m "feat(profiles): add POST /profile/submit-review"
```

---

### Task 8: Notification events for approve/reject decisions

**Files:**
- Modify: `backend/notifications/model.go`
- Modify: `backend/notifications/service.go`
- Create: `backend/notifications/profile_review_test.go`

**Interfaces:**
- Produces: `notifications.TypeProfileReviewApproved`, `notifications.TypeProfileReviewRejected` (both `string` constants); `notifications.ProfileReviewDecisionEvent{UserID, Approved bool, Reason string}`; `(*Service) NotifyProfileReviewDecision(ctx, event ProfileReviewDecisionEvent) error`.

The two already-declared-but-unused constants `TypeAssessmentApproved`/`TypeAssessmentRejected`
in `notifications/model.go:26-27` belong to the *existing* assessment journey pipeline (no
method uses them yet) and must not be reused here — this is a distinct feature per the spec's
§1, so it gets its own distinctly-named constants following the same `Type<Domain><Event>`
convention documented in that file's comment at line 24-25.

- [ ] **Step 1: Write the failing test**

Create `backend/notifications/profile_review_test.go`:

```go
package notifications

import (
	"context"
	"testing"
)

func TestNotifyProfileReviewDecisionApproved(t *testing.T) {
	stub := &notificationCreatorStub{}
	service := NewService(stub)

	err := service.NotifyProfileReviewDecision(context.Background(), ProfileReviewDecisionEvent{
		UserID:   "user-1",
		Approved: true,
	})
	if err != nil {
		t.Fatalf("NotifyProfileReviewDecision() error = %v", err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("created %d notifications, want 1", len(stub.created))
	}
	got := stub.created[0]
	if got.UserID != "user-1" {
		t.Errorf("UserID = %q, want user-1", got.UserID)
	}
	if got.Type != TypeProfileReviewApproved {
		t.Errorf("Type = %q, want %q", got.Type, TypeProfileReviewApproved)
	}
}

func TestNotifyProfileReviewDecisionRejectedIncludesReason(t *testing.T) {
	stub := &notificationCreatorStub{}
	service := NewService(stub)

	err := service.NotifyProfileReviewDecision(context.Background(), ProfileReviewDecisionEvent{
		UserID:   "user-1",
		Approved: false,
		Reason:   "missing next of kin details",
	})
	if err != nil {
		t.Fatalf("NotifyProfileReviewDecision() error = %v", err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("created %d notifications, want 1", len(stub.created))
	}
	got := stub.created[0]
	if got.Type != TypeProfileReviewRejected {
		t.Errorf("Type = %q, want %q", got.Type, TypeProfileReviewRejected)
	}
	if got.Message == "" {
		t.Error("Message is empty, want the rejection reason included")
	}
}
```

This reuses `notificationCreatorStub`, already defined in `backend/notifications/service_test.go:8-10`
(same package, so it is visible here without any import).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./notifications/... -run TestNotifyProfileReviewDecision -v`
Expected: FAIL with `undefined: ProfileReviewDecisionEvent`

- [ ] **Step 3: Implement**

In `backend/notifications/model.go`, add two constants after the existing
`TypeAppointmentReminder` line (inside the same const block, lines 16-30):

```go
	TypeProfileReviewApproved = "profile_review_approved"
	TypeProfileReviewRejected = "profile_review_rejected"
```

In `backend/notifications/service.go`, add a new event struct after `ApplicationDecisionEvent`
(lines 23-29):

```go
type ProfileReviewDecisionEvent struct {
	UserID   string
	Approved bool
	Reason   string
}
```

And a new method after `NotifyApplicationDecision` (lines 102-119):

```go
func (s *Service) NotifyProfileReviewDecision(ctx context.Context, event ProfileReviewDecisionEvent) error {
	if event.Approved {
		return s.store.Create(ctx, Notification{
			UserID:    event.UserID,
			Type:      TypeProfileReviewApproved,
			Title:     "Profile approved",
			Message:   "Your profile has been approved. You can now start assessments or apply for jobs.",
			ActionURL: "/profile",
		})
	}
	return s.store.Create(ctx, Notification{
		UserID:    event.UserID,
		Type:      TypeProfileReviewRejected,
		Title:     "Profile not approved",
		Message:   "Your profile was not approved: " + event.Reason,
		ActionURL: "/profile",
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./notifications/... -v`
Expected: All PASS, including the two new tests and every pre-existing `notifications` test.

- [ ] **Step 5: Run `gofmt` and `go vet`**

Run: `cd backend && gofmt -w notifications/ && go vet ./notifications/...`
Expected: gofmt silent, vet clean.

- [ ] **Step 6: Commit**

```bash
cd backend
git add notifications/model.go notifications/service.go notifications/profile_review_test.go
git commit -m "feat(notifications): add profile review approved/rejected events"
```

---

### Task 9: Admin routes — list, detail, approve, reject

**Files:**
- Create: `backend/adminapi/profile_reviews.go`
- Create: `backend/adminapi/profile_reviews_test.go`
- Modify: `backend/adminapi/routes.go`
- Modify: `backend/adminapi/routes_test.go`

**Interfaces:**
- Consumes: `Page[T]`, `parsePageLimit` (existing, `adminapi/pagination.go`); `profileapproval.Approve`, `profileapproval.Reject`, `profileapproval.ErrNotPending`, `profileapproval.ErrReasonRequired` (Task 4); `users.ListAllUsers`, `users.GetUserByID` (existing); `profiles.GetFullProfile` (existing); `notifications.NewDefaultService().NotifyProfileReviewDecision` (Task 8); `contextutil.GetUserID` (existing).
- Produces: `adminapi.HandleListProfileReviews`, `adminapi.HandleGetProfileReview`, `adminapi.HandleApproveProfileReview`, `adminapi.HandleRejectProfileReview` (all `echo.HandlerFunc`); routes `GET /admin/profile-reviews`, `GET /admin/profile-reviews/:user_id`, `POST /admin/profile-reviews/:user_id/approve`, `POST /admin/profile-reviews/:user_id/reject`.

- [ ] **Step 1: Write the failing tests**

Create `backend/adminapi/profile_reviews_test.go`:

```go
package adminapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"backend/users"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestHandleListProfileReviewsDefaultsToPendingReview(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "user_type", Value: "artisan"}, {Key: "profile_status", Value: "pending_review"}},
	))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch, bson.D{{Key: "n", Value: 1}}))

	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews", "")
	if err := HandleListProfileReviews(ctx); err != nil {
		t.Fatalf("HandleListProfileReviews() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Data []users.User `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Data.Data) != 1 || body.Data.Data[0].ProfileStatus != "pending_review" {
		t.Errorf("response data = %#v", body.Data.Data)
	}
}

func TestHandleGetProfileReviewNotFound(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	ctx, rec := adminHandlerContext(http.MethodGet, "/profile-reviews/missing", "")
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("missing")

	if err := HandleGetProfileReview(ctx); err != nil {
		t.Fatalf("HandleGetProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleApproveProfileReviewNotPending(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "profile_status", Value: "approved"}},
	))

	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/user-1/approve", "")
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("user-1")

	if err := HandleApproveProfileReview(ctx); err != nil {
		t.Fatalf("HandleApproveProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRejectProfileReviewRequiresReason(t *testing.T) {
	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/user-1/reject", `{"reason":""}`)
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("user-1")

	if err := HandleRejectProfileReview(ctx); err != nil {
		t.Fatalf("HandleRejectProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRejectProfileReviewHappyPathDoesNotFailOnNotificationError(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	// profileapproval.Reject's own GetUserByID + UpdateUser:
	mt.AddMockResponses(mtest.CreateCursorResponse(
		0, "test.users", mtest.FirstBatch,
		bson.D{{Key: "_id", Value: "user-1"}, {Key: "profile_status", Value: "pending_review"}},
	))
	mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))

	ctx, rec := adminHandlerContext(http.MethodPost, "/profile-reviews/user-1/reject", `{"reason":"missing documents"}`)
	ctx.SetParamNames("user_id")
	ctx.SetParamValues("user-1")

	if err := HandleRejectProfileReview(ctx); err != nil {
		t.Fatalf("HandleRejectProfileReview() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Profile rejected") {
		t.Errorf("body = %s, want a success message", rec.Body.String())
	}
}
```

The last test's name (`...DoesNotFailOnNotificationError`) states the intent precisely:
`notifications.NewDefaultService()` panics if `ConfigureDefaultStore` was never called (see
Task 4's header note), and no test process in this codebase calls it. So
`HandleApproveProfileReview`/`HandleRejectProfileReview` must never call
`notifications.NewDefaultService()` unguarded — the notification helper below wraps that call
in a deferred `recover()`, logging instead of propagating, exactly as the codebase already
treats every other notification-delivery failure as non-fatal (see `jobrequests`'s pattern
described in Task 4's header, which logs and continues on an *error* return — this additionally
guards against a *panic*, which is unique to `NewDefaultService()`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./adminapi/... -run "TestHandleListProfileReviews|TestHandleGetProfileReview|TestHandleApproveProfileReview|TestHandleRejectProfileReview" -v`
Expected: FAIL with `undefined: HandleListProfileReviews` (and the other three handlers)

- [ ] **Step 3: Implement**

Create `backend/adminapi/profile_reviews.go`:

```go
package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"backend/contextutil"
	"backend/notifications"
	"backend/profileapproval"
	"backend/profiles"
	"backend/responses"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func HandleListProfileReviews(c echo.Context) error {
	page, limit := parsePageLimit(c)
	status := c.QueryParam("status")
	if status == "" {
		status = users.ProfileStatusPendingReview
	}

	list, total, err := users.ListAllUsers(bson.M{"profile_status": status}, page, limit)
	if err != nil {
		return responses.SendError(c, http.StatusInternalServerError, "failed to list profile reviews")
	}
	if list == nil {
		list = []users.User{}
	}
	return responses.SendOK(c, Page[users.User]{
		Data:  list,
		Total: total,
		Page:  page,
		Limit: limit,
	}, "Profile reviews retrieved")
}

func HandleGetProfileReview(c echo.Context) error {
	userID := c.Param("user_id")
	profile, err := profiles.GetFullProfile(c.Request().Context(), userID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return responses.SendError(c, http.StatusNotFound, "user not found")
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to load profile")
	}
	return responses.SendOK(c, profile, "Profile retrieved")
}

// notifyProfileReviewDecision fires the artisan-facing notification for an
// approve/reject decision. notifications.NewDefaultService panics if
// ConfigureDefaultStore was never called (a startup invariant enforced in
// main.go, not something every test process satisfies), so this recovers
// from that panic the same way every other notification-delivery failure in
// this codebase is treated: logged, never surfacing to the caller, because
// the review decision itself already succeeded in the database by the time
// this runs.
func notifyProfileReviewDecision(ctx context.Context, userID string, approved bool, reason string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("failed to deliver profile review notification", "user_id", userID, "recovered", r)
		}
	}()
	err := notifications.NewDefaultService().NotifyProfileReviewDecision(ctx, notifications.ProfileReviewDecisionEvent{
		UserID:   userID,
		Approved: approved,
		Reason:   reason,
	})
	if err != nil {
		slog.Error("failed to deliver profile review notification", "user_id", userID, "error", err)
	}
}

type rejectProfileReviewRequest struct {
	Reason string `json:"reason"`
}

func HandleApproveProfileReview(c echo.Context) error {
	userID := c.Param("user_id")
	reviewedBy := contextutil.GetUserID(c)

	if err := profileapproval.Approve(c.Request().Context(), userID, reviewedBy); err != nil {
		if errors.Is(err, profileapproval.ErrNotPending) {
			return responses.SendError(c, http.StatusConflict, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to approve profile")
	}

	notifyProfileReviewDecision(c.Request().Context(), userID, true, "")
	return responses.SendOK(c, nil, "Profile approved")
}

func HandleRejectProfileReview(c echo.Context) error {
	userID := c.Param("user_id")
	reviewedBy := contextutil.GetUserID(c)

	var req rejectProfileReviewRequest
	if err := c.Bind(&req); err != nil {
		return responses.SendError(c, http.StatusBadRequest, "invalid request body")
	}

	if err := profileapproval.Reject(c.Request().Context(), userID, reviewedBy, req.Reason); err != nil {
		if errors.Is(err, profileapproval.ErrReasonRequired) {
			return responses.SendError(c, http.StatusBadRequest, err.Error())
		}
		if errors.Is(err, profileapproval.ErrNotPending) {
			return responses.SendError(c, http.StatusConflict, err.Error())
		}
		return responses.SendError(c, http.StatusInternalServerError, "failed to reject profile")
	}

	notifyProfileReviewDecision(c.Request().Context(), userID, false, req.Reason)
	return responses.SendOK(c, nil, "Profile rejected")
}
```

Now update `backend/adminapi/routes.go` from:

```go
func RegisterAdminRoutes(g *echo.Group) {
	g.GET("/users", HandleListUsers)
	g.GET("/users/:id", HandleGetUser)
	g.PATCH("/users/:id/status", HandleUpdateUserStatus)
	g.GET("/stats", HandleGetStats)
}
```

to:

```go
func RegisterAdminRoutes(g *echo.Group) {
	g.GET("/users", HandleListUsers)
	g.GET("/users/:id", HandleGetUser)
	g.PATCH("/users/:id/status", HandleUpdateUserStatus)
	g.GET("/stats", HandleGetStats)
	g.GET("/profile-reviews", HandleListProfileReviews)
	g.GET("/profile-reviews/:user_id", HandleGetProfileReview)
	g.POST("/profile-reviews/:user_id/approve", HandleApproveProfileReview)
	g.POST("/profile-reviews/:user_id/reject", HandleRejectProfileReview)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./adminapi/... -v`
Expected: All PASS, including the five new tests and every pre-existing `adminapi` test.
`TestHandleRejectProfileReviewHappyPathDoesNotFailOnNotificationError` in particular must
return 200, not panic — this is the test that proves the `recover()` in
`notifyProfileReviewDecision` works.

- [ ] **Step 5: Update `adminapi/routes_test.go`**

The existing `TestRegisterAdminRoutesRegistersUserEndpoints` test in
`backend/adminapi/routes_test.go` checks a fixed map of expected routes. Add the four new
routes to its `want` map:

```go
	want := map[string]bool{
		"GET /admin/users":               false,
		"GET /admin/users/:id":           false,
		"PATCH /admin/users/:id/status":  false,
		"GET /admin/stats":               false,
		"GET /admin/profile-reviews":              false,
		"GET /admin/profile-reviews/:user_id":     false,
		"POST /admin/profile-reviews/:user_id/approve": false,
		"POST /admin/profile-reviews/:user_id/reject":  false,
	}
```

(Read the file first with `cat backend/adminapi/routes_test.go` to get the exact current
map contents and gofmt-correct alignment before editing — the snippet above shows the
required additions, not necessarily the exact existing whitespace.)

- [ ] **Step 6: Run the route-registration test and the full `adminapi` suite**

Run: `cd backend && go test ./adminapi/... -v`
Expected: All PASS.

- [ ] **Step 7: Confirm no import cycle**

Run: `cd backend && go build ./...`
Expected: builds cleanly. `adminapi` now imports `profileapproval`, `profiles`, `notifications`,
and `users` — all leaf-ward imports, no cycle risk (none of those four import `adminapi`).

- [ ] **Step 8: Run `gofmt` and `go vet`**

Run: `cd backend && gofmt -w adminapi/ && go vet ./adminapi/...`
Expected: gofmt silent, vet clean.

- [ ] **Step 9: Commit**

```bash
cd backend
git add adminapi/profile_reviews.go adminapi/profile_reviews_test.go adminapi/routes.go adminapi/routes_test.go
git commit -m "feat(adminapi): add profile review list/detail/approve/reject endpoints"
```

---

### Task 10: End-to-end admin-guard test for the new routes

**Files:**
- Modify: `backend/adminapi/admin_group_test.go`

**Interfaces:**
- Consumes: `buildAdminGroup()`, `queueUserLookup()`, `useAdminMockMongo()` (all existing, same file).

This mirrors the precedent set in the prior admin-dashboard work: a bare route-registration
test alone does not prove the `IsAdmin` guard actually rejects a non-admin caller through the
real middleware chain — `backend/adminapi/admin_group_test.go` already exists specifically to
close that gap for the `/admin/users` and `/admin/stats` routes, and its own file comment
states this is deliberate, reusable infrastructure.

- [ ] **Step 1: Write the failing test**

Add to `backend/adminapi/admin_group_test.go` (after the existing
`TestAdminUsersCreateEndpointRejectsNonAdminCallerEndToEnd` function):

```go
func TestAdminGroupRejectsNonAdminJWTOnProfileReviewApprove(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	queueUserLookup(mt, "artisan-1", "artisan") // JWTAuth's lookup
	queueUserLookup(mt, "artisan-1", "artisan") // IsAdmin's lookup

	token, _, err := jwtutil.GenerateToken("artisan-1")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	app := buildAdminGroup()
	req := httptest.NewRequest(http.MethodPost, "/admin/profile-reviews/user-2/approve", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a non-admin JWT; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminGroupAllowsAdminJWTOnProfileReviewList(t *testing.T) {
	mt := useAdminMockMongo(t, "users")
	queueUserLookup(mt, "admin-1", "admin") // JWTAuth's lookup
	queueUserLookup(mt, "admin-1", "admin") // IsAdmin's lookup
	// HandleListProfileReviews' own Find + CountDocuments:
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))
	mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.users", mtest.FirstBatch))

	token, _, err := jwtutil.GenerateToken("admin-1")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	app := buildAdminGroup()
	req := httptest.NewRequest(http.MethodGet, "/admin/profile-reviews", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an admin JWT; body: %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `cd backend && go test ./adminapi/... -run TestAdminGroup -v`
Expected: PASS for all `TestAdminGroup*` tests, including the two new ones. (These are
extension tests on already-implemented routes from Task 9, so there is no red step here —
Task 9's implementation already makes these pass; this task exists to add the missing
end-to-end coverage, not to drive new implementation.)

- [ ] **Step 3: Run `gofmt`**

Run: `cd backend && gofmt -l adminapi/`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
cd backend
git add adminapi/admin_group_test.go
git commit -m "test(adminapi): add end-to-end admin-guard coverage for profile review routes"
```

---

### Task 11: Full backend regression check

**Files:** none (verification-only task).

- [ ] **Step 1: Run the full backend test suite**

Run: `cd backend && go test ./... 2>&1 | grep -v "^ok"`
Expected: no output, or only the known-flaky pre-existing failures already documented from
prior sessions (`backend/logging`'s `TestStartedShipperDrainsTheQueue`, a goroutine-count
timing test unrelated to this feature — confirm it is still the same test if anything prints,
and re-run that one package alone with `-count=1` to confirm it passes in isolation before
concluding it is pre-existing flakiness rather than a real regression).

- [ ] **Step 2: Run `go vet` and `gofmt` across the whole module**

Run: `cd backend && go vet ./... && gofmt -l .`
Expected: vet clean, gofmt prints nothing (or only files under `.worktrees/`, which are a
separate git worktree and not part of this change).

- [ ] **Step 3: Confirm no import cycles anywhere in the new dependency graph**

Run: `cd backend && go build ./...`
Expected: builds cleanly — this is the authoritative check; if any cycle existed among
`users`, `profiles`, `profileapproval`, `notifications`, `assessments`, `jobrequests`, or
`adminapi`, this command fails with an explicit cycle error naming the exact package loop.

---

### Task 12: Frontend types and API client

**Files:**
- Create: `admin/src/types/profileReview.ts`
- Create: `admin/src/api/profileReviews.ts`

**Interfaces:**
- Consumes: `admin/src/lib/api.ts`'s default axios instance (existing); `admin/src/types/api.ts`'s `ApiEnvelope<T>`, `PageEnvelope<T>` (existing); `admin/src/types/user.ts`'s `AdminUser` (existing — extend it, do not duplicate it).
- Produces: `ProfileReviewUser` (extends `AdminUser`'s shape — see Step 1), `ProfileReviewDetail`; `listProfileReviews`, `getProfileReview`, `approveProfileReview`, `rejectProfileReview`.

- [ ] **Step 1: Add the new fields to the existing `AdminUser` type**

Read `admin/src/types/user.ts` first — it currently mirrors `backend/users/model.go`'s `User`
struct field-for-field (per that file's own header comment). Task 1 added five new fields to
the Go struct; mirror them here in the same file, in the same relative position (immediately
before `created_at`/`updated_at`, matching the Go struct's field order exactly, since this
file's whole purpose is a 1:1 mirror):

```typescript
  profile_status: "not_submitted" | "pending_review" | "approved" | "rejected";
  profile_submitted_at?: string;
  profile_reviewed_at?: string;
  profile_reviewed_by?: string;
  profile_reject_reason?: string;
```

Add these as new lines in the existing `AdminUser` interface, immediately before the
`created_at: string;` line. Also add, near the bottom of the file where `UserStatus`/`UserType`
are already exported as derived types:

```typescript
export type ProfileStatus = AdminUser["profile_status"];
```

- [ ] **Step 2: Create the profile review detail type**

Create `admin/src/types/profileReview.ts`:

```typescript
import type { AdminUser } from "./user";

// Mirrors backend/profiles/service.go FullProfile — the subset of fields
// the review detail screen needs to show an admin what to check. AdminUser
// is embedded because Go's profiles.FullProfile embeds *users.User, and the
// JSON encoding of an embedded struct pointer flattens its fields into the
// same top-level object, not a nested one.
export interface ProfileReviewDetail extends AdminUser {
  id_number: string;
  date_of_birth: string;
  gender: string;
  address: string;
  city: string;
  skills: string[];
  skill_acquisition_mode: string;
  years_of_experience: number;
  next_of_kin_name: string;
  next_of_kin_phone_number: string;
  next_of_kin_relationship: string;
  locations: string[];
  working_hours: { day: string; opening_time: string; closing_time: string }[];
  references: { name: string; phone_number: string }[];
}
```

- [ ] **Step 3: Create the API client**

Create `admin/src/api/profileReviews.ts`:

```typescript
import api from "../lib/api";
import type { ApiEnvelope, PageEnvelope } from "../types/api";
import type { AdminUser } from "../types/user";
import type { ProfileReviewDetail } from "../types/profileReview";

export interface ListProfileReviewsParams {
  status?: string;
  page?: number;
  limit?: number;
}

export async function listProfileReviews(
  params: ListProfileReviewsParams,
): Promise<PageEnvelope<AdminUser>> {
  const response = await api.get<ApiEnvelope<PageEnvelope<AdminUser>>>(
    "/admin/profile-reviews",
    { params },
  );
  return response.data.data;
}

export async function getProfileReview(userId: string): Promise<ProfileReviewDetail> {
  const response = await api.get<ApiEnvelope<ProfileReviewDetail>>(
    `/admin/profile-reviews/${userId}`,
  );
  return response.data.data;
}

export async function approveProfileReview(userId: string): Promise<void> {
  await api.post(`/admin/profile-reviews/${userId}/approve`);
}

export async function rejectProfileReview(userId: string, reason: string): Promise<void> {
  await api.post(`/admin/profile-reviews/${userId}/reject`, { reason });
}
```

- [ ] **Step 4: Type-check**

Run: `cd admin && npm run build`
Expected: `tsc -b` and `vite build` both succeed with no type errors. There is no runtime
behavior to exercise yet (no page imports these files), so this is the only verification
available for this task — matches this project's standing decision to skip automated frontend
tests (see this plan's Global Constraints).

- [ ] **Step 5: Lint**

Run: `cd admin && npm run lint`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
cd admin
git add src/types/user.ts src/types/profileReview.ts src/api/profileReviews.ts
git commit -m "feat: add profile review types and API client"
```

---

### Task 13: Frontend hooks

**Files:**
- Create: `admin/src/hooks/useProfileReviews.ts`

**Interfaces:**
- Consumes: `listProfileReviews`, `getProfileReview`, `approveProfileReview`, `rejectProfileReview` (Task 12).
- Produces: `useProfileReviews(params)`, `useProfileReview(userId)`, `useApproveProfileReview()`, `useRejectProfileReview()`.

- [ ] **Step 1: Implement**

Read `admin/src/hooks/useUsers.ts` first — this file follows its exact shape (query-key
convention, `useMutation` + `invalidateQueries` on success).

Create `admin/src/hooks/useProfileReviews.ts`:

```typescript
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  approveProfileReview,
  getProfileReview,
  listProfileReviews,
  rejectProfileReview,
  type ListProfileReviewsParams,
} from "../api/profileReviews";

export function useProfileReviews(params: ListProfileReviewsParams) {
  return useQuery({
    queryKey: ["profileReviews", params],
    queryFn: () => listProfileReviews(params),
  });
}

export function useProfileReview(userId: string | undefined) {
  return useQuery({
    queryKey: ["profileReviews", "detail", userId],
    queryFn: () => getProfileReview(userId as string),
    enabled: Boolean(userId),
  });
}

export function useApproveProfileReview() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => approveProfileReview(userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["profileReviews"] });
    },
  });
}

export function useRejectProfileReview() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, reason }: { userId: string; reason: string }) =>
      rejectProfileReview(userId, reason),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["profileReviews"] });
    },
  });
}
```

- [ ] **Step 2: Type-check**

Run: `cd admin && npm run build`
Expected: succeeds.

- [ ] **Step 3: Lint**

Run: `cd admin && npm run lint`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
cd admin
git add src/hooks/useProfileReviews.ts
git commit -m "feat: add profile review TanStack Query hooks"
```

---

### Task 14: Frontend pages and navigation

**Files:**
- Create: `admin/src/pages/ProfileReviewsPage.tsx`
- Create: `admin/src/pages/ProfileReviewDetailPage.tsx`
- Modify: `admin/src/components/layout/Sidebar.tsx`
- Modify: `admin/src/App.tsx`

**Interfaces:**
- Consumes: `useProfileReviews`, `useProfileReview`, `useApproveProfileReview`, `useRejectProfileReview` (Task 13); `DataTable`, `Column`, `Pagination`, `StatusBadge`, `FilterSelect` (existing, `admin/src/components/`); `LoadingState`, `EmptyState`, `ErrorState` (existing, `admin/src/components/states/`); `extractApiErrorMessage` (existing, `admin/src/lib/errors.ts`).

- [ ] **Step 1: Implement the list page**

Read `admin/src/pages/UsersPage.tsx` first — this new page follows its exact shape (search/filter
state, `DataTable` + `Pagination` composition).

Create `admin/src/pages/ProfileReviewsPage.tsx`:

```typescript
import { useState } from "react";
import { useNavigate } from "react-router-dom";

import { DataTable, type Column } from "../components/DataTable";
import { Pagination } from "../components/Pagination";
import { FilterSelect } from "../components/FilterSelect";
import { StatusBadge } from "../components/StatusBadge";
import { extractApiErrorMessage } from "../lib/errors";
import { useProfileReviews } from "../hooks/useProfileReviews";
import type { AdminUser } from "../types/user";

const STATUS_OPTIONS = [
  { label: "Pending review", value: "pending_review" },
  { label: "Approved", value: "approved" },
  { label: "Rejected", value: "rejected" },
  { label: "Not submitted", value: "not_submitted" },
];

const COLUMNS: Column<AdminUser>[] = [
  { key: "name", header: "Name", render: (u) => `${u.first_name} ${u.last_name}` },
  { key: "contact", header: "Contact", render: (u) => u.email || u.phone },
  { key: "profile_status", header: "Status", render: (u) => <StatusBadge status={u.profile_status} /> },
  {
    key: "submitted",
    header: "Submitted",
    render: (u) => (u.profile_submitted_at ? new Date(u.profile_submitted_at).toLocaleDateString() : "—"),
  },
];

export function ProfileReviewsPage() {
  const navigate = useNavigate();
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState("pending_review");

  const { data, isLoading, error, refetch } = useProfileReviews({
    page,
    limit: 20,
    status,
  });

  return (
    <div>
      <h1 className="mb-4 font-display text-xl font-semibold text-ink-900">Profile Reviews</h1>

      <div className="mb-4 flex flex-wrap gap-3">
        <FilterSelect value={status} onChange={setStatus} options={STATUS_OPTIONS} placeholder="All statuses" />
      </div>

      <DataTable
        columns={COLUMNS}
        rows={data?.data ?? []}
        rowKey={(u) => u.id}
        isLoading={isLoading}
        error={error ? extractApiErrorMessage(error) : null}
        onRetry={() => refetch()}
        emptyTitle="No profiles to review"
        emptyDescription="Try a different status filter."
        onRowClick={(u) => navigate(`/profile-reviews/${u.id}`)}
      />

      {data && (
        <Pagination page={data.page} limit={data.limit} total={data.total} onPageChange={setPage} />
      )}
    </div>
  );
}
```

`StatusBadge`'s existing `STATUS_STYLES` map (in `admin/src/components/StatusBadge.tsx`) only
has entries for `active`/`pending`/`suspended` — it already falls back to a neutral gray style
for any unrecognized status string (confirmed in that component's existing code:
`STATUS_STYLES[status] ?? "bg-surface-100 text-ink-700"`), so `pending_review`/`approved`/
`rejected`/`not_submitted` will render correctly without modification, just without a distinct
color per status. That is acceptable for this task; a follow-up could extend `STATUS_STYLES`
if the four-color distinction becomes important, but is not required here.

- [ ] **Step 2: Implement the detail page**

Create `admin/src/pages/ProfileReviewDetailPage.tsx`:

```typescript
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { LoadingState } from "../components/states/LoadingState";
import { ErrorState } from "../components/states/ErrorState";
import { StatusBadge } from "../components/StatusBadge";
import { extractApiErrorMessage } from "../lib/errors";
import { useApproveProfileReview, useProfileReview, useRejectProfileReview } from "../hooks/useProfileReviews";

export function ProfileReviewDetailPage() {
  const { userId } = useParams<{ userId: string }>();
  const navigate = useNavigate();
  const { data, isLoading, error, refetch } = useProfileReview(userId);
  const approve = useApproveProfileReview();
  const reject = useRejectProfileReview();
  const [rejectReason, setRejectReason] = useState("");
  const [showRejectForm, setShowRejectForm] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  if (isLoading) return <LoadingState label="Loading profile…" />;
  if (error) return <ErrorState message={extractApiErrorMessage(error)} onRetry={() => refetch()} />;
  if (!data) return null;

  async function handleApprove() {
    setActionError(null);
    try {
      await approve.mutateAsync(data.id);
      navigate("/profile-reviews");
    } catch (err) {
      setActionError(extractApiErrorMessage(err));
    }
  }

  async function handleReject() {
    if (!rejectReason.trim()) return;
    setActionError(null);
    try {
      await reject.mutateAsync({ userId: data.id, reason: rejectReason });
      navigate("/profile-reviews");
    } catch (err) {
      setActionError(extractApiErrorMessage(err));
    }
  }

  return (
    <div className="max-w-2xl">
      <h1 className="mb-1 font-display text-xl font-semibold text-ink-900">
        {data.first_name} {data.last_name}
      </h1>
      <div className="mb-4">
        <StatusBadge status={data.profile_status} />
      </div>

      {actionError && (
        <p className="mb-4 rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">{actionError}</p>
      )}

      <dl className="mb-6 grid grid-cols-2 gap-4 rounded-lg border border-ink-300/30 bg-white p-4 text-sm">
        <div>
          <dt className="text-ink-500">Skills</dt>
          <dd>{data.skills?.join(", ") || "—"}</dd>
        </div>
        <div>
          <dt className="text-ink-500">Years of experience</dt>
          <dd>{data.years_of_experience ?? "—"}</dd>
        </div>
        <div>
          <dt className="text-ink-500">Address</dt>
          <dd>{data.address || "—"}</dd>
        </div>
        <div>
          <dt className="text-ink-500">Locations served</dt>
          <dd>{data.locations?.join(", ") || "—"}</dd>
        </div>
        <div>
          <dt className="text-ink-500">Next of kin</dt>
          <dd>{data.next_of_kin_name || "—"}</dd>
        </div>
        <div>
          <dt className="text-ink-500">References</dt>
          <dd>{data.references?.length ?? 0} on file</dd>
        </div>
      </dl>

      {data.profile_status === "pending_review" && (
        <div className="flex flex-col gap-3">
          <div className="flex gap-2">
            <button
              type="button"
              onClick={handleApprove}
              disabled={approve.isPending}
              className="rounded-md bg-brand-500 px-4 py-2 text-sm font-medium text-white hover:bg-brand-600 disabled:opacity-50"
            >
              Approve
            </button>
            <button
              type="button"
              onClick={() => setShowRejectForm((v) => !v)}
              className="rounded-md border border-ink-300 px-4 py-2 text-sm font-medium text-ink-700 hover:bg-surface-100"
            >
              Reject
            </button>
          </div>

          {showRejectForm && (
            <div className="rounded-lg border border-ink-300/30 bg-white p-4">
              <label className="mb-1 block text-sm font-medium text-ink-700" htmlFor="reject-reason">
                Rejection reason
              </label>
              <textarea
                id="reject-reason"
                value={rejectReason}
                onChange={(e) => setRejectReason(e.target.value)}
                className="mb-3 w-full rounded-md border border-ink-300 px-3 py-2 text-sm focus:border-brand-500 focus:outline-none"
                rows={3}
              />
              <button
                type="button"
                onClick={handleReject}
                disabled={reject.isPending || !rejectReason.trim()}
                className="rounded-md bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
              >
                Confirm rejection
              </button>
            </div>
          )}
        </div>
      )}

      {data.profile_status === "rejected" && data.profile_reject_reason && (
        <p className="rounded-md bg-surface-100 px-3 py-2 text-sm text-ink-700">
          Last rejection reason: {data.profile_reject_reason}
        </p>
      )}
    </div>
  );
}
```

The Approve/Reject buttons are only rendered `data.profile_status === "pending_review"` — this
is a client-side UX convenience only, mirroring the pattern already used for the client-side
admin-type check in `admin/src/pages/LoginPage.tsx` (documented there as UX-only, with the
server's `profileapproval.ErrNotPending` check as the real enforcement). Both endpoints already
independently enforce the same precondition server-side (Task 9), so hiding the buttons here is
not a substitute for that check, only a convenience against showing an action a user is about to
have rejected by the API anyway.

- [ ] **Step 3: Wire the routes**

Read `admin/src/App.tsx` first. Add two new `<Route>` entries inside the existing
`<Route element={<AdminLayout />}>` block, alongside the existing `/users` route:

```typescript
          <Route path="/profile-reviews" element={<ProfileReviewsPage />} />
          <Route path="/profile-reviews/:userId" element={<ProfileReviewDetailPage />} />
```

And add the two new imports at the top of the file, alongside the existing page imports:

```typescript
import { ProfileReviewsPage } from "./pages/ProfileReviewsPage";
import { ProfileReviewDetailPage } from "./pages/ProfileReviewDetailPage";
```

- [ ] **Step 4: Add the sidebar nav entry**

Read `admin/src/components/layout/Sidebar.tsx` first. Its `NAV_ITEMS` array currently has
two entries (`Overview`, `Users`). Add a third:

```typescript
  { to: "/profile-reviews", label: "Profile Reviews", icon: ClipboardCheck },
```

Add `ClipboardCheck` to the existing `lucide-react` import at the top of the file (it currently
imports `LayoutDashboard, Users` from `"lucide-react"` — change that line to
`import { LayoutDashboard, Users, ClipboardCheck } from "lucide-react";`).

- [ ] **Step 5: Type-check**

Run: `cd admin && npm run build`
Expected: succeeds with no type errors.

- [ ] **Step 6: Lint**

Run: `cd admin && npm run lint`
Expected: no errors.

- [ ] **Step 7: Manual smoke check**

Run: `cd admin && npm run dev`, then in a browser (or via the `run` skill if available in this
session) visit `/profile-reviews` while logged in as an admin against a running backend that
includes this plan's Task 9 routes. Confirm: the page loads without a console error, the empty
state renders correctly if there are no `pending_review` profiles, and clicking a row (once at
least one exists) navigates to the detail page and shows Approve/Reject actions. This is a
manual step since this project has no automated frontend tests (Global Constraints).

- [ ] **Step 8: Commit**

```bash
cd admin
git add src/pages/ProfileReviewsPage.tsx src/pages/ProfileReviewDetailPage.tsx \
        src/components/layout/Sidebar.tsx src/App.tsx
git commit -m "feat: add profile reviews list and detail pages"
```

---

## Post-plan manual step (not a task — operational, run once before/during deploy)

Run the Task 3 backfill script against the real database before this feature reaches any
environment with existing artisan users, first as a dry run to sanity-check the counts, then
with `-write`:

```bash
cd backend
go run ./scripts/backfillprofilestatus            # dry run — read the printed counts
go run ./scripts/backfillprofilestatus -write      # apply
```

Skipping this means every existing artisan has `ProfileStatus == ""`, which
`users.EffectiveProfileStatus` treats as `not_submitted` — meaning `StartJourney` and job
applications would start rejecting *every* existing artisan the moment Task 5 ships, including
those already certified. This step is what prevents that.
