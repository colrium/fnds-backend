# Artisan Assessment Pipeline — Design

Status: **approved, implementation not started** — D1, D2, D4, D6, D7 decided 2026-08-06. D3 narrowed to "port the working PHP correctly" after reviewing the legacy system. The `is_verified` ownership half of D5 remains open.
Date: 2026-08-06 · Revised after reviewing `fundisapp/fundis-be`

Gates an artisan from "profile 100% complete" through four assessment stages to a
verified, discoverable listing.

## Prior art — this has been built twice before

Read this section before the design. The legacy Laravel backend
(`fundisapp/fundis-be`, branch `staging`) contains a **complete assessment system
that ran in production**, and the Go rewrite contains a partial port of it that
was deleted. Almost every open question in this design was already answered once,
usually badly, and the failure modes are documented below rather than rediscovered.

**Two unreconciled generations existed in Laravel simultaneously:**

- **Gen 1 (2022)** — `competency_assessments`, `vetting_reports`,
  `upskilling_reports`. Offline paper capture: an admin typed in what happened at
  an in-person session. Free-text `assessor_name`, `skill`, `assessment_venue`.
  Never linked to Gen 2, never affected any flag. Authorised by Spatie
  permissions on `user_type = admin`.
- **Gen 2 (Jul–Nov 2024)** — `assessment_types`, `assessments`,
  `assessment_questions`, `assessment_options`, `assessment_responses`,
  `time_slots`, `appointments`, `assessment_reports`. The real pipeline.
  Authorised by `user_type = assessor` middleware.

**Both are now dead code.** The role-scoped route files that mounted Gen 2
(`routes/admin_api.php`, `artisan_api.php`, `assessor_api.php`) exist only on
feature branches — they were wiped from `staging` by three consecutive
`chore: fix merge conflicts` commits and nobody noticed, because there is not one
test covering Gen 2. The Go rewrite then repeated the same failure: the ported
`assessments` package was deleted in `368202e`, and `IsAssessor` is still wired to
nothing. **Two independent losses of the same feature to unmounted routes.**

### The legacy stage model

There was no state machine. No `stage` column, no journey entity, no ordering.
"Where is this artisan" was inferred at read time from three unrelated signals: a
computed `completed_assessment` accessor, `appointments.status`
(`scheduled|completed|cancelled`), and whether an `assessment_reports` row existed.
The de facto order was:

```
artisan attaches skills to profile
  → GET /artisan/assessments  (every assessment matching their skills, ungated, all types at once)
  → theory / "multiple options": POST /assessments/submit        → upserts theoretical_score
  → practical: POST /assessments/submit/practical                → uploads ONE VIDEO per question
  → oral: POST /artisan/appointments (books a global slot, mints a VideoSDK room)
      → assessor rates 0–5, flips appointment to completed
  → assessor finalises: POST /assessor/assessments/report        → grade, recommendation
```

