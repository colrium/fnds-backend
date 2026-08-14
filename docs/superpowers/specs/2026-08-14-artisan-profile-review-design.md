# Artisan Profile Review (Admin Approval Gate)

**Status:** approved
**Date:** 2026-08-14
**Repos touched:** `backend/`, `admin/`

## 1. Problem

Artisans can currently complete their profile and immediately start the assessment
journey (`assessments.StartJourney`) or apply for jobs/vacancies
(`jobrequests.HandleCreateJobApplication`) the moment `profiles.GetProfileCompletion`
reports complete. No human ever looks at the profile content before the artisan can
act on it. There is no admin-facing review step, and no field anywhere records
whether a profile has been vetted.

This spec adds a standalone approval gate — independent of the existing assessment
journey pipeline (`assessments.Journey`, stages `profile_review → oral → written →
practical → certified`) — that an artisan must pass before they can start an
assessment journey or apply for a job.

**Why not reuse the existing `assessments.StageProfileReview` stage instead of adding
a new field?** That stage only exists inside a `Journey`, which itself only gets
created *after* `StartJourney` succeeds. Gating `StartJourney` on a stage that lives
inside the journey `StartJourney` creates is circular. A field on `User` is the only
place that can gate journey creation itself, and it is also the natural place to gate
job applications, which have no assessment journey involved at all.

## 2. Data model

New fields on `users.User` (`backend/users/model.go`):

```go
ProfileStatus       string     `json:"profile_status" bson:"profile_status"`
ProfileSubmittedAt  *time.Time `json:"profile_submitted_at,omitempty" bson:"profile_submitted_at,omitempty"`
ProfileReviewedAt   *time.Time `json:"profile_reviewed_at,omitempty" bson:"profile_reviewed_at,omitempty"`
ProfileReviewedBy   string     `json:"profile_reviewed_by,omitempty" bson:"profile_reviewed_by,omitempty"`
ProfileRejectReason string     `json:"profile_reject_reason,omitempty" bson:"profile_reject_reason,omitempty"`
```

`ProfileStatus` is one of four string constants (new file `users/profile_status.go`,
following the existing `Status*` constant pattern in `users/model.go`):

```go
const (
	ProfileStatusNotSubmitted  = "not_submitted"
	ProfileStatusPendingReview = "pending_review"
	ProfileStatusApproved      = "approved"
	ProfileStatusRejected      = "rejected"
)
```

Default for new users: `not_submitted` (zero value in Go is `""`; every path that
reads `ProfileStatus` must treat `""` as `not_submitted` — see §6 edge cases — but new
inserts explicitly set the constant so the stored value is never blank).

### Why four states, not three

`not_submitted` vs `pending_review` distinguishes "hasn't tried yet" from "waiting on
an admin" — both in the admin queue (which only ever lists `pending_review`) and in
any future analytics on submission funnel. Collapsing them would make it impossible
to tell "artisan never bothered" from "artisan is stuck waiting."

## 3. State machine

```
not_submitted --(artisan: POST /profile/submit-review, profile complete)--> pending_review
pending_review --(admin: approve)--> approved
pending_review --(admin: reject, reason required)--> rejected
approved --(artisan: profile edited via PATCH /profile)--> pending_review   [automatic]
rejected --(artisan: profile edited AND resubmits)--> pending_review        [explicit]
```

Editing while `rejected` does **not** by itself move the state — the artisan must
explicitly call `submit-review` again after fixing their profile. This is
deliberate: "I changed something" and "I believe I've fixed the problem and want
re-review" are different signals, and conflating them would move a half-fixed
profile back into the queue on every keystroke-adjacent save.

Editing while `approved` **does** automatically revert to `pending_review` — no
separate artisan action needed, because an approved artisan has no reason to expect
they need to "resubmit"; the system silently protects the admin's prior approval from
covering content the admin never saw.

Editing while `pending_review` is a no-op on `ProfileStatus` (already pending;
nothing to revert from).

## 4. Backend components

### 4.1 New package: `profileapproval`

Placed as its own package (not inside `profiles` or `users`) because it orchestrates
across both: it reads `profiles.GetProfileCompletion`, writes `users.User` fields, and
triggers `notifications.Service`. Putting it in either `profiles` or `users` would
create a dependency in the wrong direction (`users` must not import `profiles`, per
the existing layering — `profiles` already imports `users`, not the reverse).

