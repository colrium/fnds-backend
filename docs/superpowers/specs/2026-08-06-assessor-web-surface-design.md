# Assessor Web Surface — Screen Flow and API Contract

Status: **draft for review**
Date: 2026-08-06
Companion to [`2026-08-06-artisan-assessment-pipeline-design.md`](./2026-08-06-artisan-assessment-pipeline-design.md)

Assessors work on the **web app only** — no mobile client. This specs the screens
in `fundisapp/frontend` and the exact endpoints each one needs, so the Go backend
and the React frontend can be built against one contract.

The reference implementation is the Flutter app `fundisapp/fundis-assessor`, which
is the only assessor UI that has ever existed. Where this diverges from it, the
reason is given.

## Scope

**In:** assessor authentication and role gating, appointment list and detail, the
oral session (video + checklist capture), the practical session, report
submission, and availability management.

**Out:** artisan-facing pipeline screens, the admin profile-review queue, and
question-bank authoring. Those are separate surfaces on the same backend.

## Three structural gaps to close first

None are large, but every screen below depends on them.

| Gap | Current state | Needed |
|---|---|---|
| Role gating | `src/components/ProtectedRoute.tsx` is 22 lines and checks only `useAuthStore(s => s.token)`. **No role guard exists anywhere in the app.** | A role check, either a `requireRole` prop on `ProtectedRoute` or a sibling `RoleRoute`. Assessor routes must reject artisans and customers, not just anonymous users. |
| Navigation | `src/components/Layout/Sidebar.tsx` has hardcoded `customerNav`/`artisanNav`, selected by `user?.user_type === "artisan"`. | A third `assessorNav`. Note the existing `NavItem` already supports `requireVerified`, `feature` and `children`. |
| Dashboard entry | `src/pages/Dashboard.tsx` ends `userType === "artisan" ? <WorkerDashboard /> : <EmployerDashboard />` — a binary ternary, so an assessor currently lands on the employer dashboard. | A three-way branch. |

Already in place: `src/types/auth.ts` has `"assessor"` in the `Role` union, and
`middleware.IsAssessor` exists and is tested in the Go backend (it is simply not
mounted).

Everything ships behind `FEATURES.assessment` in `src/config/features.ts`, which
`App.tsx`, `Sidebar.tsx` and `Dashboard.tsx` already know how to branch on.

## Screen flow

```
 sign in (existing)
      │
      ▼
 A1  Assessor dashboard ──────────────► A6  Availability
      │  today's sessions, counts            publish / generate / withdraw slots
      ▼
 A2  Appointments  [Upcoming | Past]
      │
      ▼
 A3  Appointment detail  (pre-session briefing)
      │   artisan · skill · written score + integrity flags · task brief · evidence
      │
      ├──► oral ──────► A4  Live session   (video call + checklist, side by side)
      │                      │
      └──► practical ──► A5  Practical session  (task brief + evidence review)
                             │
                             ▼
                        A7  Report  (checklist review · scores · grade · recommendation)
                             │
                             ▼
                        session filed, appointment closed  ──► back to A2
```

---

## A1 — Assessor dashboard

`/assessor` · route guard: `assessor`

Landing screen. Answers "what am I doing today".

**Shows:** today's sessions in time order with a join affordance; counts of
upcoming sessions, sessions awaiting a report, and sessions completed this month;
a shortcut to publish availability when the assessor has no future slots (the most
common reason an assessor gets no bookings).

**Data:** `GET /assessor/dashboard`

Reuse `DashboardCard` from `src/components/Dashboard/` and `EmptyState` for the
no-sessions case.

## A2 — Appointments

`/assessor/appointments` · tabs: Upcoming | Past

**Shows:** appointment cards — artisan name, phone, skill, scheduled time,
stage badge (oral/practical), status badge, and whether a report is outstanding.

`StatusBadge` already styles `scheduled`, `completed` and `cancelled`.

**Data:** `GET /assessor/appointments?scope=upcoming|past&page=&limit=`

Two deliberate changes from the Flutter app:

- **Server-side scope filtering.** The Flutter app fetched all appointments and
  filtered in the client with `if (appointment.timeSlot.daysLeft < 0) continue`,
  which means an assessor with a long history downloads all of it to render one
  screen. `scope` and pagination belong on the server.
- **Paginated.** The legacy endpoints returned unbounded lists.

## A3 — Appointment detail (pre-session briefing)

`/assessor/appointments/:id`