Note what this means: **the legacy "practical" stage was an artisan-uploaded
video, not an in-person exam.** There was never a venue, centre or partner
institution in Gen 2 — the only venue field in the entire repo is Gen 1's
free-text `assessment_venue`, typed by an admin after the fact. The in-person
practical at a verification centre is genuinely new. See
[Remote practical](#remote-practical-fallback) for why the video path is still
worth keeping.

### Must not repeat

Concrete defects found in the legacy implementation, each of which this design
has to actively prevent:

1. **The artisan set their own grade.** `Artisan/AssessmentResponseController`
   built its report payload with `'grade' => $request->grade`, and `grade` was
   not in the validator. The subject of the assessment supplied their own result.
   *This is the single worst defect in the system.*
2. **The answer key was shipped to the client.** Questions were returned with
   `->with('options')` and `Option` had no `$hidden`, so `is_correct` went out on
   every fetch. Already fixed here by `json:"-"` on `CorrectOptionIDs`/`Rubric`.
3. **Scoring never checked the option belonged to the question.**
   `Option::where('id', $response->response)` — submitting any correct option ID
   from any question in the database scored a point.
4. **The drawn question set was never persisted**, and two controllers drew
   different counts (`take(20)` admin, `take(21)` artisan). Scores had no
   denominator and were uninterpretable.
5. **One attempt, ever, with no transaction.** Submission looped inserts with no
   `DB::transaction`, and resubmission was rejected outright. A dropped
   connection mid-quiz left partial rows that **locked the artisan out
   permanently**, with no admin reset path. This is the strongest possible
   argument for the pause/resume and attempt decisions.
6. **`UNIQUE(artisan_id, assessment_id)` on appointments.** A business state flag
   inside a unique constraint, so rebooking required hard-`delete()`ing the old
   appointment. All reschedule and failure history was destroyed by design.
7. **Slot booking raced.** `if (! $timeSlot->is_available)` was a plain read
   outside any lock or transaction; two artisans booking concurrently both passed.
8. **Slots had no owner, skill or capacity.** `time_slots` was a global singleton
   calendar shared across every assessor and every trade.
9. **No assessor was ever assigned to an appointment.** `appointments` had no
   `assessor_id`; the assessor was recorded post-hoc as `completed_by`. Assessor
   eligibility was self-asserted by editing the skills on their own profile.
10. **Unscoped assessor mutations.** `completeAppointment($id)` and
    `rateOralOrPracticalAssessment` had no skill or ownership scoping — any
    assessor could complete or score any appointment by ID. And
    `fetchAssessmentReportById(int $id)` ignored `$id` entirely.
11. **A per-session event log was collapsed into a mutable summary row.**
    `oral_assessment_responses` (rating, comments, assessor_id, appointment_id)
    was created and dropped 23 days later into `assessment_reports`, losing all
    per-session history and multi-assessor attribution. The dropping migration
    was named `make_assessor_id_column_nullable...` and its `down()` does not
    recreate the table.
12. **First-write-wins finalisation.** The assessor's report used
    `$report->grade ?? $request->grade` for every field, so the assessor
    **could not correct anything already set** — including the grade the artisan
    set themselves.
13. **Mixed score scales in one table.** `theoretical_score` was a raw count
    (0–21), `practical_score`/`oral_score` were 0–5 ratings. No max, no
    weighting, no pass threshold, no normalisation. There was **no passing score
    anywhere in the system** — `grade` was a free string.
14. **No unique index behind `updateOrCreate`.** Every report write was
    `updateOrCreate(['artisan_id'=>…,'skill_id'=>…])` with no matching unique
    index, so concurrent writes created duplicates.
15. **A global video join token.** `GET /videosdk/generate-token` was
    authenticated but role-free and room-free: any logged-in user got a 2-hour
    `allow_join` token valid for any room.
16. **Reminders that never fired.** `HourlyAppointmentReminder` matched
    `whereTime('start_time', now()->addHours(2)->format('H:i:s'))` — an exact
    second-level equality against cron fire time. Nothing recorded that a
    reminder was sent, so re-runs re-notified. Both reminder commands were
    push-only (OneSignal) and silently skipped artisans without a `device_key`.
17. **The one email that did fire omitted the meeting link.**
    `Notifications/AppointmentReminder` listed date and time but never
    `meeting_id`, so nothing told the artisan how to join their own call.
18. **Two authorization models for one role.** Gen 1's "assessor" was
    `user_type=admin` + a Spatie role; Gen 2's was `user_type=assessor`.
19. **No update or delete for assessments or questions, ever.** Admin could
    create only. A typo in a question was permanent.
20. **`text` question type was unreachable.** The DB enum allowed
    `text|multiple_choice|boolean|video` but both write endpoints validated
    `in:multiple_choice,boolean,video`. Free-text existed in the schema and never
    in the product — so the Bedrock free-text scoring decision has no legacy
    precedent to lean on.

21. **The VideoSDK API key *and secret* are hardcoded in the shipped assessor
    app** (`fundis-assessor/lib/features/assessor/video-call/api/video_api.dart`).
    The client mints its own HS256 JWT and calls `POST
    https://api.videosdk.live/v2/rooms` directly, so the server is not involved in
    room creation at all. Anyone who unzips the APK holds the account secret.
    **This is a live incident, not a design note: rotate the secret.** It also
    settles the argument about server-side token minting — there is nothing to
    debate, the current arrangement gives every installed client the master key.
    `api_keys.dart` additionally carries plaintext test credentials and phone
    numbers.
22. **The oral questionnaire was display-only.** The assessor app renders a
    10-category, 24-question checklist from a hardcoded Dart constant
    (`assessment_checklist_data.dart`) on its own screen, and **never submits any
    of it**. The structured judgement an assessor formed was thrown away; only a
    single 1–5 number and a free-text comment survived.
23. **The assessor's grade was silently discarded.** The app posts `grade`,
    `practical_score`, `oral_score`, `comment` and `recommendation` to
    `/assessor/assessments/report` — into the first-write-wins handler (defect 12).
    Since the artisan's own submission had already created the row with their
    self-set `grade`, **the assessor's grade never took effect.** Defects 1 and 12
    compound into a system where the assessor's verdict was decorative.
24. **No offline capability despite on-site work.** `hive` is present but used only
    for session storage. Assessors working at sites and partner centres have poor
    connectivity, and every scoring action is a bare online POST with no queue or
    retry.

### Worth keeping from the legacy design

Not everything was wrong:

- **`instruction` + `what_to_submit` on questions** (added 2024-11-20) — the task
  brief and the evidence spec for a practical question. This is the closest thing
  the old system had to a rubric, and it was never shown to assessors. Adopted
  below for practical tasks.
- **`recommendation`** on the report — an upskilling suggestion distinct from
  pass/fail. Good: it makes a failure actionable.
- **Per-modality comments** (`practical_comment`, `oral_comment`) — added late and
  then mostly unused because the rating endpoint kept writing the shared
  `comment` field. Correct instinct, incomplete follow-through.
- **`completed_by` + `completed_at`** on appointments — who actually ran the
  session, which matters for audit even when an assessor was never assigned.
- **Bulk slot generation** — `TimeSlotService::generateTimeSlots()` took
  `dates[]`, `start_time`, `end_time`, `slot_duration`, `break_duration`,
  `slots_before_break`. Genuinely useful for staffing a centre for a day, and
  worth porting (with batched inserts and a duplicate guard, which it lacked).
- **Presigned-URL evidence review** — assessors read the artisan's practical
  video through a 30-minute presigned S3 URL. The Go `files/` package already
  does this properly.

## Existing ground truth

Facts from the current codebase that constrain this design:

| Thing | Where | Relevance |
|---|---|---|
| `GetProfileCompletion` → `{complete, percent, missing_sections}` over 6 sections | `profiles/completion.go` | The stage-0 gate. Already used as a gate at `jobrequests/routes.go:541`, so the precedent exists. |
| `User.IsVerified` + `VerifiedUntil *time.Time` | `users/model.go:40-41` | The only verification state today. |
| `worker_verified_month`, **1000 KES / 30 days**, "Verified and Discoverable" | `subscriptions/catalog.go` | **The 1000 KES discoverability charge already exists** and already sets `is_verified`/`verified_until` (`subscriptions/model.go:293`). See the conflict below. |
| `middleware.IsAdmin`, `middleware.IsAssessor` | `middleware/roles.go` | Written and tested, but **no admin/assessor route group is wired in `main.go`**. |
| `assessor` is a valid registration type | `auth/routes.go:409` | Assessors can already sign up; nothing is exposed to them. |
| An `assessments` package existed and was deleted | commit `368202e` (2026-07-16) | Recover shapes with `git show 368202e^:assessments/model.go`. Its routes had **no role checks and no ownership scoping** — do not restore as-is. |
| VideoSDK integration | `integrations/videosdk.go` | **Broken**: hex-encodes the JWT header/payload instead of base64url, so it emits an invalid token. No room creation. Env vars missing from `.env.example` and `configs/env.go`. Unused. **A working reference implementation exists** — see the legacy row below. |
| Working VideoSDK service | `fundis-be/app/Services/VideoSdkService.php` | The Go file is a broken transcription of this. The PHP mints a proper HS256 JWT via `firebase/php-jwt` (`apikey`, `permissions:['allow_join']`, 2h expiry) and `createMeeting()` does `POST {base}/rooms` returning `roomId`. Config at `config/services.php:107-111`, env keys `VIDEOSDK_API_KEY`/`VIDEOSDK_API_SECRET`/`VIDEOSDK_BASE_URL=https://api.videosdk.live/v2`. |
| Legacy assessment schema | `fundis-be/database/migrations/2024_07_31_*`, `2024_08_10_*`, `2024_08_27_*` | The production data model. Worth reading before finalising collections — the [Must not repeat](#must-not-repeat) list is drawn from it. |
| Legacy practical = video upload | `fundis-be/.../Artisan/AssessmentResponseController::submitPracticalAssessment` | 100 MB `mp4/mov/avi/wmv` to S3, one per question, reviewed by the assessor via presigned URL. **There was never an in-person practical.** |
| `institutions` package | `institutions/model.go` | **Cannot serve as exam centres.** `Institution{ID, Name, Type}` has no location field, and semantically it is the "where did you train" lookup for the profile wizard. Exam venues need their own collection with a location — see [Stage 4](#stage-4--practical-exam). |
| `payments.RegisterChargeCompletedHandler` | `payments/routes.go:28-31` | The hook if any stage is paid. Integer KES amounts. |
| `jobs.Scheduler` (hourly ticker) | `jobs/scheduler.go` | Exists but is **never started from `main.go`**. Needed for appointment reminders. |
| No AI/LLM dependency in either repo | — | Confirmed absent from the Go backend *and* the legacy Laravel backend. Bedrock scoring is fully greenfield, with no prior art to lean on. |
| No fee on any legacy assessment stage | `fundis-be` | Confirmed: no fee column, no charge in the booking or submission paths, no assessment route behind `subscription` middleware. The 1000 KES practical fee is **new revenue on a previously free flow** — worth watching in funnel data. |
| Profile requires **exactly one** skill | `profiles/completion.go` `hasSingleValue` | A journey is scoped to one skill. Simplifies v1. |

## The verification conflict

Today an artisan pays 1000 KES and receives the verified badge. No assessment,
no competence signal. `subscriptions/model.go:293` writes `is_verified: true` on
plan activation, and `users.ListArtisans` surfaces them.

So "verified" currently means *paid*, and this feature wants it to mean
*assessed*. Those cannot both own the same flag. This is the central decision,
not an implementation detail — see D5.

## Stage model

One journey document per artisan (per skill), holding the current stage plus
per-stage outcomes. This is preferred over the legacy flat
`Appointment`/`AssessmentResponse` pair because the artisan-facing UI and the
admin queue both need "where is this person" as a single cheap read.

```
profile_complete ──> profile_review ──> oral ──> written ──> practical ──> certified
                          │              │         │            │
                          └─ rejected    └─ ...    └─ ...       └─ ... (re-entry, see D1)
```

Stage and status as typed string consts, mirroring the
`subscriptions/catalog.go` `Entitlement`/`PlanGroup` style.

```go
type Stage string

const (
    StageProfileReview Stage = "profile_review"
    StageOral          Stage = "oral"
    StageWritten       Stage = "written"
    StagePractical     Stage = "practical"
    StageCertified     Stage = "certified"
)

type StageStatus string

const (
    StatusLocked          StageStatus = "locked"           // prior stage not passed
    StatusAvailable       StageStatus = "available"        // artisan may start/book
    StatusAwaitingPayment StageStatus = "awaiting_payment" // practical: booked, fee unpaid
    StatusScheduled       StageStatus = "scheduled"        // appointment booked
    StatusInProgress      StageStatus = "in_progress"      // written: attempt open
    StatusAwaitingReview  StageStatus = "awaiting_review"  // submitted, needs a human
    StatusPassed          StageStatus = "passed"
    StatusFailed          StageStatus = "failed"
)
```

`components/ui/StatusBadge.tsx` already styles `pending`/`scheduled`/`completed`/
`rejected`/`failed`, so the frontend gets most of these for free.

### Collections

- `assessment_journeys` — one per artisan+skill. Current stage, per-stage outcome
  subdocuments, `skill_id`, timestamps.
- `assessment_questions` — the bank. Per skill, with `status` (`draft`/`approved`/
  `retired`), `type`, `points`, and an authoring provenance field (D6).
- `assessment_attempts` — one per written attempt. Holds the **question snapshot**
  (the drawn, order-randomized set), per-answer autosave, pause/resume state,
  integrity signals, score.
- `assessment_appointments` — oral and practical bookings. `journey_id`,
  **`assessor_id` assigned at booking time** (legacy defect 9), `centre_id` and
  `practical_task_id` for the practical stage, slot, status, `completed_by`,
  `completed_at`.
- `assessment_sessions` — the **append-only** record of what happened in each
  oral or practical session: assessor, scores, per-modality comments,
  recommendation, evidence file IDs. Separate from the appointment on purpose:
  legacy collapsed its `oral_assessment_responses` event log into one mutable
  summary row and lost all per-session and multi-assessor history (defect 11). A
  second sitting appends a row; nothing is overwritten.
- `assessor_slots` — assessor availability, carrying **`assessor_id`, `stage`,
  `centre_id` (practical) and a capacity**. A global ownerless calendar shared
  across every trade was unusable (defect 8). Booking is a conditional atomic
  update, never read-then-write (defect 7). Note there is deliberately **no
  `skill_id` on a slot**.
- `assessor_skills` — which skills an assessor is authorised to assess:
  `assessor_id`, `skill_id`, `granted_by`, `granted_at`. **Admin-granted only.**
  This is what drives auto-assignment, and it replaces the legacy arrangement
  where an assessor authorised themselves for a trade by editing the skills on
  their own profile (defect 9).

**Booking is auto-assigned.** The artisan picks a time, not a person; the server
matches a qualified assessor with availability, load-balanced by fewest upcoming
appointments, and retries the next candidate if a claim loses a race. Full flow in
the [assessor web surface spec](./2026-08-06-assessor-web-surface-design.md#auto-assignment).

Every collection that is written with an upsert gets the matching unique index in
`EnsureIndexes` — legacy ran `updateOrCreate` on `assessment_reports` for two
years with no unique index on `(artisan_id, skill_id)` (defect 14).

All: UUID string `_id`, snake_case `json`+`bson`, `created_at`/`updated_at`,
`EnsureIndexes(ctx)` registered in `main.go` alongside the others.

### Routing

Three new groups in `main.go`, mirroring the existing `artisanGroup`:

```go
artisanGroup  // existing — journey read, start written, submit answers, book slots
assessorGroup := api.Group(""); assessorGroup.Use(middleware.JWTAuth, middleware.IsAssessor)
adminGroup    := api.Group(""); adminGroup.Use(middleware.JWTAuth, middleware.IsAdmin)
```

Artisan queries scope by `user_id` **in the Mongo filter**, following
`files.GetByIDForUser` — not by post-fetch checks. This is the specific failure of
the deleted package.

Assessors are different, and deliberately so: **any assessor may read anything
about an assessment**, so reads are not scoped, but **writes are restricted to the
appointment's assigned `assessor_id`**. Legacy had this exactly backwards — it left
mutations unscoped, which is what made the assessor's verdict untraceable
(defect 10). Rationale and the privacy caveat are in the
[assessor web surface spec](./2026-08-06-assessor-web-surface-design.md#authorization).

## Stage 1 — Profile review

Gate: `profiles.GetProfileCompletion(profile).Complete`. On completion the
journey is created in `profile_review` and enters an admin queue. Admin approves
or rejects with a reason; rejection returns the artisan to editing with the
reason surfaced. Frontend already has the terminal-state swap pattern
(`ProfileCompleteView`) to hang "awaiting approval" off.

**Admin approval here is what makes the artisan discoverable** — it is not merely
a gate into the assessment pipeline. Passing this stage sets `is_verified` and puts
the artisan into employer browse results. See
[Three outcomes](#three-outcomes-deliberately-separate).

That makes this the highest-leverage stage in the pipeline: an unreviewed queue
means artisans who completed their profile stay invisible.

### Review SLA — under 48 hours (decided)

Every completed profile is reviewed within 48 hours. To make that a commitment
rather than an aspiration, four things are needed:

- **Tell the artisan.** The "awaiting approval" state should state the 48-hour
  window explicitly. An unexplained wait is the single most common reason someone
  abandons onboarding, and a stated bound costs nothing.
- **Track age, not just depth.** The admin queue sorts oldest-first (the design
  already does this via `ListJourneysByStage` sorting on `updated_at` ascending) and
  surfaces the age of the oldest item. Queue *depth* is a vanity metric; queue *age*
  is the SLA.
- **Alert before the breach, not after.** A scheduled check flags anything
  approaching the window — say at 36 hours — to the existing Slack ops channel
  (`notifications/slack.go`, `integrations/slack_activity.go`). This requires
  `jobs.NewScheduler().Start()` to actually be wired into `main.go`, which it
  currently is not.
- **Notify on decision.** Approval and rejection both go out over the notifications
  outbox (SMS + in-app). A rejection must carry its reason, or the artisan cannot
  act on it.

Two operational consequences worth planning for rather than discovering:

- **Weekends and holidays.** 48 hours means Friday evening submissions need Saturday
  or Sunday attention, or the SLA is really "48 hours on weekdays". Worth stating
  which is meant.
- **Volume.** The SLA is a staffing commitment. A launch campaign that drives a
  spike in completed profiles breaches it unless review capacity scales or the
  simplest cases can be fast-tracked.

This stage is entirely new. Legacy had **no approval gate of any kind**: nothing
checked profile completeness before `GET /artisan/assessments`, which simply
returned every assessment matching the artisan's self-declared skills. The only
vetting artefact was Gen 1's `vetting_reports`, written by an admin after an
offline interview and wired to nothing.

## Stage 2 — Oral exam (video call)

Live call, so it is inherently identity-verifying and proctored. This stage does
the real vetting work.

- Assessor publishes slots; artisan books one; **the assessor is bound to the
  appointment at booking time**, not inferred afterwards from who happened to
  close it.
- Room created server-side at booking. Join tokens minted **per participant, per
  room, at join time, with a short TTL** — legacy handed any authenticated user a
  2-hour token that could join any room (defect 15).
- Assessor works from a template questionnaire per skill and submits structured
  scores plus notes, which append an `assessment_sessions` row.
- The artisan's written score and integrity flags are shown to the assessor
  before the call, so an implausible score gets probed live.

**Porting note.** `integrations/videosdk.go` is a broken transcription of working
PHP: it hex-encodes the JWT header and payload where the original correctly
base64url-encodes them via `firebase/php-jwt`, and it omits room creation
entirely. Fixing it is a port, not a design problem — read
`fundis-be/app/Services/VideoSdkService.php` and mirror it, then add
`VIDEOSDK_*` to `.env.example` and `configs/env.go` (the Go file reads
`os.Getenv` inline, which the repo's own conventions forbid).

Two things the legacy version got wrong that the port must fix: the room was
created **before** the DB transaction, so a failed write leaked an orphan room;
and nothing was ever recorded of the session beyond a status flag — no recording,
no webhook handler, no `recording_url`.

## Stage 3 — Written assessment

15–20 questions, mixed types, drawn per attempt from the per-skill bank. Untimed.

**Opens when the oral result is released**, not merely when the oral session
happens. The assessor's report submission is the release event, so
`POST /assessor/appointments/:id/report` is what unlocks this stage. Marking is by
AI, which leaves feedback for both the artisan and the next assessor.

### Pass mark — 80% (decided)

**Passing at 80% is required to unlock the practical stage.** This supersedes the
earlier "diagnostic, not the gate" framing, and the change has consequences worth
being explicit about rather than discovering later.

**What still holds.** The oral stage comes *first*, and it is a live video call.
So by the time an artisan reaches the written stage their identity has been
established by a human. Whoever sits the written test is at least a known person,
which is a materially better position than an anonymous online exam.

**What no longer holds.** The earlier anti-cheat reasoning rested on the written
stage being low-stakes: if it did not gate anything, cheating gained little. At an
80% gate the incentive to cheat is real, on a test that is untimed, unproctored
and taken at home. Three mitigations follow, and they are now load-bearing rather
than nice-to-have:

- **The bank must be genuinely large.** Drawing 18 from a bank of 20 leaks the
  whole bank within a few attempts, and with unlimited retries an artisan
  eventually sees everything. `WrittenQuestionMinimum = 15` was sized for a
  diagnostic stage and is now far too low. Recommend **a floor of 60–100 approved
  questions per skill** before the stage opens for that trade, and treat an
  under-stocked bank as a blocker on launching a new trade.
- **The answer key must never leave the server** — already enforced by `json:"-"`
  on `CorrectOptionIDs`/`Rubric`, and the reason legacy's leak (defect 2) was fatal.
- **Integrity flags now matter.** They still must not auto-fail, but they should be
  surfaced to the practical assessor alongside the written score so a suspicious
  pattern gets probed in person.

**Retries interact with the threshold.** Unlimited attempts plus a 48h cooldown
plus an 80% bar is effectively mastery learning: an artisan can keep going until
they pass. That is a reasonable stance for an assessment rather than an exam, but
it should be a conscious one — the pass mark measures persistence as much as
knowledge. If that is unwanted, the lever is a large bank plus attempt-count
visibility to the practical assessor, not a retry cap that strands people.

### Scoring rules

Four rules, each fixing a specific legacy defect:

- **The drawn set is snapshotted onto the attempt.** Legacy drew `take(21)` and
  never recorded it, so a score had no denominator and refreshing the page changed
  the paper (defect 4).
- **A chosen option must belong to its question.** Legacy looked options up
  globally by ID, so any correct option ID from anywhere in the bank scored a
  point (defect 3). Validate against the attempt's own snapshot.
- **One normalised scale within the written stage.** Points per question, summed
  to a percentage against the attempt's own max. Legacy mixed a raw 0–21 count
  with 0–5 ratings in one table with no max and no threshold (defect 13). Note
  the written percentage is *not* the same scale as the human stages — see
  [The competency framework](#the-competency-framework).
- **The artisan never supplies any part of their own result.** No score, grade or
  pass field is accepted from the client on any endpoint, for any role, ever.
  Legacy took `grade` straight off the request body in the artisan's own
  submission handler (defect 1).

### Pause and resume (decided)

Allowed, and explicitly **not** treated as a motivation signal.

- Every answer is autosaved on submit (`PATCH .../answers`), so a dropped
  connection, a flat battery or an exhausted data bundle resumes in place rather
  than restarting.
- `paused_at` / `resumed_at` / `pause_count` are recorded on the attempt as **ops
  telemetry only**. They never feed the score.
- Rationale: artisans are on metered mobile data, shared devices and unreliable
  power. Blocking pause, or reading it as disinterest, penalises infrastructure
  rather than interest. Abandonment (started, no submission after N days) is the
  signal worth acting on, via an SMS nudge from `jobs.Scheduler`.

### Re-attempts (decided)

- Unlimited attempts on the written stage, each one retained in full.
- A **cooldown** between attempts (`writtenRetakeCooldown`, default 48h),
  enforced in `service.go` and surfaced to the client as `next_attempt_at`.
- The previous attempt's assessor/AI feedback is **carried forward** onto the new
  attempt (`carried_notes`) so the artisan sees what to improve and the next
  reviewer has continuity.

## Stage 4 — Practical exam

Flow: artisan **picks the question type** (the practical task they want to be
assessed on) → picks a **verification centre** near them → pays the fee → books
a slot.

### Verification centres

A new `verification_centres` collection, not `institutions` (which has no
location and means something else — see the ground-truth table). Each centre
carries a `location_id` referencing the existing `locations` package, plus a
`partner_institution_id` when the venue is a partner school rather than a Fundis
site. "Near me" filters centres by the artisan's `profile.Locations`.

### Fee — 1000 KES (decided)

Charged for the **physical exam specifically**. Stages 1–3 are free.

- Mirrors the Cellulant checkout pattern in `subscriptions/service.go`: create a
  `practical_exam_payments` record holding a `payment_reference`, create a
  `transactions.CellulantTransaction`, return a checkout link.
- Confirmation is gated on `payments.RegisterCellulantPaymentHandler`; success
  codes are 178/188. Booking sits in `awaiting_payment` until the webhook lands,
  then flips to `scheduled`.
- Validation mirrors `validateCellulantPayment`: merchant transaction ID and
  account number must both equal the payment reference, amount must equal the fee
  exactly, currency case-insensitive match, service code match. Without this a
  spoofed webhook books a free exam.
- The fee is idempotent per booking — a replayed webhook must not double-charge
  or double-confirm.

### Task briefs

Each `PracticalTask` carries an **`instruction`** (how the task is to be
performed) and **`what_to_submit`** (the evidence expected). Both are lifted
directly from the legacy `assessment_questions` columns added in November 2024 —
the only rubric-like fields the old system had, and never surfaced to anyone.
Here they are shown to the artisan when picking a task *and* to the assessor
before scoring, which is what makes a practical score defensible.

### Remote practical fallback

Keep the legacy video-upload path as a secondary route: the artisan films the task
and uploads it, an assessor reviews it via a presigned URL. Legacy already ran
this at 100 MB for `mp4/mov/avi/wmv`, so it is proven.

It matters for two reasons. It is the answer for an artisan with no centre within
reach, which is otherwise a hard stop on the whole pipeline. And it is the
degraded path if centre staffing slips — without it, an unstaffed county blocks
certification entirely. The fee question for this route is deliberately left open
below.

### Evidence

Assessors upload results and evidence via the **existing** presigned S3 flow
(`files/` plus a new `document_type`), not a new upload path. Legacy gave
assessors no upload endpoint at all — they could read the artisan's video and type
integers, nothing more.

### Slot generation

Port `TimeSlotService::generateTimeSlots()` for staffing a centre: `dates[]`,
`start_time`, `end_time`, `slot_duration`, `break_duration`, `slots_before_break`.
Fix the two flaws in the original — insert in a batch rather than one `create()`
per slot, and guard against duplicates so re-running a request does not duplicate
an entire day.

### Reminders

`jobs.NewScheduler().Start()` is never called from `main.go`, so this needs wiring
before any reminder works. Three correctness requirements, all from legacy
failures (defects 16 and 17):

- Query a **window** (`$gte`/`$lt`), never an exact timestamp equality. The legacy
  hourly reminder matched `HH:MM:SS` against cron fire time and therefore almost
  never fired.
- **Record that a reminder was sent** and key on it, so a re-run or an overlapping
  tick does not re-notify.
- **Include the join link.** The one legacy notification that did fire listed the
  date and time but omitted the meeting ID.

Deliver over the existing notifications outbox so SMS and email both work; legacy
was push-only via OneSignal and silently skipped every artisan without a
`device_key`.

## The competency framework

**This supersedes the percentage pass marks sketched earlier in this document.**
The assessor app implements a real, considered competency rubric
(`fundis-assessor/lib/common/components/forms/custom_score_dropdown.dart`), and
it should be the model of record rather than invented thresholds. It separates
*inputs* from *outcome*.

### Scores — 1 to 5, per modality

Assigned by an assessor for the oral and practical stages. Each level is defined
by supervision need and safety/quality standard, not by a mark:

| Score | Label | Meaning |
|---|---|---|
| 1 | Poor | Minimal understanding; work does not meet required safety or quality standards |
| 2 | Fair | Partial understanding and skill; requires occasional supervision |
| 3 | Satisfactory | Adequate understanding and skills; performs with little supervision |
| 4 | Good | Solid understanding and good skills; exceeds minimum safety and quality standards |
| 5 | Excellent | Mastery; works autonomously and can teach others |

### Grade — 1 to 3, the certification outcome

The artisan-facing result. Note it is explicitly tied to **RPL (Recognition of
Prior Learning)** readiness, which means this rubric is designed to interoperate
with the formal TVET framework — a significant constraint the earlier draft of
this spec missed entirely:

| Grade | Label | Meaning |
|---|---|---|
| 1 | Basic | Requires basic training. Small jobs with supervision, apprenticeship role. Not ready for outside jobs. **Not ready for RPL** |
| 2 | Intermediate | Requires intermediate training. Medium jobs; small jobs independently. Semi-autonomous, supervision for large jobs. **Might be ready for RPL in specific areas** |
| 3 | Advanced | Requires advanced or specialised training. Small and large jobs. Autonomous, no supervision. **Ready for RPL and independent outside jobs** |

### Consequences for this design

- **There is no pass/fail on the human stages.** An artisan is *graded*, not
  passed. `WrittenPassPercent`/`PracticalPassPercent` from the earlier draft are
  the wrong shape for oral and practical, and should be dropped for those stages.
  This also aligns with the original framing that these are assessments rather
  than exams.
- **`StatusFailed` is wrong for oral and practical.** Replace with a graded
  outcome plus a `recommendation`. Grade 1 is not a failure — it is a real result
  that routes the artisan to training. The pipeline's job is to *place* an
  artisan on the scale, and only the written stage has a genuine floor (an artisan
  who knows nothing of their trade).
- **Grade drives discoverability, and this is the answer to D5.** A grade band is
  a far better input to the verified/discoverable decision than a boolean, and it
  gives employers something meaningful. Proposal: grade is set only by an
  assessor, is the terminal effect the pipeline has always lacked, and the
  discoverability subscription controls *listing*, not *credibility*.
- **Scores and grade must be stored as the rubric's own integers** with their
  labels resolved server-side, so the artisan-facing app, the assessor app and any
  future certificate all render identical wording.

### The oral questionnaire

The assessor app already contains the oral interview template — 10 categories,
24 questions (`assessment_checklist_data.dart`): Clarity and Articulation,
Understanding and Knowledge, Relevance of Experience, Application of Skills,
Depth of Response, Practical Insight, Professionalism and Confidence, Adherence
to Standards, Problem-Solving and Critical Thinking, Communication Skills.

Two corrections to the earlier draft:

- It is **skill-agnostic**, not per-skill. The earlier draft assumed "a template
  questionnaire per skill"; the real one assesses transferable competence and
  applies to every trade. Seed it as one shared template, and allow per-skill
  templates later rather than assuming them now.
- It must be **captured, not just displayed**. Move it server-side into a seeded
  template and record the assessor's per-category responses on the
  `assessment_sessions` row. As it stands the assessor's structured judgement is
  discarded and only one number survives (defect 22) — which also starves the
  Bedrock assessor-report generation of its best input.

## Three outcomes, deliberately separate

The single most important structural point in this design. There are now **three
independent things** an artisan can have, and conflating any two of them is how the
legacy system and the current Go backend both went wrong:

| Outcome | Earned by | Means | Field |
|---|---|---|---|
| **Discoverable** | Profile 100% complete **+ admin verification** | Appears in employer browse and search | `is_verified` (admin-driven) |
| **Certified** | Passing every assessment stage | Has an assessed competency grade and a certificate | `certified_at`, `certification_grade` |
| **Featured** | Paying (1000 KES / 30 days) | Ranked above other artisans in browse and search | `featured_until` |

Discoverability is therefore **stage 1 of the pipeline**, not the end of it. An
artisan becomes findable as soon as an admin verifies their completed profile, and
becomes *credible* later by being certified. That ordering is deliberate: it means
an artisan gets value from the platform before being asked to sit four assessments,
rather than being invisible until they finish.

### The paid plan becomes "Featured Profile" (decided)

`worker_verified_month` ("Verified and Discoverable", 1000 KES / 30 days) sold
exactly the discoverability that admin verification now grants for free. It is
**repurposed to sell placement rather than presence**: same price, same duration,
but it ranks an artisan above others in browse and search instead of deciding
whether they appear at all.

This keeps the revenue line and keeps the paywall off the basic experience. It also
mirrors an existing employer-side plan, `employer_featured_job` (1000 KES,
`EntitlementFeaturedJob`), so the concept and the price point are already
established in the catalogue.

The practical exam fee (1000 KES) is unaffected — that pays for an in-person
sitting, which is a real cost.

#### Code changes required

1. **`subscriptions/model.go:293` must stop writing `is_verified`/`verified_until`.**
   This is the critical change. While the subscription path writes that flag,
   `is_verified` cannot mean "admin-verified" and the whole separation collapses.
   It should write `featured_until` instead.
2. **Rename the entitlement** `EntitlementVerifiedDiscoverable` →
   `EntitlementFeaturedProfile`, and the plan's `Name`/`Description`/`Features` to
   match. Add `featured_until *time.Time` to `users.User`.
3. **Rewrite `users.ListArtisans`** (`users/model.go:220-250`). It currently
   *filters* on `is_available: true OR verified_until > now` and *sorts* by
   `verified_until DESC, created_at DESC` — i.e. the paid flag is doing both jobs
   today. It becomes:
   - **filter** on `is_verified: true` (admin-verified — this is what makes an
     artisan discoverable at all)
   - **sort** by featured first (`featured_until > now`), then certified, then
     recency
   Also remove the existing post-fetch `IsVerified = IsVerified && VerifiedUntil.After(now)`
   recomputation, which only makes sense while the flag is subscription-driven.
4. **`ArtisanSummary`** gains featured and certified indicators so browse can badge
   and filter without a second query.

#### Two things to decide before implementing

- **Featuring an unverified profile sells nothing.** An artisan who pays before
  admin verification is paying to be ranked highly within a set they are not in.
  Either block the purchase until `is_verified`, or accept the payment and start
  the 30 days only on verification. Blocking is clearer; starting-on-verification
  is kinder if someone pays during the ≤48h review window. **Recommend the
  latter**, given reviews complete inside 48 hours anyway.
- **Existing subscribers.** Anyone currently active on `worker_verified_month` paid
  for "Verified and Discoverable". Two migration hazards:
  - `user_subscriptions.subscription_plan_id` stores the plan ID as a string and
    `FindPlan(code)` resolves it, so **changing the plan ID would orphan every
    existing record**. Keep the ID `worker_verified_month` and change only its
    presentation and entitlement, or introduce a new plan and leave the old one
    resolvable for history.
  - Their `is_verified` was set by payment, not by review. On cutover they would
    silently lose discoverability unless they are either grandfathered or fast-
    tracked through profile review. Needs an explicit backfill decision — this is
    the kind of thing that generates support tickets on deploy day.

#### Still to define

What "featured" looks like to an employer: top-of-results placement, a badge, a
dedicated carousel, or all three. Ranking priority with no visual distinction is
hard to perceive and therefore hard to sell twice.

## Certification

Reached when every stage is passed. Three effects:

### 1. A grade

The competency grade (1–3, see
[the competency framework](#the-competency-framework)) becomes the artisan's
certified level. Since the practical stage is last, **the practical assessor's
grade is the certification grade** — no separate computation or averaging.

Earlier sessions still record a grade, but as an *advisory* recommendation that
gives the practical assessor continuity. Only the final stage certifies.

### 2. A PDF certificate

Generated server-side on certification, stored in S3, and shareable.

- **Contents:** artisan name and Fundis ID, skill, grade with its full descriptor
  (including the RPL wording), issue date, an expiry if certification is
  time-limited, and a verification code.
- **Shareable means publicly reachable**, which means **forgeable unless verifiable**.
  A PDF on its own is a picture. Pair every certificate with a short
  `verification_code` and a public verification endpoint/page
  (`GET /certificates/:code`) that returns the authoritative record. The PDF should
  carry the code and a QR pointing at it, so an employer can check rather than
  trust.
- **Regeneration must be deterministic.** Re-issuing after a name correction
  should produce the same certificate with a new revision, not a second valid
  certificate. Keep `certificate_id` stable and version the file.
- **Storage:** reuse the existing S3 setup and serve via presigned URL for the
  artisan's own download; the public verification page renders from the record
  rather than exposing the bucket.
- **New dependency.** Go has no stdlib PDF generation, so this needs a library
  (`jung-kurt/gofpdf` is unmaintained; `go-pdf/fpdf` or an HTML-to-PDF service are
  the realistic options). Worth a deliberate choice — an HTML template rendered to
  PDF is far easier to design and localise than drawing primitives.
- Legacy relevance: `assessment_reports.sign_off` was added and then dropped, and
  no certificate was ever produced. This is genuinely new ground.

### 3. A certified profile

`certified_at` and `certification_grade` on the user, surfaced as a badge on the
public artisan profile and in `ArtisanSummary` so browse results can show it.
Distinct from `is_verified` per the table above.

Certification should also be **visible in search/browse as a filter** — its main
commercial value is letting an employer say "show me certified electricians", and
none of the assessment work pays off unless that filter exists.

## AI scoring and reports — AWS Bedrock (decided)

A small agent in `integrations/bedrock.go` using `aws-sdk-go-v2`
`bedrockruntime`. Region and model ID via `configs/env.go` accessors
(`BEDROCK_REGION`, `BEDROCK_MODEL_ID`), consistent with the existing
`AWSRegion()`/`AWSS3Bucket()` style. Three jobs:

1. **Score free-text answers.** Objective question types are scored
   deterministically in Go; only free-text goes to Bedrock. This is what makes
   "results available on completion" achievable (D4) without waiting on a human.
2. **Generate artisan-facing feedback** — what to improve, in plain language.
3. **Generate an assessor-facing report** — a summary the practical assessor reads
   before the session, including the written score so an implausibly high score
   gets probed live (D2).

Both audiences are required, not optional: the AI marks the written stage and
leaves feedback **for the artisan and for the assessor**. The assessor-facing
output is what carries written-stage insight forward into the practical session,
and it is also the main consumer of the captured oral checklist — which is why
capturing that checklist rather than displaying it matters.

Non-negotiables baked into the design:

- **Graceful degradation.** If Bedrock is unconfigured or errors, the attempt
  still completes: objective score is returned immediately, free-text answers
  fall to `awaiting` for human review. An AI outage must never strand an artisan
  mid-pipeline.
- **AI output is advisory on the human-gated stages.** For the written stage the
  AI score stands. For oral and practical, AI output is a *suggestion* the
  assessor confirms or overrides; the assessor's decision is authoritative and
  the override is recorded.
- **Never in the request path for question authoring.** AI-drafted questions land
  in `status: "draft"` and a human approves them into the live bank (D6).
- Prompts and model responses are persisted on the attempt for auditability, so a
  disputed score can be reconstructed.

## The existing assessor client

`fundisapp/fundis-assessor` is a Flutter app (Riverpod, go_router, dio/http, hive,
`videosdk ^1.1.8`) that **already implements the assessor experience** — login,
upcoming/past appointments, a calendar, an artisan detail view, the video call,
the checklist screen, and score/grade/recommendation submission. Single commit,
`main`, "implemented assessor features".

It is the only assessor UI that exists, so it defines the contract. It currently
talks to the **dead Laravel backend**:

```dart
static const String LIVE_URL = "https://fundis-prod.herokuapp.com/api/v1";
static const String DEV_URL  = "https://fundis-staging.herokuapp.com/api/v1";
static const bool isDebug = false;   // → production Heroku
```

### Endpoints it consumes

| Method | Path | Purpose |
|---|---|---|
| POST | `/login` | Auth |
| GET | `/assessor/appointments` | Upcoming (client-side filters `daysLeft < 0`) |
| GET | `/assessor/appointments/pasts` | History |
| POST | `/assessor/appointments/{id}/complete` | Close the session |
| POST | `/assessor/assessments/report` | `artisan_id, skill_id, practical_score, practical_comment, oral_score, oral_comment, grade, comment, recommendation` |
| POST | `/notify/artisan` | **Not in this spec.** "An assessor is waiting for you to join the oral assessment interview" — a live nudge when the artisan has not joined the call. A real workflow need worth keeping. |

`assessmentResult(appointmentId)` throws `UnimplementedError()` — dead code.

### Breaking changes the Go backend forces

The app will not work against the new backend without a release. Worth deciding
now whether to adapt the API or the app:

- **`int` IDs vs UUID strings.** Every model is `final int id`. The Go backend uses
  UUID strings throughout. This is unavoidable and touches every model.
- **Nested vs flat token.** The app expects `{token: {access_token, token_type,
  expires_in}}`; the Go backend returns a flat token field.
- **MySQL truthiness.** `isAvailable: json['is_available'] == 1` — an int 0/1
  check that silently evaluates to `false` against a Mongo boolean, so **every
  slot would render as unavailable.**
- **`time_slot` shape.** The app expects a nested `time_slot` object with
  `start_time`/`end_time`/`date` as separate strings, since legacy stored only a
  date on the appointment. The new design puts a real timestamp on the slot.
- The app already carries defensive scar tissue from the legacy `user_id` →
  `artisan_id` rename: `json['user_id'] ?? json['artisan_id']` and
  `(json['user'] ?? json['artisan'])`. Pick one and keep it.

### Also worth fixing in the app

- `AppConfigKey.userType = "artisan"` — in the assessor app. Almost certainly a
  copy-paste from the artisan app.
- No offline queue (defect 24), despite assessors working at centres and sites.
  Given the practical stage is in-person by design, an offline scoring queue is
  closer to a requirement than a nice-to-have.

## Cross-cutting

- **Notifications**: new `Type*` consts in `notifications/model.go` per stage
  transition, delivered over the existing SMS/email outbox. Frontend must extend
  the `NotificationType` union *and* the icon map, or entries fall back to `Bell`.
- **Feature flag**: add `assessment: false` to `src/config/features.ts`; `App.tsx`,
  `Sidebar.tsx` and `Dashboard.tsx` already branch on `FEATURES`.
- **Frontend role gap**: no assessor nav, no assessor dashboard, no role route
  guard. `ProtectedRoute` only checks for a token. The `Role` union already
  includes `"assessor"`. All of this needs building.

## Decision log

Settled 2026-08-06:

- **D1 — Re-entry after failure.** Unlimited written attempts with a 48h cooldown;
  all attempts retained; feedback carried forward to the next attempt. See
  [Re-attempts](#re-attempts-decided).
- **D2 — Anti-cheating. Revised.** The written stage **is** a gate: 80% to unlock
  the practical. Per-attempt draw from a per-skill bank with randomised order,
  answer key never returned, integrity flags surfaced to the practical assessor,
  never auto-failing. Because the stage now gates, the bank floor rises to a
  recommended 60–100 approved questions per skill — see
  [Pass mark](#pass-mark--80-decided) for what changed and why.
- **Written pass mark — 80%.** Required to reach the practical stage.
- **Certification.** Passing every stage yields a grade (the practical assessor's),
  a shareable PDF certificate with a verification code, and `certified_at` on the
  profile. See [Certification](#certification).
- **Discoverability.** Earned by a 100% complete profile **plus admin
  verification** — stage 1, not the end of the pipeline. Separate from
  certification and from any subscription. See
  [Three outcomes](#three-outcomes-deliberately-separate).
- **The 1000 KES plan becomes "Featured Profile".** Same price and duration, but it
  sells ranking above other artisans rather than the discoverability that admin
  verification now grants free. Mirrors the existing `employer_featured_job` plan.
  See [the plan change](#the-paid-plan-becomes-featured-profile-decided) — note it
  requires `subscriptions/model.go:293` to stop writing `is_verified`, and a
  migration decision for existing subscribers.
- **Profile review SLA: under 48 hours.** With queue-age tracking, pre-breach
  alerting and notification on decision. See
  [Review SLA](#review-sla--under-48-hours-decided).
- **D4 — Free-text and instant results.** Resolved by Bedrock: objective types
  score deterministically, free-text scores via AI, so results are available on
  completion. Human review only when Bedrock is unavailable.
- **D6 — AI.** AWS Bedrock agent for free-text scoring, artisan feedback and
  assessor reports. Authoring stays human-approved. See
  [AI scoring and reports](#ai-scoring-and-reports--aws-bedrock-decided).
- **D7 — "Pick a question".** Confirmed: the artisan picks the *type of practical
  task* they want to be assessed on, then picks a verification centre near them.
- **Physical exam fee.** 1000 KES, charged for the practical stage specifically.
  Stages 1–3 free.

### Still open

- **D3 — Video vendor. Closed.** VideoSDK.live is the vendor on *both* sides: it
  worked server-side in Laravel and the Flutter assessor app ships
  `videosdk ^1.1.8` with a working call UI. Not a decision, a port. Two required
  changes, though: **rotate the leaked secret** (defect 21), and move room
  creation and token minting server-side — today the client does both with an
  embedded master secret.
- **D5 — `is_verified` ownership. Decided.** `is_verified` means **admin-verified
  complete profile**, and it drives discoverability. Certification is a separate
  outcome with its own fields. **This leaves one open revenue question:**
  `worker_verified_month` (1000 KES/30d) currently sells exactly the
  discoverability that is now free, so the plan must be retired or repurposed — see
  [the conflict](#-this-conflicts-with-the-existing-paid-plan). Retained below is
  why this mattered:
  in Laravel, passing every stage set **nothing** — no verified flag, no
  discoverability change, an artisan who completed the whole pipeline was
  indistinguishable from one who never started. The Go rewrite inverted that:
  `worker_verified_month` grants `is_verified` for 1000 KES/30d with no assessment
  at all (`subscriptions/model.go:293`). So the feature has never once had a
  terminal effect, and paying is currently the only thing that produces one.
  **This is the decision most likely to make the whole pipeline pointless again.**
- **First-month cost.** With the exam at 1000 KES and discoverability at 1000
  KES/30d, a new artisan faces **2000 KES** to become both assessed and
  discoverable — on a flow that was entirely free in the legacy product. Worth an
  explicit decision (bundle, stagger, or waive one) rather than discovering it in
  funnel data.
- **Fee on the remote practical.** If the video-upload fallback carries the same
  1000 KES as an in-person sitting, it is hard to justify on cost grounds. If it
  is cheaper, it becomes the default route and centre bookings dry up. Needs a
  call alongside D5.

## Testing requirements

Non-negotiable, because the legacy system had **zero tests covering Gen 2** and
that is traceable to nearly every defect above shipping unnoticed:

- **A route-mount smoke test per role group.** One assertion that the artisan,
  assessor and admin groups are mounted and reachable. This feature has now been
  lost twice to unmounted routes — once in Laravel by a merge conflict, once in Go
  by a package deletion. A test is what makes the third time impossible.
- **State transition tests, not CRUD tests.** The only legacy tests covered the
  Gen 1 paper forms; nothing tested scoring, submission, double-submit, slot
  contention, rating or finalisation. Table-driven tests over `ApplyDecision`,
  cooldown enforcement, and objective scoring cover the parts that actually break.
- **A concurrency test on slot booking**, since the legacy availability check was
  a read-then-write race.
- **An idempotency test on the payment webhook**, since a replayed Cellulant
  callback must not double-confirm an exam.