```go
package profileapproval

var (
	ErrProfileIncomplete = errors.New("complete your profile before submitting it for review")
	ErrAlreadyPending     = errors.New("your profile is already pending review")
	ErrNotPending         = errors.New("this profile is not currently pending review")
	ErrReasonRequired     = errors.New("a rejection reason is required")
)

func SubmitForReview(ctx context.Context, userID string) error
func Approve(ctx context.Context, userID, reviewedBy string) error
func Reject(ctx context.Context, userID, reviewedBy, reason string) error

// RevertToPendingIfApproved is called by profiles.UpdateProfile after a
// successful update. No-op unless the user's current status is "approved".
func RevertToPendingIfApproved(ctx context.Context, userID string) error
```

**`SubmitForReview`**:
1. Load user. If `ProfileStatus == pending_review` → `ErrAlreadyPending`.
2. Load profile via `profiles.GetFullProfile`; if `!profiles.GetProfileCompletion(profile).Complete` → `ErrProfileIncomplete` (same completeness gate `StartJourney` and job applications already use — "complete" means one thing across the product, per the existing comment on `StartJourney`).
3. `users.UpdateUser(userID, bson.M{"profile_status": ProfileStatusPendingReview, "profile_submitted_at": now, "profile_reject_reason": ""})` — clears any stale rejection reason so a subsequent rejection is never confused with a prior one.

**`Approve`**:
1. Load user. If `ProfileStatus != pending_review` → `ErrNotPending` (covers the double-admin race in §6).
2. `users.UpdateUser(userID, bson.M{"profile_status": ProfileStatusApproved, "profile_reviewed_at": now, "profile_reviewed_by": reviewedBy})`.
3. Fire `notifications.Service.NotifyProfileReviewDecision` (approved).

**`Reject`**:
1. `reason` trimmed; empty → `ErrReasonRequired`.
2. Load user. If `ProfileStatus != pending_review` → `ErrNotPending`.
3. `users.UpdateUser(userID, bson.M{"profile_status": ProfileStatusRejected, "profile_reviewed_at": now, "profile_reviewed_by": reviewedBy, "profile_reject_reason": reason})`.
4. Fire `notifications.Service.NotifyProfileReviewDecision` (rejected, includes reason).

**`RevertToPendingIfApproved`**: load user; if `ProfileStatus != approved`, return nil;
else `users.UpdateUser(userID, bson.M{"profile_status": ProfileStatusPendingReview})`
— deliberately does **not** touch `ProfileSubmittedAt` (this is a system-triggered
re-entry into the queue, not a new artisan submission) and does **not** clear
`ProfileReviewedBy`/`ProfileReviewedAt` from the prior approval (kept as history of
the last real review action until the next one overwrites it).

### 4.2 Hook into `profiles.UpdateProfile`

`profiles/handlers.go` `HandleUpdateProfile`, after the existing successful
`UpdateProfile` call and cache invalidation, add:

```go
if err := profileapproval.RevertToPendingIfApproved(ctx.Request().Context(), userID); err != nil {
	slog.ErrorContext(ctx.Request().Context(), "failed to revert profile status after edit", "user_id", userID, "error", err)
}
```

Logged, not surfaced to the caller — the profile update itself succeeded; a failure
to revert the review flag is a background-consistency concern, not a reason to fail
the user's save (mirrors the existing cache-invalidation error handling three lines
above it, which is also log-only).

### 4.3 Gating `StartJourney`

`assessments/service.go`, in `StartJourney`, after the existing
`!profiles.GetProfileCompletion(profile).Complete` check:

```go
user, err := users.GetUserByID(ctx, userID)
if err != nil {
	return nil, fmt.Errorf("loading user: %w", err)
}
if user.ProfileStatus != users.ProfileStatusApproved {
	return nil, ErrProfileNotApproved
}
```

New sentinel `ErrProfileNotApproved = errors.New("your profile must be approved before starting assessments")`
alongside the existing `ErrProfileIncomplete` in `assessments/service.go`.
`HandleStartJourney` gains one more `errors.Is` branch mapping it to 400, matching the
existing `ErrProfileIncomplete` branch exactly.

### 4.4 Gating job applications

`jobrequests/routes.go` `HandleCreateJobApplication`, after the existing profile
completeness check:

```go
if account.ProfileStatus != users.ProfileStatusApproved {
	return responses.SendError(ctx, http.StatusForbidden, "Your profile must be approved before applying for vacancies")
}
```

`account` is already loaded earlier in this handler (the existing `users.GetUserByID`
call used for the artisan-type check) — no new lookup needed.