The screen that makes an assessment defensible. Everything the assessor needs
**before** they start.

**Shows:**

- Artisan identity — name, phone, Fundis ID, avatar. Phone matters: it is the
  fallback when someone does not join.
- Skill and stage.
- **Written stage summary** — score, max, percent, attempt number, and the
  **integrity flags**. This is the anti-cheat mechanism doing its real work: the
  written test is not the gate, but an implausibly high score shown here gets
  probed live. Flags are presented as neutral observations, never as an
  accusation, and pause counts are explicitly labelled as expected.
- **AI pre-session briefing** (Bedrock `assessor_report`) when available.
- **Practical task brief** — `name`, `instruction`, `what_to_submit`. These
  existed in the legacy schema and were never shown to an assessor; showing them
  is what lets an assessor judge against a stated standard.
- **Evidence** — presigned links for any uploaded video or photos.
- Actions: *Join call* (oral), *Start session* (practical), *Notify artisan*.

**Data:** `GET /assessor/appointments/:id`

## A4 — Live session (oral)

`/assessor/appointments/:id/session`

**The checklist sits beside the video, not on a separate screen.** This is the
single most important UX decision here, and it is a direct fix for the legacy
failure: the Flutter app put the 24-question checklist on its own read-only
screen, so nothing was ever captured and the assessor's structured judgement was
lost (defect 22). An assessor cannot hold a video call on one screen and a
checklist on another.

**Layout:** video panel (participants, mute, camera, end) on one side; the 10
checklist categories as a scrollable scored column on the other. On narrow
viewports the checklist collapses to a sheet over the call rather than a separate
route.

**Per category:** the prompts as guidance text, a 1–5 score, and an optional note.

**Draft persistence:** the checklist autosaves to `localStorage` keyed by
appointment, mirroring the profile wizard's `professional-profile-step:${userId}`
pattern. Assessors at centres have unreliable connectivity, and losing 24
judgements to a dropped connection is unacceptable. This is the web equivalent of
the offline queue the Flutter app never had (defect 24).

**Video:** `POST /assessor/appointments/:id/join` returns a **short-TTL,
room-scoped** token minted server-side. The client never holds an API secret —
the Flutter app embedded the VideoSDK master secret and minted its own tokens
(defect 21).

## A5 — Practical session

`/assessor/appointments/:id/session` (same route, practical variant)

No video. Shows the task brief prominently, the evidence viewer, and the same
checklist column. For a remote video practical the assessor reviews the uploaded
file here; for an in-person sitting at a centre they score from observation.

**Evidence upload by the assessor** uses the existing presigned S3 flow in
`src/api/documents.ts` (`presign` → `PUT` → `complete`) with a new document type.
Legacy gave assessors no upload path at all.

## A7 — Report

`/assessor/appointments/:id/report`

**Shows:**

- The captured checklist, read-only, as a summary of what was just recorded.
- **Oral score** and/or **practical score** — 1–5, each option rendered with its
  full descriptor, not just the number. The descriptors are what make scores
  comparable between assessors, so they must be visible at the point of choosing.
- **Grade** — 1–3, likewise with full descriptors including the RPL wording.
  **On an oral session this is advisory**, recorded to give the practical assessor
  continuity. **On a practical session it certifies**, because the practical is the
  final stage — that grade becomes the artisan's certified level and goes on their
  certificate. The UI must say which, or assessors will not know the weight of what
  they are submitting.
- Per-modality comments, an overall comment, and a **recommendation**.

Rubric wording is fetched from the server (`GET /assessor/rubric`) rather than
hardcoded in the client, so the artisan app, the assessor surface and any future
certificate render identical text and the wording can be versioned.

**Submit:** `POST /assessor/appointments/:id/report`

Four contract decisions, each fixing a specific legacy defect:

1. **No `artisan_id` or `skill_id` in the payload.** The server derives both from
   the appointment. Legacy took them from the request body, which is the root of
   how an artisan came to write their own grade (defect 1).
2. **One atomic call files the session and closes the appointment.** Legacy split
   this across `POST /assessments/rate`, `POST /assessments/report` and
   `POST /appointments/{id}/complete`, which is exactly how half-finished state
   accumulated. The frontend should not have to orchestrate three writes.
3. **Append-only.** The submission creates an `assessment_sessions` row. It never
   updates a shared summary row, so a second sitting or a second assessor adds
   history instead of overwriting it (defect 11), and there is no first-write-wins
   behaviour silently discarding the assessor's grade (defect 12).