### 4.5 New artisan route

`profiles/routes.go`, authenticated group:

```go
g.POST("/profile/submit-review", HandleSubmitProfileForReview)
```

Handler follows the existing `HandleUpdateProfile` shape: get user ID from context,
call `profileapproval.SubmitForReview`, map `ErrProfileIncomplete` → 400,
`ErrAlreadyPending` → 409, else 200.

### 4.6 New admin routes (`adminapi`)

Following the existing `Page[T]` / `parsePageLimit` convention from
`adminapi/pagination.go`:

| Method | Path | Body | Notes |
|---|---|---|---|
| GET | `/admin/profile-reviews` | — | Query: `status` (default `pending_review`), `page`, `limit`. Lists `users.User` filtered by `profile_status`. |
| GET | `/admin/profile-reviews/:user_id` | — | Full user + `profiles.GetFullProfile` for the detail view. |
| POST | `/admin/profile-reviews/:user_id/approve` | — | Calls `profileapproval.Approve`, `reviewedBy` = the admin's own user ID from context. |
| POST | `/admin/profile-reviews/:user_id/reject` | `{"reason": "..."}` | 400 if `reason` empty (also enforced in `profileapproval.Reject`, defense in depth). |

Mirrors `adminapi/users.go`'s handler style exactly (same error-status mapping
conventions, same `Page[T]` envelope for the list).

### 4.7 Notifications

New event type in `notifications/model.go`: `TypeProfileReviewStatus = "profile_review_status"`.

New method on `notifications.Service`, mirroring `NotifyApplicationDecision`:

```go
func (s *Service) NotifyProfileReviewDecision(ctx context.Context, event ProfileReviewDecisionEvent) error
```

`ProfileReviewDecisionEvent{UserID, Status, Reason}`. Approved →
`"Your profile has been approved. You can now start assessments or apply for jobs."`
Rejected → `"Your profile was not approved: {reason}"`. No SMS delivery channel (unlike
`NotifyApplicationDecision`'s accepted-application SMS) — this is an in-app-only
notification; nothing in the requirements calls for SMS here, and adding it
unrequested would be scope creep.

## 5. Frontend (`admin/`)

New page `ProfileReviewsPage.tsx`, structurally identical to the existing
`UsersPage.tsx`: `DataTable` + `Pagination` + `FilterSelect` (status), row click opens
a detail view. Detail view shows the profile summary (skills, experience, documents if
present) plus Approve and Reject actions. Reject opens a small modal requiring a
non-empty reason before the button is enabled (client-side mirror of the server's
`ErrReasonRequired`, not a replacement for it).

New files:
- `src/types/profileReview.ts` — mirrors the `User` fields added in §2, plus a
  `ProfileDetail` type combining user + profile for the detail view.
- `src/api/profileReviews.ts` — `list`, `get`, `approve`, `reject`.
- `src/hooks/useProfileReviews.ts` — TanStack Query hooks, following `useUsers.ts`'s
  shape (list query keyed on filters, mutations invalidate the list on success).
- `src/pages/ProfileReviewsPage.tsx`, `src/pages/ProfileReviewDetailPage.tsx`.

`Sidebar.tsx` gains one nav entry: "Profile Reviews".

## 6. Edge cases and side effects

| Case | Behavior | Why |
|---|---|---|
| Two `submit-review` calls race | Second gets `ErrAlreadyPending` (409) | `SubmitForReview` checks current status before writing; no double-submission artifact. |
| Two admins act on the same profile | Second gets `ErrNotPending` | Both `Approve`/`Reject` require `ProfileStatus == pending_review` as a precondition; the loser sees a clean "not pending" error instead of silently overwriting the first admin's decision. |
| Artisan edits profile while an admin has the review screen open | No error; edit hook is a no-op since status is already `pending_review` | The admin may review slightly stale data — an ordinary, pre-existing kind of race (identical to any two people looking at the same document), not something this feature introduces. |
| Existing artisan mid-assessment-journey or already certified, before this ships | Backfilled to `approved` | Confirmed decision: grandfather in, do not retroactively block active users. Migration script, artisan users only, gated on `profiles.GetProfileCompletion`. |
| Existing artisan with an incomplete profile, before this ships | Backfilled to `not_submitted` | Consistent with "not submitted yet" for anyone who was never gated by anything before. |
| Artisan already has an in-flight assessment journey or pending job applications, then edits their profile (reverting to `pending_review`) or is rejected on a later re-review | Untouched — only *new* `StartJourney`/`HandleCreateJobApplication` calls are gated | Confirmed decision: no pause/freeze cascade. The gate is a point-in-time check on new actions, not a continuously-enforced invariant on old ones. |
| `ProfileStatus` is the Go zero value `""` (should never happen post-migration, but a defensive read) | Treated as `not_submitted` everywhere it's compared, via a helper rather than repeating `== "" || ==` at every call site | See §4.1 — actual comparisons should go through a small helper `users.EffectiveProfileStatus(u *User) string` returning `ProfileStatusNotSubmitted` when the stored value is empty, so `StartJourney`, job applications, and the admin list filter never each reinvent this defensively and diverge. |
| Rejection with an empty reason | 400 at both the HTTP layer (admin route body validation) and the domain layer (`ErrReasonRequired` in `profileapproval.Reject`) | Defense in depth — the domain check is authoritative; the HTTP-layer check exists only to fail fast without a DB round-trip. |
| Non-admin calls the new `/admin/profile-reviews*` routes | 401, same as every other route under `adminGroup` | These routes are registered on the existing `adminGroup` (`JWTAuth` + `IsAdmin`), no new middleware needed. |
| Notification delivery failure (e.g., store write fails) | Logged, does not fail the approve/reject HTTP call | Mirrors the existing pattern where `notifyOps` / cache invalidation failures are logged, not surfaced — the review decision itself already succeeded in the database by the time the notification fires. |

## 7. Testing plan

**`profileapproval` package** (new, `mtest`-based per the existing `adminapi`/`users`
test convention):
- `SubmitForReview`: incomplete profile → `ErrProfileIncomplete`; already pending → `ErrAlreadyPending`; happy path sets status + timestamp + clears prior reject reason.
- `Approve`: not-pending → `ErrNotPending`; happy path sets status + reviewer + timestamp; fires notification.
- `Reject`: empty reason → `ErrReasonRequired`; not-pending → `ErrNotPending`; happy path sets status + reason + reviewer; fires notification.
- `RevertToPendingIfApproved`: no-op when not approved; reverts when approved; leaves `ProfileSubmittedAt` and `ProfileReviewedBy`/`ProfileReviewedAt` untouched (regression test for the deliberate behavior in §4.1).

**`assessments.StartJourney`**: add a case — complete profile, not approved → `ErrProfileNotApproved`; complete profile, approved → succeeds (extends the existing `TestStartJourney*` table).

**`jobrequests.HandleCreateJobApplication`**: add a case — complete profile, not approved → 403 with the new message (extends existing handler tests).

**`adminapi` new file `profile_reviews_test.go`**: list filters by status; approve/reject happy paths and error-status mapping; end-to-end through the real `JWTAuth`+`IsAdmin` chain for at least one route, following the `admin_group_test.go` pattern established in the prior admin-dashboard work (a bare route-registration test is not sufficient on its own, per that precedent).

**`profiles.HandleUpdateProfile`**: add a case confirming `RevertToPendingIfApproved` is invoked and its failure is logged, not surfaced (status still 200).

**Frontend**: no automated tests, consistent with the standing decision recorded in
the admin-dashboard plan (`admin/.planning/plans/admin-dashboard.md` §8) to exclude
frontend tests for this app; `tsc -b` + `eslint` remain the enforced gates.

**Migration script**: `backend/seeders` is idempotent reference-data seeding
(locations, skills, banks) — a different concern from a one-time user-data backfill,
so it doesn't belong there. `backend/scripts/` already holds exactly this kind of
one-off tool (see `scripts/importusers`, a `main` package with its own doc-comment
header, run via `go run ./scripts/<name>`). New `backend/scripts/backfillprofilestatus/main.go`
following that same shape: iterates artisan users, computes
`profiles.GetProfileCompletion`, and sets `ProfileStatus` per §6's backfill table.
Dry-run flag prints counts before writing, matching `importusers`' `-file`-style flag
convention.

## 8. Out of scope (explicitly, per this session's decisions)

- Reusing/merging with the `assessments.StageProfileReview` stage — kept separate, see §1.
- Pausing or freezing in-flight assessment journeys or job applications on rejection/re-pending — see §6.
- SMS delivery for review decisions — in-app notification only.
- Any change to `assessments.Journey`'s own `profile_review` stage, `ApplyDecision`, or the existing `/admin/assessments/journeys/*` routes — those are untouched by this spec.