4. **Idempotent.** Re-submitting the same report must not create a second session
   or double-close the appointment.

## A6 — Availability

`/assessor/availability`

This is now the assessor's most consequential screen: since artisans book a time
rather than a person, **an assessor with no published availability receives no
work at all.** A1 should nag when future availability is empty.

**Shows:** published future slots with booked/free state; a form to publish one
slot; a bulk generator for staffing a day; and the assessor's own granted skills,
read-only, so they can see which trades they will be matched for.

Bulk generation is ported from the legacy `TimeSlotService`: `dates[]`,
`start_time`, `end_time`, `slot_duration`, `break_duration`,
`slots_before_break`. Two fixes — insert in a batch, and guard duplicates so
re-running does not duplicate a whole day.

Slots carry `assessor_id`, `stage`, `centre_id` (practical) and `capacity` — but
**not** `skill_id`, which comes from the assessor's grants. Legacy `time_slots`
had no owner at all and was one global calendar shared across every assessor and
trade (defect 8).

Use native `<input type="date">` / `type="time">` — the app has no date-picker
library and does not need one here.

---

## API contract

All routes mount on a new assessor group in `main.go`:

```go
assessorGroup := api.Group("")
assessorGroup.Use(middleware.JWTAuth, middleware.IsAssessor)
```

Conventions: `/api/v1` prefix; `responses.SendOK`/`SendCreated` envelope
`{success, data, message}`; UUID string IDs; snake_case JSON; RFC 3339 timestamps.

**Every query filters by `assessor_id` in the Mongo filter**, not after the read.
Legacy left `completeAppointment` and the rating endpoint entirely unscoped, so
any assessor could score any appointment by ID (defect 10).

### 1. Dashboard

```
GET /assessor/dashboard
→ { counts: { upcoming, awaiting_report, completed_this_month },
    today: AssessorAppointment[],
    has_future_slots: bool }
```

### 2. List appointments

```
GET /assessor/appointments?scope=upcoming|past&page=1&limit=20
→ PaginatedData<AssessorAppointment>
```

`scope=upcoming` returns `scheduled` appointments from now forward, ascending;
`past` returns everything earlier or terminal, descending. Default `upcoming`.

### 3. Appointment detail

```
GET /assessor/appointments/:id
→ AssessorAppointment {
    id, journey_id, stage: "oral"|"practical", status, scheduled_for,
    meeting_id?,                       // present for oral, room created at booking
    artisan: { id, first_name, last_name, phone, fundis_id?, avatar? },
    skill_id, skill_name,
    centre?: { id, name, address? },           // practical
    practical_task?: { id, name, instruction, what_to_submit },
    written?: { attempt_number, score, max_score, percent,
                integrity_flags: string[], feedback?, assessor_report? },
    evidence?: [{ id, file_name, content_type, size, download_url, created_at }],
    report_submitted: bool,
    completed_at?
  }
```

404 when the appointment is not this assessor's — not 403, so the endpoint does
not confirm the existence of other assessors' records.

### 4. Join the call

```
POST /assessor/appointments/:id/join
→ { token, meeting_id, expires_at }
```

Token minted server-side per join: room-scoped, short TTL, participant-scoped.
Returns 409 if the appointment is not `scheduled`.

### 5. Notify the artisan

```
POST /assessor/appointments/:id/notify-artisan
→ { notified: true }
```

Sends the standard nudge over the existing notifications outbox (SMS + in-app),
message owned server-side. Should be rate-limited per appointment so an impatient
assessor cannot spam an artisan.

### 6. Rubric and checklist template

```
GET /assessor/rubric
→ { scores: [{ value: 1..5, label, description }],
    grades: [{ value: 1..3, label, description }],
    checklist: [{ id, title, prompts: string[] }] }
```

Server-owned so wording stays identical across surfaces and can be versioned.
Seed from the exact production wording in the Flutter app — the score levels are
defined by supervision need and safety standards, and the grade levels reference
RPL eligibility, so they must not be paraphrased.

Cacheable; consider `middleware.RedisCache` as the artisan public pages do.

### 7. Submit report

```
POST /assessor/appointments/:id/report
{
  checklist: [{ category_id, score: 1..5, note? }],
  score: 1..5,        // the appointment's own stage — one session, one type
  score_comment?,
  grade: 1..3,
  comment?,
  recommendation?
}
→ { session_id, appointment: AssessorAppointment, journey_stage, grade }
```

One score, for the appointment's stage — the server knows which modality it is, so
the client does not name it. Validation: `score` and `grade` required; every
`category_id` must exist in the current template; nothing outside range.

Restricted to the appointment's assigned `assessor_id` (403 otherwise), even
though reads are open. Files an `assessment_sessions` row, advances the journey,
closes the appointment, releases results to the artisan, and notifies them —
atomically. Idempotent: 409 on re-submit, and a retried request that already
succeeded must not file a second session.

**Releasing the oral result is what unlocks the written stage**, so this endpoint
is the gate for the next stage rather than a passive record.

### 8. Availability

```
GET    /assessor/slots?from=&to=        → AssessorSlot[]   (own)
POST   /assessor/slots                  { stage, starts_at, ends_at, centre_id?, capacity? }
POST   /assessor/slots/generate         { stage, dates[], start_time, end_time,
                                          slot_duration, break_duration, slots_before_break,
                                          centre_id?, capacity? }
DELETE /assessor/slots/:id              → 204
```

Note there is **no `skill_id` on a slot** — eligibility comes from the assessor's
`assessor_skills` grants at match time.

`DELETE` returns 409 when the slot is booked. Withdrawing availability must not
silently cancel an artisan's appointment; use admin reassignment instead.

### 9. Assessor capabilities

```
GET  /assessor/skills                       → own grants (read-only to the assessor)
GET  /admin/assessors/:id/skills            → grants
POST /admin/assessors/:id/skills            { skill_ids: [] }
DELETE /admin/assessors/:id/skills/:skillId → 204
```

Grant management is **admin-only by design**. An assessor who could grant
themselves a skill could authorise themselves for a trade they cannot judge, which
is exactly what legacy allowed.

### 10. Evidence upload

Reuse the existing presigned flow rather than adding a path:

```
POST /files/presign   →  PUT to S3  →  POST /files/complete
```

with a new `document_type` for assessment evidence, then attach via the report
submission.

---

## Frontend file plan

```
src/types/assessment.ts                      types mirroring the above
src/api/assessment.ts                        plain async fns on the shared axios instance
src/components/ProtectedRoute.tsx            + role gating
src/components/Layout/Sidebar.tsx            + assessorNav
src/pages/Dashboard.tsx                      + assessor branch
src/config/features.ts                       + assessment flag
src/pages/assessor/AssessorDashboardPage.tsx
src/pages/assessor/AppointmentsPage.tsx
src/pages/assessor/AppointmentDetailPage.tsx
src/pages/assessor/SessionPage.tsx           video + checklist, oral and practical variants
src/pages/assessor/ReportPage.tsx
src/pages/assessor/AvailabilityPage.tsx
src/components/assessor/ChecklistPanel.tsx
src/components/assessor/RubricSelect.tsx     1–5 / 1–3 with descriptors
src/components/assessor/WrittenSummaryCard.tsx
src/components/assessor/AppointmentCard.tsx
```

Rubric wording is **not** duplicated in the frontend — it comes from
`GET /assessor/rubric`.

## Notes for the build

- **Video SDK on the web.** The Flutter app used `videosdk ^1.1.8`; the web needs
  `@videosdk.live/js-sdk` or the React SDK. This is the one genuinely new
  dependency. It should read only a **server-issued token**, never an API key —
  no VideoSDK credential belongs in `import.meta.env`.
- **Notifications.** Extend `NotificationType` in `src/api/notifications.ts` *and*
  the icon map in `NotificationCenter.tsx`, or new assessment events fall back to
  a generic bell.
- **Testing.** The frontend has no test runner at all. At minimum the backend
  needs a route-mount smoke test for the assessor group — this feature has already
  been lost twice to unmounted routes.

## Decisions

Settled 2026-08-06:

1. **One session scores one assessment type.** The report endpoint takes a single
   `score` for the appointment's own stage, not both modalities. Simpler endpoint,
   and it keeps one session mapped to one stage transition.
2. **Assessors are auto-assigned by skill.** Assessors publish availability;
   artisans pick a *time*, not a person; the server assigns a qualified assessor.
   See [Auto-assignment](#auto-assignment) — this is the largest change and it
   inverts the booking flow.
3. **Any assessor can read everything about an assessment.** No per-assessor
   scoping on reads. Writes remain restricted to the assigned assessor. See
   [Authorization](#authorization).
4. **Centres have internet, possibly slow or limited.** No offline mode. Design
   for a thin, resilient connection instead — see
   [Designing for limited connectivity](#designing-for-limited-connectivity).

Also confirmed: the written stage opens once **oral results are out**, and AI
marking produces feedback for *both* the artisan and the assessor.

## Auto-assignment

Artisans no longer choose an assessor. This removes a real problem — under the
previous design an artisan would be picking a name they know nothing about — but
it adds a matching engine and two new requirements.

### Assessor capabilities

Which skills an assessor may assess must be **explicitly granted, not
self-asserted.** The legacy system derived it from `assessor->profile->skills`,
meaning an assessor could authorise themselves for any trade by editing their own
profile (defect 9).

New `assessor_skills` collection: `assessor_id`, `skill_id`, `granted_by`,
`granted_at`. Admin-managed. An assessor with no grants receives no bookings.

### Availability carries no skill

An `assessor_slot` is now just "this assessor is free from X to Y for this stage"
(plus `centre_id` for practical). Skill eligibility comes from `assessor_skills`
at match time. This is deliberately simpler than putting `skill_id` on the slot:
an assessor free at 10:00 who can assess plumbing and electrical publishes one
slot, not two.

### Booking, from the artisan's side

```
GET /assessment/availability?stage=oral&from=&to=
→ [{ starts_at, ends_at, available_count }]
```

Returns bookable **time windows** for the artisan's main skill, with a count of
qualified assessors free in each. It does not leak assessor identities.

```
POST /assessment/appointments   { stage, starts_at }
```

Server-side, atomically:

1. Resolve the artisan's main skill from their profile.
2. Find candidate slots: matching `stage` and `starts_at`, not full, belonging to
   an assessor holding an `assessor_skills` grant for that skill.
3. Pick one — **fewest upcoming appointments first**, random tie-break, so load
   spreads instead of landing on whoever sorts first.
4. Claim it with a conditional update guarded on capacity. **If the claim loses a
   race, retry the next candidate** rather than failing; only return
   "no availability" once every candidate is exhausted.
5. Create the appointment with `assessor_id` bound, and the video room for oral.

Concurrency matters here in a way it did not before: if three qualified assessors
are free at 10:00, three artisans can book that window and each gets a different
assessor. The fourth is told there is no availability.

### Reassignment

Because the artisan did not choose the assessor, the system owns fixing it when
an assessor withdraws or no-shows. Needed:

```
POST /admin/appointments/:id/reassign   { assessor_id? }
```

With no body, it re-runs the matcher for the same time window; with an
`assessor_id` it assigns explicitly. Notifies both parties. Withdrawing
availability that is already booked still returns 409 — reassignment is the
supported path, not deletion.

## Authorization

Two different rules, deliberately:

- **Reads are open to any assessor.** An assessor can view any appointment,
  journey, written attempt, session history and evidence. This supports covering
  for an absent colleague, second opinions, and continuity across stages.
- **Writes are restricted to the assigned assessor.** Only the appointment's
  `assessor_id` may submit its report or close it. Legacy left mutations entirely
  unscoped, so any assessor could score any appointment (defect 10), which is what
  made the assessor's verdict untraceable.

Two consequences worth accepting explicitly:

- Open reads mean **every assessor can see every artisan's personal details and
  assessment history.** That is a privacy surface. Recommend audit-logging assessor
  reads of records they are not assigned to — cheap now, and the only way to answer
  "who looked at this" later.
- `GET /assessor/appointments` still defaults to **the caller's own** appointments,
  since a list of everyone's sessions is not a useful working screen. Add
  `?scope=all` for the cross-assessor view rather than making it the default.

## Designing for limited connectivity

Centres have internet, so no offline queue. But "slow or limited" is a design
constraint, and possibly a metered one:

- **Draft the checklist to `localStorage`** as specced, keyed by appointment. This
  covers a dropped request without any sync machinery.
- **Never auto-play or auto-download evidence.** Practical evidence is video —
  render a poster and fetch on explicit play. Auto-loading a 100 MB file on a
  metered connection at a centre is the single easiest way to make this feature
  hated.
- **Small payloads.** Paginate everything, and keep the appointment list lean;
  the detail screen is where the heavy fields belong.
- **Retry the report submission** with clear pending state, and make it idempotent
  server-side so a retry after a timeout cannot file two sessions.
- Show a submission-failed state that keeps the draft, rather than a toast that
  loses the assessor's work.
