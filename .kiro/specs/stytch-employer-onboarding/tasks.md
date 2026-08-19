# Implementation Plan: Stytch Employer Onboarding And Workspaces

## Overview

The implementation language is **Go 1.26** for the backend and **TypeScript (React 18 + Vite)** for the
frontend, as written in the design document. No language selection is required.

The order below is the design's own dependency order, and four of its edges are hard prerequisites that
cannot be reordered:

1. **The `uniq_phone` index migration lands before any Stytch signup path exists.** Design
   [The `uniq_phone` problem](design.md) shows the current non-partial unique index rejects the *second*
   phone-less insert with `E11000`. Every phone-less employer created before task 2.1 ships would fail.
   The migration is a 4-step ordered operation and its steps may not be separated.
2. **`jobs.NewScheduler()` must be called from `main.go` before the expiry, reaper, deletion-effector, and
   Stytch-delete-retry jobs are written.** The design's grounding table records that `jobs/scheduler.go`
   has no live caller today, while Req 12.8, 3.7, 18.8, and 13.4 all depend on a running scheduler.
3. **`configs.TransactionsSupported` is written before `TransferOwnership`.** Ownership transfer is the
   one operation in this feature that requires a MongoDB multi-document transaction (design decision 4),
   and the probe is what decides between running it and returning `503 transactions_unavailable`.
4. **`stytch.Fake` is built before any property test.** 22 of the 30 properties drive Fundis code against
   the in-process fake; `FailAt` is the generator surface for P8 and P30, and `MintSessionJWT` with a real
   RSA key is what keeps P19, P21, and the credential-chain properties non-vacuous.

`pgregory.net/rapid` is added to `go.mod` in task 1.4, before the first property test in task 4.3.

Task numbering groups by design component. Every task names the requirement criteria it satisfies and the
design section it implements. Sub-tasks marked `*` are optional: they are test tasks, or they implement one
of the design's deferred items.

## Tasks

- [ ] 1. Configuration, dependencies, and startup validation
  - [-] 1.1 Add the seven Stytch configuration accessors and declare them in `.env.example`
    - Add `StytchProjectID`, `StytchSecret`, `StytchPublicToken`, `StytchEnvironment`,
      `StytchWebhookSecret`, `StytchEmployerAuthEnabled`, `LegacyEmployerAuthEnabled` to `configs/env.go`
      as one-line `os.Getenv` accessors, following the existing convention
    - Declare all seven in `.env.example` with the comment block from design *Configuration*
    - Implements design *Configuration* / `.env.example` addition
    - _Requirements: 20.10, 21.1, 21.12_

  - [~] 1.2 Implement startup validation for the five required Stytch variables
    - In `main.go`, when `STYTCH_EMPLOYER_AUTH_ENABLED` parses as true, require each of the five Stytch
      values to be non-empty after `strings.TrimSpace`; the first empty one emits one error-severity log
      naming that variable and calls `os.Exit(1)` with zero Stytch calls attempted
    - When the flag is false the five are not required, so an unadopted deployment starts unchanged
    - Implements design *Configuration* startup validation
    - _Requirements: 20.11_

  - [-] 1.3 Implement the `configs.TransactionsSupported` startup probe
    - Add `TransactionsSupported(ctx) (bool, error)` to `configs/db.go`, determined once from the `hello`
      command: a replica set reports `setName`, `mongos` reports `msg == "isdbgrid"`
    - Call it from `main.go`, log the result, and store it for the health payload; fail startup with one
      error naming the topology when employer Stytch auth is enabled and the probe reports no support
    - Production and staging are replica sets (ratified), so the probe reports support in both and the
      fail-fast is a deployment guard; do not add a standalone-degradation path
    - Prerequisite for task 10.4 (`TransferOwnership`)
    - Implements design *Transaction availability* / decision 4
    - _Requirements: 14.3, 14.4_

  - [~] 1.4 Add `pgregory.net/rapid` and `github.com/stytchauth/stytch-go/v18` to `go.mod`
    - Both dependencies are accepted; neither is provisional and no fallback is implemented
    - `rapid` is accepted as a test-only dependency with no transitive dependencies, notwithstanding Go
      having no test-only dependency scope; nothing in production imports it
    - `stytch-go/v18` is accepted as a production dependency, confined to `stytch/client_http.go` behind
      the `Client` interface; do not hand-roll the eleven endpoints or the discovery flows
    - Run `go mod tidy` and confirm `rapid` stays out of the production build graph
    - Prerequisite for every property-test task
    - Implements design *PBT library decision* and *Stytch client*
    - _Requirements: testing strategy; both dependencies ratified_

  - [ ]* 1.5 Write example-based tests for configuration and startup
    - Table test over the five Stytch variables asserting the named startup failure and zero Stytch calls
    - Parse `.env.example` and assert all seven new variables are declared
    - Assert startup fails when employer Stytch auth is on and the transaction probe reports no support
    - _Requirements: 20.10, 20.11; design decision 4_

- [ ] 2. Index migrations on existing collections
  - [-] 2.1 Rebuild `uniq_phone` as a partial unique index in `users.EnsureIndexes`, under a distributed lock
    - Acquire an exclusive, TTL-bounded distributed lock (a `migration_locks` collection, findAndModify
      upsert) before step 1, and release it after step 4 (or on early return). Without this, every
      instance in a multi-replica deployment races through drop+create concurrently, and the window
      between one instance's `DropOne` and `CreateOne` has no unique constraint on phone at all
    - Implement the four ordered steps exactly as the design states them, in one function, in this order:
      (1) aggregate `users` grouped by `phone` where `phone` is a non-empty string and assert no group has
      a count above 1 — on any duplicate, log one error naming the duplicate count and return an error so
      startup fails; (2) `updateMany({phone: {$regex: "^\\s*$"}}, {$unset: {phone: ""}})` — matching
      whitespace-only values too, not just the exact empty string, or two whitespace-only phones can both
      survive the pre-check and then fail index creation partway through step 3; (3) `DropOne("uniq_phone")`
      then `CreateOne` with `SetUnique(true).SetPartialFilterExpression(bson.M{"phone": bson.M{"$type":
      "string", "$gt": ""}})`; (4) read `Indexes().List()` first and run step 3 only when the existing
      options differ from the target — compared via a canonical JSON encoding rather than a raw BSON byte
      comparison, since MongoDB can store a semantically identical filter with different field ordering —
      so a restart is a no-op
    - Apply the same needs-rebuild-then-create pattern (not an unconditional `CreateMany`) to `uniq_email`
      and `uniq_slug_public`: blasting all three through one `CreateMany` fails the entire startup with
      `IndexOptionsConflict` the moment any one of them already exists with different options
    - Change `users/model.go` `User.Phone` to `bson:"phone,omitempty"`, JSON tag unchanged
    - This task MUST land before task 6.3 creates a phone-less employer `users` document
    - Implements design *The `uniq_phone` problem and its mitigation*
    - _Requirements: 1.12, 1.14_

  - [~] 2.2 Add `users_email_ci` and `GetUsersByNormalizedEmail`
    - Add the non-unique `{email_normalized: 1}` index and backfill `email_normalized` for existing rows
    - Add `GetUsersByNormalizedEmail(ctx, email) ([]User, error)` returning *all* case-variant matches,
      which the existing single-document `GetUserByEmail` cannot do
    - _Requirements: 4.1, 4.8, 21.7_

  - [~] 2.3 Add the `workspace_id` field and its partial index to the five existing collections
    - Add `workspace_id string` with `bson:",omitempty"` to `companies`, `job_vacancies`,
      `job_applications`, `user_subscriptions`, and `quotation_attachments`
    - Create `companies_workspace`, `companies_owner` (new — `companies` has no index today),
      `job_vacancies_workspace`, `job_applications_workspace`, and `user_subscriptions_workspace` per the
      index catalogue, each with `PFE {workspace_id: {$exists: true}}` where the catalogue states one
    - `omitempty` is required so "no workspace" means *absent*, which is what makes the Req 9.7 filter a
      clean `{"workspace_id": {"$exists": false}}`
    - Implements design *Fields added to existing collections* and *Complete index catalogue*
    - _Requirements: 6.1, 6.2, 6.3, 9.1, 9.7, 24.1, 24.10_

  - [ ]* 2.4 Write integration tests for the index migration
    - Seed a collection holding two documents with the same non-empty phone; assert `EnsureIndexes`
      returns an error and startup fails
    - Seed one `phone: ""` document; assert it is `$unset` and the rebuilt index is partial
    - Run `EnsureIndexes` twice; assert the second run performs zero index drops
    - _Requirements: 1.12, 1.14_

- [ ] 3. Stytch_Adapter and the in-process fake
  - [~] 3.1 Define the `stytch` package types, `Config`, and the `Client` interface
    - Create `stytch/` with `Config`, `Client` (the 16 methods of the design), `Session`, `Organization`,
      `Member`, `CreateOrgInput` (carrying `EmailJITProvisioning: "RESTRICTED"`, `EmailInvites:
      "ALL_ALLOWED"`, and `IdempotencyKey`), and the `Clock` seam
    - The package reads zero environment variables; `main.go` passes a `Config` value in, following the
      `logging.RawConfig` precedent
    - Implements design *Stytch_Adapter*
    - _Requirements: 2.13, 3.12, 20.10_

  - [~] 3.2 Implement error classification and the `Adapter.Do` retry policy
    - Implement `Kind`, `Error` with `Unwrap`, and `IsUnavailable`, mapping every failure into the six
      closed kinds
    - `Do(ctx, name, mutating, fn)` applies a 5-second timeout, attempt 1 immediate then retries at 200 ms
      and 600 ms with ±20% jitter from the injected clock, honours `Retry-After` on 429 clamped to
      `Timeout` with at most 3 total attempts, and never retries `mutating == true` with an empty
      `IdempotencyKey`
    - On exhaustion emit exactly one error record naming the op, the final status, and the workspace
      identifier when issued under a resolved `SessionContext`, excluding every secret
    - `Error.Error()` never includes `Config.Secret` or a session token
    - Implements design *Error classification*, *Retry schedule*, *Stytch outage degradation ladder*
    - _Requirements: 20.1, 20.2, 20.3, 20.4, 20.9, 20.12_

  - [~] 3.3 Implement the JWKS cache, `VerifySessionJWT`, and `Health`
    - Cache the JWKS for at least 1 hour and continue serving the last successfully fetched key set while
      a refresh fails
    - `VerifySessionJWT` verifies RS256 locally against the cached JWKS with zero Stytch calls — the path
      that survives an outage
    - `Health()` reads an `atomic.Pointer[HealthSnapshot]` updated after every adapter call
    - Implements design *Stytch_Adapter*, *Health indicator*
    - _Requirements: 16.1, 16.2, 20.5, 20.6, 20.7, 20.13_

  - [~] 3.4 Implement `VerifyWebhook` as a pure function
    - HMAC-SHA256 over `{svix-id}.{svix-timestamp}.{body}` compared with `hmac.Equal`, base64 secret after
      the `whsec_` prefix; reject a timestamp more than 5 minutes from the passed-in `now`
    - Keep it a pure function over the raw body, the three svix headers, and `now` — no server, no Svix
      library — which is what makes P21 cheap
    - Implements design *Webhook_Consumer* signature verification
    - _Requirements: 19.1, 19.2, 19.3_

  - [~] 3.5 Implement the HTTP client in `stytch/client_http.go`
    - Wrap `stytch-go/v18` for the discovery flows, organizations, members, and sessions; confine the SDK
      to this one file so the `Client` interface stays the only seam
    - `Config.BaseURL` is injected so tests point it at an `httptest.Server` with no `t.Setenv`
    - _Requirements: 1.1, 1.2, 2.1, 2.16, 3.8, 11.9, 13.3, 15.9, 17.3, 17.5, 17.6, 18.8_

  - [~] 3.6 Implement `stytch.Fake` with call recording, `FailAt`, and RSA session-JWT minting
    - In-memory maps, no goroutine, no network; currently compiled into the non-test build so other
      packages' tests can use it without a cyclic test-only import. This MUST NOT be treated as
      acceptable long-term: `FailAt` is failure-injection machinery, and shipping it reachable from a
      production binary is a supply-chain risk. `main.go` refuses to start with employer auth enabled
      while this fake is the wired `Client` (see task 3.5's completion criterion below) as a stopgap.
      Once task 3.5's real HTTP client is wired everywhere `stytch.Fake` is used in `main.go` today, move
      this file to a test-only `stytch/fake` package (or gate it with a `//go:build test` tag) so it is
      no longer reachable from the production build at all
    - `Calls []Call` recording every call in order, so "zero Stytch calls" becomes an assertion
    - `FailAt map[string][]FailSpec` keyed by operation and attempt number — the generator surface for P8,
      P30, and the retry-schedule property
    - `SigningKey *rsa.PrivateKey`, `JWKS()`, and `MintSessionJWT(memberID, orgID, ttl)` so the adapter's
      *actual* local verification path is exercised rather than a stubbed one
    - `CallCount(op)` and `Reset()`
    - **Prerequisite for nearly every property test in this plan**
    - Implements design *The in-process Stytch fake*
    - _Requirements: testing strategy; the requirements' PBT preamble_

  - [~] 3.7 Implement `fakeClock` and the shared test helpers
    - A `Clock` implementation advanced explicitly, so the 15-minute challenge expiry, the 7-day invitation
      expiry, the 72-hour reaper, the 7-day deletion window, and the 30-second cache bound are testable in
      microseconds with no test sleeping longer than 50 ms
    - A notification spy implementing the small interface the services depend on
    - Implements design *Test infrastructure*
    - _Requirements: testing strategy_

  - [ ]* 3.8 Write property test for webhook signature verification
    - **Property 21: Webhook signature verification rejects every tampered payload**
    - Generator: `genWebhookPayload` × a mutation drawn from {body byte, signature byte, timestamp byte,
      header removal}; oracle is "verification must fail, no panic, zero state change"
    - **Validates: Requirements 19.1, 19.2, 19.3**

  - [ ]* 3.9 Write example-based tests for the adapter
    - JWKS fetch, and a stale-serve test asserting the last good key set is served after a failed refresh
    - Health payload after a success, after a failure, and one asserting no secret appears
    - Retry-schedule assertions: no retry for a mutating call without an idempotency key; 429 honours
      `Retry-After`; exhaustion emits exactly one error record
    - _Requirements: 20.2, 20.3, 20.6, 20.13_


- [ ] 4. Permission_Matrix, route registry, and the startup audit
  - [-] 4.1 Implement the Permission_Matrix as a bitmask declaration
    - Create `workspaces/permissions.go` with `Role` and its four constants, `AllRoles`, the 26
      `Permission` constants, `AllPermissions`, and `PermissionSet uint32` as a bitmask over the index of
      each permission in `AllPermissions`
    - Transcribe `permissionMatrix` verbatim from Req 7.3–7.6; `workspace.import_personal_data` is
      Owner-only because it is absent from the Req 7.4 Admin list
    - The Admin row includes `PermBillingView`, `PermInvoiceView`, `PermPayoutView`, and `PermAuditView`
      — read-only money access — and excludes `PermBillingManage`, `PermPaymentMethodManage`,
      `PermInvoicePay`, and `PermSubscriptionManage`, which Req 7.9 reserves to `owner` and `finance`
    - `Grants(r, p)` is a single AND with no database handle in scope, no map lookup on the hot path, and
      no allocation; an unknown role yields the empty set rather than a panic
    - Add `GrantedPermissions(r)` with stable order and `ValidRole(s)`
    - Implements design *Permission_Matrix*
    - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 7.8, 7.9, 7.13, 7.17, 7.18_

  - [~] 4.2 Implement `VerifyMatrix()` as a startup assertion
    - Assert every `Permission` constant appears in `AllPermissions` exactly once, that `permissionMatrix`
      has an entry for all four roles, and that Owner's set is a superset of the union of the other three
    - Return an error so `main.go` exits — a matrix typo is a deployment-blocking bug
    - _Requirements: 7.3, 7.13_

  - [ ]* 4.3 Write property test for the Permission_Matrix
    - **Property 1: The Permission_Matrix is a total function with an Owner superset**
    - Generator: `genRole` × `genPermission` over the two closed sets, plus arbitrary strings for the
      unknown-role case; oracle is the four literal sets from Req 7.3–7.6 transcribed independently in the
      test
    - **Validates: Requirements 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.13, 7.16, 7.18**

  - [ ]* 4.4 Write property test for restricted permission unreachability
    - **Property 2: Restricted Permissions are never reachable from a non-Owner Workspace_Role**
    - Generator: `genRole` restricted to non-owner, and to non-owner-non-finance; oracle is the two
      exclusion lists from Req 7.7–7.9
    - The non-owner-non-finance exclusion list is exactly `billing.manage`, `payment_method.manage`,
      `invoice.pay`, and `subscription.manage`; `invoice.view` and `payout.view` are **not** in it,
      because Req 7.4 grants both to Admin — an oracle that excluded them would fail against the matrix
    - **Validates: Requirements 7.7, 7.8, 7.9, 7.10, 7.11, 7.12, 24.6, 24.7**

  - [~] 4.5 Implement `ScopedGroup`, `RouteRegistry`, and `workspaceScopedRoutes`
    - Create `workspaces/guard.go` with `ScopedGroup` whose `GET`/`POST`/`PATCH`/`DELETE` signatures take a
      `Permission` argument, making "registered without declaring a permission" a compile error
    - `RouteRegistry` records every declaration; `workspaceScopedRoutes` is the code-resident statement of
      intent holding every row of the design's scoped route table
    - Implements design *Permission_Guard and the route registry*
    - _Requirements: 8.5_

  - [~] 4.6 Implement `RouteRegistry.Verify` and call it from `main.go` after all registration
    - Assert all four claims: every declared Permission exists in `AllPermissions`; every route in
      `workspaceScopedRoutes` was declared through a `ScopedGroup`; no such route also appears on a
      non-scoped group; and every route registered through a `ScopedGroup` appears in
      `workspaceScopedRoutes`, so the two lists cannot drift
    - Return an error that makes startup fail
    - The three session-scoped `/workspaces` rows are excluded deliberately and by name
    - _Requirements: 8.6_

  - [ ]* 4.7 Write property test for route permission declaration
    - **Property 29: Every registered workspace-scoped endpoint declares a Permission**
    - Generator: the Echo route table from a fully constructed app; oracle is `RouteRegistry` and
      `workspaceScopedRoutes`
    - Also assert every workspace-scoped request emits a log record carrying both the workspace identifier
      and the acting user identifier
    - **Validates: Requirements 8.5, 8.6, 16.11, 23.3**

- [ ] 5. New collections, models, and indexes
  - [~] 5.1 Implement the `workspaces` and `memberships` models with `EnsureIndexes`
    - `Workspace` and `Membership` structs exactly as the design declares them, including
      `NameNormalized`, `StytchOrganizationID` as `json:"-"`, `DeletionState` as an absent field when
      none, `AuthorityVersion` starting at 1, and the four `stytch_member_delete_*` fields
    - Create all eight `workspaces` and `memberships` indexes from the catalogue, including the two
      load-bearing uniques: `uniq_active_owner_per_workspace` (`{workspace_id: 1}` unique with
      `PFE {role: "owner", status: "active"}`) and `uniq_membership_user_workspace`
    - Implements design *`workspaces`*, *`memberships`*, *Complete index catalogue*
    - _Requirements: 2.10, 2.14, 14.1, 14.2, 15.1, 15.2, 16.3, 19.7_

  - [~] 5.2 Implement the `invitations`, `account_links`, and `onboarding_states` models
    - All three structs as declared, with no field anywhere holding a plaintext invitation token, and every
      `onboarding_states` field `json:"-"` because the document carries a live Stytch credential
      (`IntermediateSessionToken`) — a bearer token with write access to create Stytch Organizations.
      Encrypt this field at rest using the existing encryption utilities before it is written to MongoDB;
      a database dump or backup that exposes it in plaintext would let an attacker hijack every in-flight
      onboarding. It must never appear in logs or backups in plaintext
    - Create the five `invitations` indexes, the four `account_links` indexes (including both uniqueness
      directions), and the five `onboarding_states` indexes from the catalogue
    - _Requirements: 3.1, 5.8, 10.8, 10.9, 10.10, 12.1, 16.4_

  - [~] 5.3 Implement the `audit_log_entries`, `processed_webhook_events`, and migration-run models
    - `audit.Entry`, `ProcessedWebhookEvent` with `expires_at = processed_at + 30d`, and
      `employer_migration_runs` / `employer_migration_findings`
    - Create `audit_workspace_time`, `audit_target`, the `webhook_events_ttl` TTL index with
      `expireAfterSeconds: 0`, and `migration_findings_run`
    - _Requirements: 19.4, 21.16, 23.2, 23.4_

  - [~] 5.4 Create the `invoices` collection
    - New collection — `external_invoices` is the artisan→customer invoice keyed by `artisan_id` and is
      **not** repurposed
    - `Invoice` struct as declared, with `WorkspaceName` snapshotted at issue so a later rename does not
      retroactively alter an issued invoice
    - Create `uniq_invoice_number` and `invoices_workspace_issued`
    - Implements design *The `invoices` collection does not exist yet*
    - _Requirements: 9.1, 18.9, 24.1, 24.8, 24.9_

  - [~] 5.5 Wire every new `EnsureIndexes` into `main.go`
    - Call the `EnsureIndexes` function for each of the five new packages, matching the existing convention
      used by `users`, `subscriptions`, `quotations`, `referrals`, `notifications`, `jobrequests`,
      `institutions`, and `files`
    - _Requirements: 8.6, 20.11_

- [ ] 6. Onboarding_Service: signup, candidate discovery, and the Onboarding_Ticket
  - [~] 6.1 Implement email normalization, masking, and the onboarding stage machine
    - `identity.NormalizeEmail`: trim then lowercase — one function used identically by
      `Onboarding_Service`, `Invitation_Service`, and `Migration_Job`. This deliberately does NOT
      normalize Gmail dots or plus-suffixes (`john.doe@gmail.com` and `user+tag@gmail.com` normalize to
      values distinct from `johndoe@gmail.com` and `user@gmail.com` respectively, even though Gmail
      treats them as the same mailbox). This is an accepted launch limitation per Requirement 4 criterion
      7 — do not add provider-specific alias handling here without a requirements change
    - `MaskEmail` reveals at most the first character of the local part plus the full domain; `MaskPhone`
      reveals at most the final 3 digits, and `MaskPhone("12")` returning `"12"` is deliberate rather than
      padded
    - The closed `Stage` set with every transition as a conditional `findOneAndUpdate` naming the expected
      current stage, so a request in the wrong stage matches zero documents and returns 409
    - Implements design *Onboarding_Service*, *Onboarding state machine*
    - _Requirements: 1.1, 1.13, 3.1, 4.3, 4.7, 10.2_

  - [~] 6.2 Implement `InitiateMagicLink` and `OAuthStartURL`
    - Apply `mail.ParseAddress` on the normalized address and return 400 naming the `email` field with zero
      Stytch calls when it fails
    - Verify Turnstile with the existing `turnstile` package using action `employer_signup` before calling
      Stytch; a rejection is 400 with zero Stytch calls
    - Return a fixed `200 {"stage":"verification_sent"}` body regardless of whether the address exists
    - `OAuthStartURL` starts the Stytch OAuth discovery flow for Google; the supported method set is
      exactly `{magic_link, google_oauth}` and an unknown method is 400
    - Implements design *Sequence: employer signup by magic link*
    - _Requirements: 1.1, 1.2, 1.5, 1.8, 1.9, 1.10, 25.4_

  - [~] 6.3 Implement `Authenticate`, candidate discovery, and the `users` document insert
    - Treat the email as verified only when Stytch reports it verified; an unverified address returns 403
      with zero workspaces, zero memberships, and zero writes
    - Upsert `onboarding_states` on `{stytch_member_id}` so re-authentication resumes rather than creating
      a second record
    - Search candidates *only after* a verified authentication, via `GetUsersByNormalizedEmail`
    - With zero candidates, insert the `users` document with `user_type: "customer"`, `status: "active"`,
      `is_active: true`, zero password bytes, the normalized email, and the `phone` **key omitted**, then
      set stage `awaiting_workspace_name`
    - Record the signup method, the Stytch `member_id`, and the Stytch `organization_id` on the records
      this feature creates
    - Depends on task 2.1 — the phone-less insert fails without the partial index. This dependency is a
      hard sequencing constraint with no code-level guard: if task 6.3 ships before task 2.1's migration
      has completed, the insert fails with `E11000` on the old non-partial `uniq_phone` index. Add a
      startup assertion in `main.go` (after `users.EnsureIndexes` runs) that reads back the `uniq_phone`
      index definition and fails startup if it is not the partial form, so a misordered deploy fails
      loudly at startup rather than on the second phone-less signup in production
    - _Requirements: 1.3, 1.6, 1.7, 1.11, 1.12, 1.13, 1.16, 3.2, 3.6, 4.1, 4.2, 15.5, 25.5_

  - [~] 6.4 Implement the Onboarding_Ticket and its allow-list
    - Mint an HS256 JWT signed with `configs.GetSessionKey()` carrying `Audience: ["fundis-onboarding"]`
      and a 30-minute TTL, bound to the `onboarding_states` document, so it can never be mistaken for a
      `jwtutil` access token; the `intermediate_session_token` never leaves the server
    - `Resolver.Authenticate` recognises the audience and attaches an onboarding-only marker instead of a
      `SessionContext`; the guard denies every workspace-scoped route with 403 and permits exactly the
      seven onboarding paths, expressed as a **table** so the audit can enumerate it
    - Implements design *The Onboarding_Ticket*
    - _Requirements: 1.4_

  - [~] 6.5 Implement `SubmitName` and the candidate-exclusion rules on `DecideLink`
    - Collect a first and last name before the state reaches `complete`, pre-filled from the Stytch-reported
      name when present
    - `DecideLink` accepts exactly two decisions; exclude a candidate already linked to a different member
      with `409 email_already_linked`; withhold the link option with `email_belongs_to_non_employer` for
      `user_type` in {artisan, assessor, admin}; withhold with `link_candidate_suspended` for a suspended
      candidate; require a distinct email for "create a separate account"
    - Present every case-variant match as a candidate and create at most one Account_Link across the set
    - _Requirements: 1.15, 4.2, 4.4, 4.5, 4.6, 4.8, 4.9, 4.10_

  - [ ]* 6.6 Write property test for email normalization
    - **Property 3: Email_Normalization is idempotent and case-collapsing**
    - Generator: `genEmailVariant` — a base address plus a transformation drawn from {case-flip,
      pad-whitespace, plus-suffix, dot-shift, unicode-homoglyph}; oracle is self-comparison for idempotence
      and the transformation kind for equality
    - **Validates: Requirements 1.1, 1.13, 4.1, 4.7, 10.2, 21.7**

  - [ ]* 6.7 Write property test for signup non-disclosure
    - **Property 27: Signup responses do not disclose account existence**
    - Generator: `genEmailVariant` × a `users` population of 0–8; oracle is a byte comparison of the two
      response bodies and statuses. Requires a real test database
    - **Validates: Requirements 25.4, 25.5**

  - [ ]* 6.8 Write example-based tests for signup wiring
    - Turnstile accept and reject stubs on signup initiation, asserting the action name `employer_signup`
      and zero Stytch calls on reject
    - Assert the supported signup method set is exactly `{magic_link, google_oauth}`
    - Assert the first/last name is pre-filled when the fake reports a name
    - Magic-link and Google OAuth round trips against the Stytch test environment, skipped unless
      `STYTCH_PROJECT_ID` is set so `make test` is green without credentials
    - _Requirements: 1.1, 1.2, 1.8, 1.9, 1.10, 1.15, 25.7_


- [ ] 7. Session_Resolver and Permission_Guard
  - [~] 7.1 Implement `SessionContext` and the credential chain
    - `SessionContext` immutable once resolved, with every field derived from the credential or read from
      the membership document and never from a token claim; `Session.Roles` is populated for observability
      only and is deliberately not typed as `workspaces.Role`
    - The chain in fixed order: RS256 JWT with a matching `iss` → `VerifySessionJWT` with no network; opaque
      → `AuthenticateSession` cached for the request; HS256 → `jwtutil.ValidateToken`, accepted while
      `LEGACY_EMPLOYER_AUTH_ENABLED` is true for a linked user and always for an unlinked one; none →
      `401 {"success": false, "message": "Unauthorized"}` byte-identical across all four failure modes with
      no hint about which path was tried
    - Implements design *Session_Resolver*, *Credential order*
    - _Requirements: 16.1, 16.2, 16.12, 16.13, 21.10, 22.2, 22.3_

  - [~] 7.2 Implement member-to-user resolution through `memberships.stytch_member_id`
    - Resolve `(user_id, workspace_id)` in one indexed read on `memberships.stytch_member_id`, which carries
      the per-workspace Member identifier, because a Stytch B2B Member is per-Organization while Req 5.8
      binds one Account_Link per `users` document
    - `Resolve` requires **both** a membership holding the session's `member_id` and an Account_Link for the
      `user_id` that membership names; absent either → `401 unlinked_member`
    - Derive the active workspace from the session's `organization_id`, or from the URL path when present
    - Derive the role from the Fundis membership document with zero reliance on any session role claim
    - This is the ratified reading of Req 16.3 and 16.4, stated verbatim in the requirements; `account_links`
      is the primary identity binding and per-request resolution goes through the membership
    - _Requirements: 16.3, 16.4, 16.5, 16.6, 8.10_

  - [~] 7.3 Implement the 30-second context cache and its invalidation
    - An in-process sharded map keyed on `(userID, workspaceID)`, 30-second TTL from the injected clock; a
      hit serves the stored context with zero Mongo reads and zero Stytch calls
    - Store `AuthorityVersion` on the entry and compare it on every refresh
    - `resolver.Invalidate(userID, workspaceID)` in-process on the writing instance, plus a publish of
      `{user_id, workspace_id, authority_version}` on the Redis channel `fundis:workspace:authority` that
      every instance subscribes to; when `configs.RDB` is nil the publish is skipped and the 30-second TTL
      is the only bound, which is a documented degradation
    - Implements design *Context cache*
    - _Requirements: 16.7, 16.8, 16.9, 17.2, 17.4, 17.8, 17.10_

  - [~] 7.4 Implement `RequirePermission` in the design's exact evaluation order
    - The seven-step table in order: (1) unresolved credential → 401 fixed body; (2)
      `users.status == "suspended"` → 403; (3) no membership for `(user, workspace)` or `status == "removed"`
      → 404; (4) `status == "suspended"` → 403 `membership_suspended`; (5) `deletion_state == "scheduled"`
      on a write whose permission is not `workspace.delete` → 409 `workspace_deletion_scheduled`; (6)
      `!sc.Grants(required)` → 403 `permission_denied` with `details.missing_permission`; (7) otherwise set
      the workspace id and the context and call the handler
    - Step 3 before step 4 makes a removed membership indistinguishable from a nonexistent workspace; step 2
      before step 3 is what makes Req 8.9 apply "regardless of Workspace_Role"
    - Apply zero authorization decisions based on `user_type`; every early return runs the handler zero times
    - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.7, 8.8, 8.9, 8.10, 8.11, 18.4_

  - [~] 7.5 Add `contextutil.SetWorkspaceID`/`GetWorkspaceID` and emit `workspace.id` from `AccessLog`
    - Mirror the existing `SetUser` primitives; add `logging.ContextWithWorkspaceID` alongside
      `ContextWithUserID`, keeping `logging` stdlib-only
    - `middleware/requestlog.go` `AccessLog` emits `workspace.id` when the request context carries one
    - No import cycle is created: `contextutil` does not import `workspaces`
    - _Requirements: 16.11_

  - [~] 7.6 Implement the session, permission-matrix, and self-revocation endpoints
    - `GET /workspaces/:workspaceID/permissions` returning the matrix and the caller's granted set
    - Expose the active workspace, role, and granted permission set on the authenticated-session endpoint
    - An endpoint letting an authenticated employer revoke every Stytch session across every workspace
    - Expose the linked email and link method on the account settings endpoint
    - _Requirements: 7.16, 16.10, 17.6, 5.15_

  - [ ]* 7.7 Write property test for guard denial of suspended and removed memberships
    - **Property 17: A suspended or removed Membership is denied every workspace-scoped Permission**
    - Generator: `genRole` × `genPermission` × `genMembershipStatus` × `genUserStatus`; oracle is the guard
      decision table, with a handler-invocation counter asserting zero calls
    - **Validates: Requirements 8.1, 8.2, 8.3, 8.4, 8.7, 8.8, 8.9, 8.10, 8.11, 13.5, 18.4**

  - [ ]* 7.8 Write property test for cache staleness bounds
    - **Property 19: Session_Resolver caching never serves stale authority beyond the bound**
    - Generator: an interleaving of {request, role change, suspend, remove, advance clock by 0–120 s}
      against `fakeClock`; oracle is the stored membership versus the resolved context, with a simulated-time
      bound of 30 s. Run with `-race`
    - **Validates: Requirements 16.7, 16.8, 16.9, 17.2, 17.3, 17.4, 17.8, 17.9**

- [ ] 8. Workspace provisioning and the account-link flow
  - [~] 8.1 Implement `ValidateName`, `NormalizeName`, and `DeriveSlug` as pure functions
    - `ValidateName`: accept when the trimmed length is in the closed interval 2–80 and the trimmed string
      holds at least one Unicode letter or number, else 400 naming `workspace_name`
    - `NormalizeName`: trim, then collapse each run of whitespace to one space
    - `DeriveSlug`: lowercase, replace each run outside {a–z, 0–9, `-`} with one hyphen, strip leading and
      trailing hyphens, and return `workspace` when the derivation empties
    - All three are database-free, which is what makes P4 and P6 pure
    - _Requirements: 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.9_

  - [~] 8.2 Implement `CreateForOnboarding` with lease, organization reuse, and slug allocation
    - Win a lease with a conditional `findOneAndUpdate` from `awaiting_workspace_name` to `provisioning`;
      a lost lease returns `409 provisioning_in_progress`
    - When the state already carries `stytch_organization_id`, reuse it and make zero Stytch creates
    - Otherwise call `CreateOrganizationFromDiscovery` with JIT `RESTRICTED`, invites `ALL_ALLOWED`, and
      idempotency key `"onboarding:" + state._id`, then write `stytch_organization_id` onto the state
      **before** any workspace write
    - Insert `workspaces`, retrying the slug with `-2`, `-3`, … up to `maxSlugRetryAttempts` (100, a named
      constant, not an inline literal) attempts on `mongo.IsDuplicateKeyError`, letting `uniq_workspace_slug`
      be the arbiter rather than a read-then-write check — the same shape as the proven
      `users.PublishWithUniqueSlug`. 100 collisions on one base name is an accepted ceiling for launch;
      revisit (configurable max, or a UUID-suffix fallback after a low threshold) if abuse on a single
      popular base name is observed, since exhausting the space for a given name currently surfaces as a
      503 to the 101st creator of that name
    - Extract the retry loop into one shared helper used by both `CreateForOnboarding` and
      `CreateAdditional` (task 8.3) rather than duplicating it, so the retry policy has one definition
    - A failed insert returns 503 with the organization id retained
    - Insert the owner `memberships` document with `authority_version: 1` and the `account_links` document,
      then set stage `complete`
    - Reject a name identical after normalization and case-insensitive comparison to a workspace the caller
      already actively belongs to, with `409 duplicate_workspace_name` and the existing workspace id
    - Create zero companies — company creation stays in the vacancy-posting flow
    - Implements design *Sequence: employer signup by magic link*, *Idempotency keys*
    - _Requirements: 2.1, 2.8, 2.10, 2.11, 2.12, 2.13, 2.14, 2.17, 3.2, 3.3, 3.4, 3.5, 3.10, 3.12_

  - [~] 8.3 Implement `Rename`, `CreateAdditional`, and `ListForUser`
    - `Rename` applies validation and normalization to the new value, retains the existing slug, and
      propagates the new name to the Stytch organization
    - `CreateAdditional` applies the same creation rules and caps one employer at 5 workspaces per 24 hours
      with 429 beyond that
    - `ListForUser` returns name, slug, role, and member count for every workspace holding an active
      membership
    - _Requirements: 2.15, 2.16, 15.10, 15.12, 15.13_

  - [~] 8.4 Implement `SelectChallenge`, `Issue`, and `Verify` in `identity.LinkService`
    - `SelectChallenge` is pure: matching normalized emails → `stytch_email_match` satisfied immediately; a
      candidate with a phone and differing emails → `phone_otp`; no phone and differing emails → 409
      contact support
    - `Issue` reuses `users.IssuePhoneOTP` unchanged and records `challenge {method, issued_at, expires_at:
      +15m, attempts: 0}` on the state; the 5-per-hour cap runs through the Rate_Limiter
    - `Verify` returns 401 and `$inc challenge.attempts` on an incorrect value with zero links created; the
      OTP's own verdict wins where the two lifetimes disagree, and the attempt still counts
    - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6_

  - [~] 8.5 Implement `Link`, `Unlink`, and `ApplyEmailChange`
    - `Link` inserts exactly one `account_links` row against both unique indexes, sets `is_active`/`status`
      active on a previously inactive candidate, retains the bcrypt hash untouched, offers linking with zero
      password entry for a `migrated: true` candidate, writes one audit entry naming the user, the member,
      the challenge method, and the request IP, sends the email plus SMS naming the change time, and advances
      the stage to `awaiting_workspace_name`
    - `Unlink` succeeds only for a user holding zero owner memberships, else 409 naming the affected
      workspaces
    - `ApplyEmailChange` retains the link, updates the stored email only when the new address is unheld, and
      on a collision retains the old address, sets `review_required`, and writes one audit entry naming both
      addresses masked
    - On subsequent authentications, sign a linked employer in through Stytch and create zero additional
      `users` documents
    - _Requirements: 4.11, 4.12, 5.7, 5.8, 5.9, 5.10, 5.11, 5.12, 5.13, 5.14, 5.16_

  - [~] 8.6 Implement `RescopeUserData` and the import-personal-data endpoint
    - Idempotent `updateMany` per collection with a filter excluding already-scoped rows, writing
      `companies`, `job_vacancies`, and `user_subscriptions` **directly** via `configs.MI.DB` so no
      `workspaces → jobrequests` or `workspaces → subscriptions` import is created. This is an accepted
      layering tradeoff, not an oversight: it bypasses the `jobrequests`/`subscriptions` service layers
      (their validation, audit hooks, and business logic), so if those collections' schemas or invariants
      change, this direct write will not pick up the change automatically. If that becomes a maintenance
      burden, the fix is breaking the import cycle properly (a `RescopeService` in a shared package, or a
      callback interface passed into `workspaces`), not adding more direct collection access
    - Attempt the `companies`, `job_vacancies`, and `user_subscriptions` updates independently — a failure
      on one MUST NOT skip the others, since nothing prevents attempting all three. Retain every
      `owner_id`; on any failure record it, leave the workspace usable, and set `rescope_pending` so the
      next authenticated request (or a scheduled reconciliation) retries the remainder
    - For an employer who already holds an active membership, apply zero re-scoping and return the
      workspaces the data may be transferred into
    - `POST /workspaces/:workspaceID/import-personal-data` requires `workspace.import_personal_data` and
      rejects a workspace where the caller holds no active membership with 403
    - Apply zero deletions to quotations, reviews, transactions, or notifications
    - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 6.10_

  - [ ]* 8.7 Write property tests for name validation and slug derivation
    - **Property 4: Workspace_Slug derivation is idempotent and yields a valid slug** — generator
      `genWorkspaceName` covering emoji-only, Arabic-only, punctuation-only, whitespace-only, mixed-script,
      and 1/2/80/81 runes; oracle is the charset and shape predicates plus self-comparison
      **Validates: Requirements 2.7, 2.8, 2.9**
    - **Property 6: Workspace_Name validation accepts exactly the specified range** — generator biased to
      trimmed lengths 0, 1, 2, 3, 79, 80, 81; oracle is the two-line reference predicate
      **Validates: Requirements 2.2, 2.3, 2.4, 2.5, 2.6, 2.15**

  - [ ]* 8.8 Write property test for slug allocation
    - **Property 5: Slug allocation never collides**
    - Generator: `genNameSequence` of 1–30 names with 60% drawn from a 3-element pool so base slugs collide;
      oracle is distinctness of the resulting slug set. Requires a real test database
    - **Validates: Requirements 2.8, 2.10, 2.11**

  - [ ]* 8.9 Write property test for provisioning idempotence
    - **Property 7: Onboarding provisioning is idempotent**
    - Generator: `rapid.SampledFrom` over {sequential submit, concurrent submit ×N, retry after failure} with
      N drawn 1–8; oracle is all four counts equal to 1 and `Fake.CallCount("organizations.create") == 1`.
      Requires a real test database and `-race`
    - **Validates: Requirements 2.1, 2.13, 2.14, 2.17, 3.2, 3.3, 3.4, 3.10, 3.12, 21.2, 21.5**

  - [ ]* 8.10 Write property test for onboarding resumability
    - **Property 8: Onboarding is resumable from any interruption point**
    - Generator: `genFailurePoint` — an index into the 6-step provisioning sequence × a `FailSpec` injected
      through `Fake.FailAt`; oracle is the replay-to-completion state. Requires a real test database
    - **Validates: Requirements 3.1, 3.5, 3.6, 3.7, 3.8, 3.11**

  - [ ]* 8.11 Write property tests for account linking
    - **Property 22: Account_Link uniqueness holds in both directions** — generator `genLinkAttempts` of
      1–20 attempts over 1–5 users × 1–5 members interleaved concurrently, including users who then join
      further workspaces so that extra `memberships.stytch_member_id` values exist without extra
      Account_Links (Req 5.8 as ratified); oracle is both uniqueness directions, with the surviving
      Account_Link referencing the Member of the workspace where the link was established. Real test
      database, `-race`
      **Validates: Requirements 5.7, 5.8, 5.10, 5.16, 4.4**
    - **Property 23: Linking never creates a duplicate user** — generator `genEmailVariant` populations of
      0–8 pre-existing users plus 0–6 phone-less signups; oracle is count delta ≤ 1 *and* that every
      phone-less insert succeeds, which is the folded Req 1.12 claim and the reason this property needs a
      real MongoDB rather than an in-memory repository
      **Validates: Requirements 1.11, 1.12, 1.14, 4.6, 4.8, 4.9, 4.10, 4.12, 5.12, 21.6**

  - [ ]* 8.12 Write property test for Link_Challenge limits
    - **Property 25: Link_Challenge attempt limits are enforced exactly**
    - Generator: a submission sequence over {correct, incorrect} × clock advances of 0–90 minutes; oracle is
      zero links before a correct in-window submission and at most 5 accepted per candidate per hour
    - **Validates: Requirements 5.1, 5.2, 5.3, 5.4, 5.5, 5.6**

  - [ ]* 8.13 Write property test for data re-scoping
    - **Property 24: Data re-scoping is idempotent and lossless**
    - Generator: 0–15 documents per collection with mixed `workspace_id` presence; oracle is the
      once-applied state plus count and `owner_id` preservation. Requires a real test database
    - **Validates: Requirements 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.10, 13.13, 13.14, 18.9, 18.14, 21.4**

  - [ ]* 8.14 Write example-based tests for linking and workspace naming
    - Link a `migrated: true` candidate with no password entry
    - Assert the account settings payload exposes `linked_email` and `link_method`
    - Notification spy asserts the link email and SMS both name the change time
    - Same name for two workspaces yields distinct slugs
    - Assert the recorded organization-create call carries JIT `RESTRICTED` and invites `ALL_ALLOWED`
    - _Requirements: 2.11, 2.13, 4.11, 5.9, 5.15_

- [~] 9. Checkpoint - Ensure all tests pass
  - Run `go build ./...`, `go vet ./...`, and `go test ./... -race`
  - Confirm the legacy regression bar holds: the suite is green with **zero edits** to pre-existing
    assertions in `auth`, `users`, `jobrequests`, and `subscriptions`
  - Ensure all tests pass, ask the user if questions arise.


- [ ] 10. Membership lifecycle, owner protection, and ownership transfer
  - [~] 10.1 Implement `MembershipService.Create` and `List`
    - Insert against `uniq_membership_user_workspace`; a duplicate key returns the existing membership rather
      than an error
    - `List` returns display name, email, role, status, and join date for each member
    - Write one audit entry on creation
    - _Requirements: 11.11, 13.15, 15.1, 15.2, 23.1_

  - [~] 10.2 Implement `ChangeRole` and `SetStatus` as single-document authority writes
    - Every authority write is one `findOneAndUpdate` whose filter encodes the precondition and whose update
      carries `$inc: {authority_version: 1}` in the **same** write; `authority_version` is never written with
      `$set` anywhere, which is the one discipline P18 depends on
    - Use `ReturnDocument(Before)` so the previous value is available for the audit entry without a re-read
    - `SetStatus` covers suspend (retaining the Stytch member), remove (retaining the record), and reinstate
      (reusing the existing document, recreating the Stytch member when none exists for that person in the
      organization)
    - Apply zero deletions to the person's `users` document; retain every `companies`, `job_vacancies`, and
      `job_applications` document they created and keep them accessible to remaining members
    - Call `resolver.Invalidate` and publish on the Redis authority channel in the same code path
    - _Requirements: 13.1, 13.2, 13.11, 13.12, 13.13, 13.14, 17.1, 17.4_

  - [~] 10.3 Implement the eleven-row guard table
    - Enforce, before the write and again in the filter: admin changing the Owner's role → `403
      owner_protected`; admin removing the Owner → `403 owner_protected`; admin granting `owner` → 403; a
      role value outside the four → 400; demoting the sole Owner outside `TransferOwnership` → `409
      last_owner_protected`; suspending or removing the sole Owner → `409 last_owner_protected`; a caller
      changing their own role → `409 self_role_change_denied`; an Owner removing their own membership → `409
      owner_must_transfer_first`; the sole member removing their membership → 409 directing to the deletion
      flow; a transfer target that is not `active` → 409; a transfer target whose user is suspended → 409;
      a transfer target that is the caller → 400
    - Implements design *Membership_Service* guard table
    - _Requirements: 7.10, 7.11, 7.12, 7.17, 13.7, 13.8, 14.5, 14.6, 14.7, 14.11, 14.12, 14.14_

  - [~] 10.4 Implement `TransferOwnership` inside a MongoDB transaction
    - Transactions are available: production and staging are replica sets (ratified), and task 1.3 fails
      startup when employer Stytch auth is enabled without transaction support, so the transactional path
      is the only path — no standalone-degradation variant is implemented
    - Return `503 transactions_unavailable` when `m.txCapable` is false rather than performing a silent
      best-effort swap; this is unreachable defence in depth given the startup fail-fast
    - Inside `WithTransaction`: demote the caller with filter `{_id: caller, role: owner, status: active}` and
      promote the target with filter `{_id: target, status: active, role: {$ne: owner}}`, each `$inc`-ing its
      authority version; both must match exactly one document or the transaction aborts and both roles are
      left unchanged
    - Neither ordering works without a transaction: promote-then-demote is rejected by
      `uniq_active_owner_per_workspace`, and demote-then-promote leaves a zero-Owner window that Req 14.1
      forbids and P9 observes
    - Depends on task 1.3 (`configs.TransactionsSupported`)
    - Implements design *Transaction availability*
    - _Requirements: 14.3, 14.4, 14.8_

  - [~] 10.5 Implement `AdminTransferOwnership` and the zero-owner detector
    - `POST /admin/workspaces/:workspaceID/ownership` behind `middleware.IsAdmin`, retaining the owner
      membership of a platform-suspended user and transferring to a named active membership
    - Detect a workspace with zero active owner memberships, record one error-severity log naming it, and
      include it in the orphan report
    - _Requirements: 14.13, 14.15_

  - [~] 10.6 Implement Stytch mirroring, session revocation, and the delete-retry state
    - After a successful Fundis write, mirror the role with `UpdateMemberRoles`; a failure produces one
      warning and leaves the Fundis document alone, because rolling back would trade a cosmetic mismatch for
      an authority mismatch. This means Fundis and Stytch roles can drift during a Stytch outage or
      transient failure — Fundis remains authoritative for authorization (Req 7.14), but Stytch RBAC (if
      ever consulted for anything) would be stale until the next successful mirror. The role-mirror
      failure path does NOT set `stytch_member_delete_pending` or any equivalent retry flag today; a
      reconciliation job that periodically diffs Fundis Membership roles against Stytch and repairs drift
      is deferred work, not implemented in this task
    - On `SetStatus(removed|suspended)` call `RevokeMemberSessions`, and for `removed` also `DeleteMember`; a
      failure sets `stytch_member_delete_pending` with the attempt count and first-failure time. Every
      write that sets this flag (or clears it, or increments the attempt count) MUST check and log its own
      error rather than discard it with `_, _ =` — a discarded error here means the retry scheduler never
      learns the delete needs retrying and the Stytch member stays orphaned permanently
    - On a `users.status` change to suspended, revoke every session held by every linked member across every
      organization; log (not discard) any per-membership revocation failure, so a failed revocation is
      visible instead of silently leaving that membership's sessions live
    - Implement `RetryPendingStytchDeletes(ctx, now)` retrying at least every 15 minutes for at least 24 hours
    - A revocation failure records one warning and relies on the membership check to deny access
    - _Requirements: 7.15, 13.3, 13.4, 17.3, 17.5, 17.7_

  - [~] 10.7 Implement the membership notification and audit writes
    - Email the removed person that access to the named workspace has ended; email a role-change naming the
      previous and the new role; email both parties on an ownership transfer naming the workspace and the
      transfer time
    - Write one audit entry per role change, status change, and transfer, naming the acting user, the target,
      the workspace, the previous value, and the new value
    - _Requirements: 13.6, 13.9, 13.10, 14.9, 14.10, 23.1, 23.2, 23.10_

  - [ ]* 10.8 Write property test for the single-Owner invariant
    - **Property 9: Every Workspace has exactly one Owner**
    - Generator: a `t.Repeat` state machine over the 8 operations {invite, accept, role change, suspend,
      remove, reinstate, self-remove, ownership transfer}, 1–40 steps, 1–4 workspaces, 1–6 users; oracle is a
      reference model tracking `(workspace → owner)` with the invariant checked after **every** step, plus
      the machine-readable code on every rejection. Requires a real test database
    - **Validates: Requirements 14.1, 14.2, 14.5, 14.6, 14.7, 14.11, 14.12, 14.14, 14.15, 13.7, 13.8**

  - [ ]* 10.9 Write property test for ownership transfer conservation
    - **Property 10: Ownership transfer conserves the Membership count and swaps exactly two roles**
    - Generator: `genMembershipSet` of 2–8 members × `genFailurePoint` inside the transaction; oracle is a
      before/after role multiset comparison. Requires a real test database
    - **Validates: Requirements 14.3, 14.4, 14.8, 14.10**

  - [ ]* 10.10 Write property tests for membership uniqueness and authority versioning
    - **Property 14: A user holds at most one Membership per Workspace** — generator `t.Repeat` over
      {invite, accept, remove, reinstate} for a fixed (email, workspace); oracle is a membership count ≤ 1
      and a reinstatement reusing the existing document. Real test database, `-race`
      **Validates: Requirements 15.1, 15.2, 10.7, 13.12**
    - **Property 18: Authority_Version increases monotonically and exactly once per authority change** —
      reuse the P9 sequence generator; oracle is a version delta equal to the number of authority-changing
      steps. Real test database
      **Validates: Requirements 13.1, 13.2, 13.10, 13.11, 13.12, 17.1, 19.7**

  - [ ]* 10.11 Write example-based tests for membership emails and admin transfer
    - Spy asserts the removal and role-change email recipients and the previous→new role naming
    - Spy asserts the ownership transfer emails name both parties and the transfer time
    - Suspend the Owner's user, then assert the membership is retained and the admin transfer route succeeds
    - _Requirements: 13.6, 13.9, 14.9, 14.13_

- [ ] 11. Invitation lifecycle
  - [~] 11.1 Implement the invitation model, token handling, and `EffectiveStatus`
    - `newToken()` draws 32 bytes from `crypto/rand` as base64url and returns the plaintext plus its sha256
      hex hash; only the hash is stored, on any document, at any point
    - `EffectiveStatus(stored, expiresAt, now)` is pure and reports a stored `pending` row past its expiry as
      `expired` on every read without waiting for the sweep
    - _Requirements: 10.8, 12.1, 12.7_

  - [~] 11.2 Implement `Create` as an upsert on the partial unique index
    - Check the 20-per-workspace-per-hour limit and the 100-pending cap, returning `invite_rate_limited`
      (429) and `pending_invite_limit` (409)
    - Normalize the email; reject role `owner` with `400 owner_not_invitable`; reject an unparseable address
      with 400 naming `email` and zero emails sent; reject an existing active member with `409
      already_a_member` and the existing role
    - Upsert on `uniq_pending_invitation_workspace_email`; an existing pending row is updated in place with
      the new role and a refreshed expiry, returning `200 invitation_updated` — the same index that makes
      Req 11.17 and P12 hold by construction
    - Set `expires_at = now + 168h`; record inviter, workspace, email, role, issuance, and expiry
    - Create a pending invitation for a `removed` member's address, reactivating only on acceptance
    - Defer `users` document creation to acceptance when the address matches nothing
    - Reject creation for a workspace whose deletion is scheduled or complete with 409
    - On a send failure retain the pending row and return `202 invitation_created_delivery_pending`
    - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.9, 10.10, 10.12, 10.13, 10.14, 10.15, 10.16, 10.18_

  - [~] 11.3 Implement the invitation email and `List`
    - The email names the workspace, the inviter's display name, the offered role, a plain-language summary
      of that role's permissions, and the expiry date
    - `List` returns status, offered role, invited email, and expiry, and returns zero token values
    - _Requirements: 10.11, 10.17_

  - [~] 11.4 Implement `Resolve` and `Decline`
    - `Resolve` looks up by `sha256(token)` and returns workspace name, role, inviter, and the masked invited
      email before requiring authentication; a miss is `404 invitation_not_found`; revoked is 410, expired is
      410 with `can_request_new`, and a deleted-or-scheduled workspace is 410
    - `Decline` uses the token as the sole authorization with no credential required, sets `declined`, creates
      zero memberships, and notifies the inviter
    - _Requirements: 11.1, 11.2, 11.12, 11.13, 11.14, 12.5, 12.6_

  - [~] 11.5 Implement `Accept`
    - Turnstile-verify, then compare the normalized authenticated address with the invited address; a mismatch
      is `403 invitation_email_mismatch` with the masked invited address and zero memberships
    - Resolve or create the `users` document, applying the Requirement 5 link flow when an unlinked match
      exists and creating the document per Req 1.11–1.13 when nothing matches
    - Call `CreateMember` in the workspace's organization; a Stytch failure returns 503 with the invitation
      left `pending` and zero memberships
    - Create the membership; a duplicate key returns 200 naming the existing membership
    - Set `accepted`, revoke every other pending row for the same (workspace, email), notify the inviter, and
      set the accepting person's active workspace on the response
    - _Requirements: 11.3, 11.4, 11.6, 11.7, 11.8, 11.9, 11.10, 11.11, 11.15, 11.16, 11.17_

  - [~] 11.6 Implement `Resend`, `Revoke`, and `SweepExpired`
    - `Resend` mints a fresh token, invalidates the previous one, resets the expiry to 7 days out, sends one
      email, and is capped at 3 per invitation per hour with 429 beyond that
    - `Revoke` sets `revoked`, invalidates the token, and writes one audit entry naming the revoking user, the
      workspace, and the invited email
    - Both reject an invitation already `accepted`, `declined`, or `revoked` with 409
    - `SweepExpired` sets every stored `pending` row past its expiry to `expired`, run at least every 24 hours
      from the scheduler
    - Retain every invitation record — no delete path and no TTL index — and keep a departed inviter's pending
      invitations acceptable
    - _Requirements: 12.2, 12.3, 12.4, 12.8, 12.9, 12.10, 12.11, 12.12_

  - [ ]* 11.7 Write property test for the invitation state machine
    - **Property 11: Invitation state transitions follow the declared machine**
    - Generator: `t.Repeat` over {create, resend, revoke, decline, accept, advance clock}; oracle is the
      five-state reference transition table written as a Go map, asserting no exit from `accepted`,
      `declined`, or `revoked` and exactly one audit entry per transition
    - **Validates: Requirements 12.1, 12.2, 12.3, 12.5, 12.7, 12.8, 12.9, 12.11, 12.12**

  - [ ]* 11.8 Write property test for single-use invitation tokens
    - **Property 12: An Invitation_Token is single-use and grants exactly one Membership**
    - Generator: 1–10 concurrent goroutines presenting one token plus 0–5 sequential replays; oracle is a
      membership count of exactly 1 and every later response naming the existing membership. Real test
      database, `-race`
    - **Validates: Requirements 10.6, 11.3, 11.11, 11.16, 11.17**

  - [ ]* 11.9 Write property test for offered-role fidelity
    - **Property 13: Invitation acceptance never grants a role other than the offered one**
    - Generator: `genRole` including invalid strings × `genEmailVariant` for the invited/authenticated pair;
      oracle is normalized equality and the offered role, which is never `owner`
    - **Validates: Requirements 10.3, 10.4, 10.5, 11.4, 11.6, 11.7, 11.8, 11.12, 11.13, 11.14**

  - [ ]* 11.10 Write example-based tests for the invitation surface
    - One test per role asserting the email names the workspace, inviter, role, plain-language permission
      summary, and expiry date
    - Failing notifier: assert 202, `invitation_created_delivery_pending`, a retained pending row, and a
      successful resend
    - Decline with the token alone and no credential
    - Assert no TTL index exists on `invitations` and no delete handler is registered
    - _Requirements: 10.11, 10.16, 12.6, 12.10_


- [ ] 12. Multi-workspace membership and switching
  - [~] 12.1 Implement the workspace switch endpoint
    - Verify the JWT locally, resolve `(user_id, workspace A)` from `memberships.stytch_member_id`, then look
      for an active membership in the target workspace; absent one, return 404 with the active workspace
      unchanged
    - Call `ExchangeSession(session_token, targetOrgID)`; a Stytch failure returns `503
      identity_provider_unavailable` with the active workspace unchanged
    - Update `memberships[B].last_active_at` and `stytch_member_id`, cache the new context, and return the
      workspace, role, granted permissions, and new session JWT
    - The active workspace is **not** server-side session state — it is the `organization_id` inside the
      verified credential, and `last_active_at` exists solely to answer "which workspace at next sign-in"
    - Implements design *Sequence: switch the active workspace*
    - _Requirements: 15.7, 15.8, 15.9_

  - [~] 12.2 Implement post-authentication workspace selection
    - Exactly one active membership → that workspace becomes active
    - More than one → the most recently active workspace becomes active and the response carries the list for
      the frontend selector
    - Zero active memberships and zero pending invitations → stage `awaiting_workspace_name`
    - Zero active memberships and at least one pending invitation for that address → return those invitations
      alongside the create-a-workspace option
    - Apply the permissions of the membership belonging to the workspace named by the request and zero
      permissions from any other membership
    - _Requirements: 15.3, 15.4, 15.5, 15.6, 15.11_

  - [ ]* 12.3 Write property test for switching authority
    - **Property 15: Workspace switching yields only the target Workspace's authority**
    - Generator: `genMembershipMatrix` of 1–5 users × 1–5 workspaces × `genRole` plus a switch sequence, with
      session role claims drawn **adversarially**; oracle is `Grants` over the stored role, asserting the
      resolved role is invariant under arbitrary session or webhook role claims
    - **Validates: Requirements 15.3, 15.4, 15.7, 15.8, 15.9, 15.10, 15.11, 16.5, 16.6, 19.13**

- [ ] 13. Re-scope companies, vacancies, applications, and subscriptions to the workspace
  - [~] 13.1 Re-scope the `jobrequests` employer read paths
    - Change `employerVacancyFilter`, `HandleEmployerDashboard`, `ListJobVacanciesByOwner`,
      `ListCompaniesByOwner`, `GetCompanyForOwner`, `GetJobVacancyForOwner`, and `GetCompanyByName` to take a
      workspace scope instead of, or in addition to, `owner_id`, reading it through `workspaces.FromEcho(c)`
    - Include the active workspace identifier in the query filter on every workspace-scoped read
    - A document holding no `workspace_id` remains visible only to the `users` document named by its
      `owner_id`
    - Apply **zero** changes to `publicVacancyFilter` and `listAvailableJobVacanciesForWorker`, so published
      vacancies stay publicly listable independently of membership
    - _Requirements: 9.2, 9.5, 9.6, 9.7, 9.8_

  - [~] 13.2 Re-scope the `jobrequests` employer write paths
    - Change `UpdateJobVacancyForOwner`, `DeleteJobVacancyForOwner`, `resolveVacancyCompany`, and the company
      write handlers to set and filter on `workspace_id`
    - A read or write naming a document whose `workspace_id` differs from the active workspace applies zero
      modifications and responds 404 rather than 403, so existence is not disclosed
    - _Requirements: 9.1, 9.3, 9.4_

  - [~] 13.3 Scope applications and the notification fan-out
    - `HandleCreateJobApplication` sets `workspace_id` from the target vacancy
    - Notify every member holding `application.view` instead of `vacancy.OwnerID` alone
    - Disclose exactly the applicant fields the existing review flow already discloses, with zero additional
      disclosure as a consequence of role
    - Apply the existing application limits and subscription checks against the artisan's own `users` document
    - _Requirements: 9.9, 9.10, 22.8, 22.9_

  - [~] 13.4 Add `workspace_id` to quotation attachments
    - Set `workspace_id` on `quotation_attachments` documents at creation
    - _Requirements: 9.1_

  - [~] 13.5 Move the employer route surface onto `ScopedGroup`
    - Re-register every row of the design's workspace-scoped route table through `ScopedGroup` with its
      declared permission, keeping the legacy employer URLs unchanged for frontend compatibility
    - Register the three session-scoped `/workspaces` routes on the separate `Authenticate`-only group
    - Leave artisan and shared endpoints on `authGroup`, and leave every `middleware.IsAdmin` route untouched
    - Register the onboarding group with its seven paths and the unauthenticated group with its eight paths,
      with `/stytch/webhooks` registered before any body-consuming middleware
    - _Requirements: 8.5, 8.12, 22.5, 22.7_

  - [ ]* 13.6 Write property test for tenancy isolation
    - **Property 16: Tenancy isolation holds for every generated access pattern**
    - Generator: two workspaces each populated with 0–10 documents across 5 collections, then the full cross
      product of (route, foreign id); oracle is 404 and zero modification, plus the unscoped-document
      visibility rule. Requires a real test database
    - **Validates: Requirements 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 9.9, 23.5, 24.8**

  - [ ]* 13.7 Write regression tests for the untouched artisan surface
    - Assert `go test ./...` is green with **zero edits** to pre-existing assertions in `auth`, `users`,
      `jobrequests`, and `subscriptions`
    - Assert `POST /api/v1/register` and `POST /api/v1/login` keep their current request and response shapes,
      that unexpired `jwtutil` tokens are still accepted, and that the artisan OTP, password reset, and social
      login flows are unchanged
    - One test per `IsAdmin` route asserting unchanged behaviour
    - Both documents functional for an artisan/employer email collision, with the employer path returning
      `email_belongs_to_non_employer` for the artisan candidate
    - _Requirements: 8.12, 22.1, 22.2, 22.3, 22.4, 22.6, 22.7, 9.8_

- [ ] 14. Scope billing to the workspace
  - [~] 14.1 Add `WorkspaceID` to `user_subscriptions` and scope entitlement reads
    - `UserSubscription` gains `WorkspaceID`; `GetActiveSubscriptions` and `HasEntitlement` accept a workspace
      scope with the individual as the documented fallback when `workspace_id` is absent
    - Apply every workspace entitlement derived from a subscription to every active membership
    - _Requirements: 24.1, 24.3, 24.10_

  - [~] 14.2 Scope subscription purchase, cancellation, and history to the workspace
    - A `subscription.manage` holder's purchase or cancellation applies to the active workspace rather than to
      the calling user
    - `billing.view` reads return every invoice, payment, and subscription belonging to that workspace and zero
      records belonging to any other
    - Retain the subscription unchanged when a membership is removed; retain the recorded payment method when
      the paying membership is removed and notify every `billing.manage` holder that the paying member has left
    - Notify every `billing.manage` holder when a workspace subscription lapses and apply the existing
      lapsed-subscription restrictions at workspace scope
    - _Requirements: 24.2, 24.4, 24.5, 24.8, 24.11_

  - [~] 14.3 Implement the invoice endpoints and workspace-name snapshot
    - `GET /invoices` (`invoice.view`), `POST /invoices/:id/pay` (`invoice.pay`), `GET /payments`
      (`payout.view`), and `POST /payment-methods` (`payment_method.manage`)
    - Name the workspace on every invoice generated for a workspace, snapshotted at issue
    - Write one audit entry on a billing payment method change
    - The Admin role reaches the two read routes (`invoice.view`, `payout.view`) and is denied the two write
      routes; the HR role is denied every billing endpoint and the Finance role is denied every vacancy write
      and application read — all three purely as a consequence of the matrix, with no separate check
    - _Requirements: 7.4, 7.9, 24.6, 24.7, 24.9, 23.1_

- [ ] 15. Workspace deletion lifecycle
  - [~] 15.1 Implement `ScheduleDeletion` with its blockers and confirmation
    - Require a confirmation value equal to the workspace name, responding 400 when it differs; reject any role
      other than `owner` with 403
    - Evaluate the blockers before scheduling: an `active` `user_subscriptions` row for the workspace yields
      `409 active_subscription`; a `transactions` row for the workspace in a non-terminal state yields `409
      outstanding_obligations`
    - Set the deletion state to `scheduled` with an effective time 7 days out, retain every workspace-scoped
      document, close every open `job_vacancies` document in the workspace, and email every active member
      naming the effective time and the cancelling procedure
    - _Requirements: 18.1, 18.2, 18.3, 18.5, 18.6, 18.11, 18.12_

  - [~] 15.2 Implement `CancelDeletion` and the scheduled-state write denial
    - Clear the deletion state and restore write access, reopening zero vacancies
    - While `scheduled`, the guard grants reads to every active member, grants the Owner cancellation, and
      denies every other workspace-scoped write with `409 workspace_deletion_scheduled` carrying
      `details.effective_at`
    - Create zero invitations for a workspace whose deletion is scheduled or complete
    - _Requirements: 18.4, 18.7, 10.18, 11.14_

  - [~] 15.3 Implement `EffectDueDeletions`
    - At the effective time set the state to `deleted`, set every membership in the workspace to `removed`, and
      delete the Stytch organization, treating `KindNotFound` as success because a webhook may already have
      reported it deleted
    - Retain every `job_applications`, `transactions`, `invoices`, and `user_subscriptions` document belonging
      to the workspace; retain a `users` document holding zero memberships and let that person create a new
      workspace
    - Remove the workspace name, the slug, and every company name in the workspace from public listings and
      search results within 24 hours
    - Write one audit entry on every deletion-state change naming the acting user, the previous state, and the
      new state
    - Re-running converges — the sequence is ordered and idempotent with no transaction
    - _Requirements: 18.8, 18.9, 18.10, 18.13, 18.14_

  - [ ]* 15.4 Write example-based tests for the deletion flow
    - Wrong confirmation → 400; correct → scheduled 7 days out with member emails sent
    - Assert an active subscription and an unsettled obligation each block scheduling with the named code
    - Assert a `KindNotFound` organization delete at the effective time is treated as success
    - _Requirements: 18.1, 18.3, 18.6, 18.11, 18.12; design Open Question 10_

- [ ] 16. Stytch webhook consumer
  - [~] 16.1 Implement `Handle` with raw-body reading, verification, and the processed-event insert
    - Read the raw body once through `io.LimitReader` into a buffer, before any body-consuming middleware
    - Verify the signature; an invalid signature is 401 with one warning naming the source IP and zero state
      change; a timestamp skew over 5 minutes is 400 with zero state change
    - Insert `processed_webhook_events {_id: svix-id}` **before** processing; a duplicate key returns 200 with
      zero additional state change; a failure after verification deletes the row and returns 500 so Stytch
      retries with the identifier left unrecorded
    - Deleting on failure relies on this deduplication record (the event identifier) as its ONLY
      idempotency guard, not on every handler being independently idempotent — verify this holds for each
      event type in task 16.2, in particular `member.delete` on an owner target, which recreates the
      Stytch member: recreating a Stytch member is not naturally idempotent, so a retry after a partial
      failure must not create a second Stytch member. If a handler's operations cannot be made safe under
      at-least-once redelivery, the deduplication record is what must prevent the second attempt, not the
      handler's own logic
    - Add a bound to the retry: if a failure recurs for the same event past a small number of attempts
      (a `retry_count` field, or a separate dead-letter collection), stop deleting the row and instead
      acknowledge with 200 while alerting ops, rather than letting Stytch retry an unrecoverable failure
      forever with no visibility
    - Implements design *Sequence: webhook processing*
    - _Requirements: 19.1, 19.2, 19.3, 19.4, 19.5, 19.12_

  - [~] 16.2 Implement event classification and application through the services
    - `member.email.update` → `Link_Service.ApplyEmailChange`; `member.delete` with a non-owner target → set
      `removed` and `$inc authority_version`; `member.delete` with an owner target → retain the membership,
      recreate the Stytch member, and record one error-severity log naming the workspace;
      `organization.delete` → set `deleted` only where the Fundis state is already `scheduled` or `deleted`,
      else one error-severity log
    - Every conditional update carries `{"last_webhook_event_at": {"$lt": eventTS}}` so a late-arriving stale
      event matches zero documents instead of overwriting newer state
    - Write only through `Membership_Service` and `Workspace_Service`, never directly, so the
      "membership-document-is-authoritative" rule is enforced by the same code that enforces it for HTTP
    - An unknown event type is recorded as processed with one info record and a 200, so it never becomes a 500
      retry loop
    - Respond within 5 seconds, deferring work beyond that bound to a background queue
    - _Requirements: 19.6, 19.7, 19.8, 19.9, 19.10, 19.11, 19.13_

  - [ ]* 16.3 Write property test for webhook idempotence and order-insensitivity
    - **Property 20: Webhook processing is idempotent and order-insensitive**
    - Generator: `genEventSequence` of 1–20 events × `genDuplicateInsertion` × `genShuffle` of distinct
      timestamps; oracle is the final state of the deduplicated, in-order run. Requires a real test database
    - **Validates: Requirements 19.4, 19.5, 19.6, 19.7, 19.8, 19.9, 19.10, 19.12**

  - [ ]* 16.4 Write example-based test for webhook latency
    - A deliberately slow deferred handler; assert the response returns promptly and the remainder is enqueued
    - _Requirements: 19.11_

- [ ] 17. Audit logging
  - [~] 17.1 Implement `audit.Logger.Write` and the closed action set
    - `Write` returns **no error** to its caller: a failed write emits one error record and is dropped, so the
      signature — not a convention — enforces that the underlying action still completes
    - Declare the thirteen action constants and drive them through an exhaustive switch in the writer so an
      unlisted action fails the build's vet step
    - Record actor, workspace, action, target, IP, and UTC time on every entry, plus `actor_kind` of
      `member` / `platform_admin` / `system`
    - Record previous and new values only for actions that change a stored value
    - Redact `Previous` and `New` through the same key-substring rule `logging.Redactor` already uses, so no
      password hash or token lands in the collection
    - _Requirements: 3.8, 5.10, 12.12, 13.10, 14.10, 18.13, 23.1, 23.2, 23.7, 23.8, 23.9, 23.10_

  - [~] 17.2 Implement `audit.Logger.List` and the read endpoint
    - `GET /workspaces/:workspaceID/audit-log` requiring `audit.view`, reverse chronological with pagination
    - Return zero entries belonging to any other workspace
    - Provide no update and no delete method and register no route that modifies or deletes an entry, so
      append-only is structural; retain every entry with no TTL index
    - _Requirements: 23.3, 23.4, 23.5, 23.6_

  - [ ]* 17.3 Write property test for secret containment
    - **Property 28: Secrets never leave the process**
    - Generator: `genSecret` values injected into config, tokens, and payloads, then every code path driven;
      oracle is a substring search over every captured log line, response body, and audit document
    - **Validates: Requirements 10.17, 20.9, 20.12, 23.7**

  - [ ]* 17.4 Write example-based tests for audit retention and admin marking
    - Assert no TTL index exists on `audit_log_entries` and that no update or delete method exists
    - One test per administrator operation asserting `actor_kind: "platform_admin"`
    - _Requirements: 23.3, 23.6, 23.9_

- [ ] 18. Rate limiting
  - [x] 18.1 Implement the named-limit table, the fixed-window `Allow`, and the sliding-window `Allow`
    - Create `ratelimit/` with the named limits exactly as tabled (see the Requirement 25 rate-limit
      strategy table): `signup_ip` 5/10m, `signup_email` 3/10m, `link_challenge` 5/1h, `invite_create`
      20/1h, `invite_resend` 3/1h, `invite_resolve_m` 10/1m, `invite_resolve_h` 30/1h, `workspace_create`
      5/24h, plus `webhook_ip` for the public webhook endpoint's own DoS guard
    - `ratelimit.Limiter.Allow` is a fixed-window counter: `INCR` plus `EXPIRE`-on-first-increment in one
      pipeline, no Lua. The fixed window can admit up to 2×`Max` across a boundary
    - `ratelimit.SlidingWindowLimiter.Allow` is the limiter actually wired at startup (`main.go`): a Redis
      sorted-set implementation that satisfies the "per rolling N minutes" reading of Req 25.1/25.2/25.6,
      at roughly 3× the fixed-window's Redis cost. The fixed-window `Limiter` remains as the in-memory
      fallback for single-instance deployments or tests without Redis. Both implementations satisfy a
      shared `ratelimit.RateLimiter` interface so callers do not need a type-specific constructor
    - Store is Redis when `configs.RDB` is non-nil and an in-process map otherwise. This means a
      multi-instance deployment WITHOUT Redis gets per-instance rate limiting (each instance independently
      enforces the full limit, so N instances collectively admit up to N× the configured count) rather
      than a shared count. This is a silent degradation, not a hard failure — document it operationally,
      and treat Redis as required infrastructure for any multi-instance deployment of this feature
    - This task has no dependencies and is scheduled early in the dependency graph because tasks 6.2, 8.4,
      8.3, 11.2, 11.4, and 11.6 all call `Allow`
    - _Requirements: 5.5, 10.12, 11.18, 12.4, 15.13, 25.1, 25.2, 25.6, 25.9_

  - [~] 18.2 Implement the `Guard` middleware
    - Set `Retry-After` naming the remaining seconds on every rejection and emit one warning-severity record
      naming the endpoint, the limit reached, and the IP address
    - Wire `Guard` onto the unauthenticated signup and invitation routes with the correct subject extractors
    - Apply the existing Turnstile verification to both the signup initiation and the invitation acceptance
      endpoints
    - _Requirements: 25.3, 25.7, 25.8_

  - [ ]* 18.3 Write property test for rate limits
    - **Property 26: Rate limits never admit more than the stated count**
    - Generator: `genLimitName` × burst size 1–200 × a clock advance pattern; oracle is the `limits` table
      itself, with two limiter instances over one `miniredis` asserting the aggregate limit rather than twice
      it, and the `Retry-After` header asserted positive and no greater than the window. Also exercise the
      `configs.RDB == nil` fallback
    - **Validates: Requirements 5.5, 10.12, 10.13, 11.18, 12.4, 15.13, 25.1, 25.2, 25.3, 25.6, 25.8, 25.9**

- [ ] 19. Scheduler wiring and the four new jobs
  - [~] 19.1 Start `jobs.NewScheduler()` from `main.go` and add the faster ticker
    - `jobs.NewScheduler()` exists but has no live caller today; start it from `main.go`
    - Keep the hourly ticker and add a second, 15-minute ticker for the Stytch member-delete retry
    - **Prerequisite for tasks 19.2 through 19.5**
    - _Requirements: 3.7, 12.8, 13.4, 18.8_

  - [~] 19.2 Register the invitation expiry sweep
    - Run `SweepExpired` at least once every 24 hours, backed by `invitations_pending_expiry`
    - _Requirements: 12.8_

  - [~] 19.3 Register the abandoned-onboarding reaper
    - Hourly: find `onboarding_states` where `stage != "complete"`, `updated_at < now-72h`, and
      `abandoned_handled != true`; for each holding a `stytch_organization_id`, confirm zero `workspaces`
      reference it and ask Stytch for the member list, deleting the organization only when it holds no member
      beyond the creating one; write one audit entry per deletion with `actor_kind: "system"`; set
      `abandoned_handled: true` so a Stytch failure retries next hour without re-auditing
    - This makes the hourly job dependent on Stytch's availability: `abandoned_handled` is set only on
      success, so a Stytch outage means the reaper retries the same organization every hour indefinitely,
      with no backoff, no max-retry count, and no dead-letter signal. Add a bounded retry: track an
      attempt count per `onboarding_states` record, apply exponential backoff between attempts, and after
      a small number of failed attempts, flag the record for on-call review (a distinct state, or a
      dedicated alert) instead of retrying forever silently
    - _Requirements: 3.7, 3.8_

  - [~] 19.4 Register the workspace-deletion effector and the Stytch delete retry
    - Call `EffectDueDeletions` on the hourly ticker, driven by `workspaces_deletion_due`
    - Call `RetryPendingStytchDeletes` on the 15-minute ticker, driven by
      `memberships_stytch_delete_pending`, retrying for at least 24 hours
    - _Requirements: 13.4, 18.8_

  - [~] 19.5 Implement the orphan report endpoint
    - A read-only admin endpoint reporting three sets: Stytch organizations with no `workspaces` document,
      paged through `ListOrganizations`; `workspaces` documents whose `stytch_organization_id` returns
      `KindNotFound`; and workspaces with zero active owner memberships, whose non-emptiness is an
      error-severity event
    - _Requirements: 3.9, 14.15_

  - [ ]* 19.6 Write example-based test for the orphan report
    - Seed one orphan organization, one workspace whose organization is `KindNotFound`, and one workspace with
      zero active owners; assert all three appear
    - _Requirements: 3.9, 14.15_


- [ ] 20. Migration_Job
  - [~] 20.1 Implement `Candidates` and `DeriveWorkspaceName` as pure functions
    - `Candidates` applies the rule once, shared by the implementation and the property test: `user_type ==
      "customer"` **and** a non-empty email after normalization **and** at least one `companies` or
      `job_vacancies` document owned by that user **and** no existing Account_Link. Among colliding normalized
      emails the greatest `created_at` wins and the rest are reported
    - `DeriveWorkspaceName` uses the most recently created company's name, falling back to the first and last
      name joined by one space when the user owns zero companies
    - _Requirements: 21.3, 21.6, 21.7, 21.14, 21.15_

  - [~] 20.2 Implement `Run` driving the same `Workspace_Service` code path
    - Per candidate perform exactly the same calls as a live signup, in the same order, through the same
      `Workspace_Service` code — not a parallel implementation — which is what makes Req 21.2 the same property
      as P7 and why the job lives in `workspaces/`
    - Create one organization, one workspace, one owner membership, and one account link per candidate, then
      apply the Requirement 6 re-scoping
    - Batch at a configurable size (default 50) and throttle to at most 10 state-changing Stytch calls per
      second. This throttle is per-process: if the migration is ever run concurrently (multiple admins
      trigger it, or it runs on more than one instance), each process independently enforces 10/sec, so
      N concurrent runs collectively hit Stytch at up to 10×N calls/sec. Ensure only one migration run
      executes at a time (a leader-elected lock, or restrict triggering to a single admin operation) rather
      than relying on the per-process throttle alone to protect Stytch's own rate limits
    - A second run over an already-migrated employer creates zero additional records — check for an
      existing "migrated" finding for the candidate before attempting Stytch/Mongo writes, in both
      dry-run and real mode, and count such candidates as `skipped` rather than `migrated`
    - Email each migrated employer naming the new workspace name and the new sign-in method
    - _Requirements: 21.2, 21.4, 21.5, 21.9, 21.13_

  - [~] 20.3 Implement the dry-run mode and the report
    - A dry run applies zero writes to Stytch and zero writes to MongoDB **except** the two report
      collections, which is the one stated exemption because "zero writes" cannot literally exclude the report
      the same requirement demands
    - `employer_migration_runs` holds the summary; `employer_migration_findings` holds one document per
      candidate with `{run_id, user_id, outcome, reason, workspace_id, stytch_organization_id,
      workspace_name}` across the six outcomes
    - Report accounts requiring an email address and every normalized-email collision
    - _Requirements: 21.6, 21.7, 21.8, 21.16_

  - [~] 20.4 Implement the two admin migration routes
    - `POST /admin/employer-migration` and `GET /admin/employer-migration/:runID` behind `middleware.IsAdmin`
      rather than a new binary — the repository has no `cmd/` directory and no CLI convention
    - _Requirements: 21.8, 21.16_

  - [~] 20.5 Implement the dual-auth window and the cutover response
    - While `STYTCH_EMPLOYER_AUTH_ENABLED` is true, accept both a valid Stytch session and a valid `jwtutil`
      token for a migrated employer and resolve the `SessionContext` identically for both
    - When `LEGACY_EMPLOYER_AUTH_ENABLED` is false, respond to `POST /api/v1/login` naming a migrated employer
      with `409 use_stytch_sign_in` carrying `details.sign_in_url`
    - Reverting is the environment variable alone for migrated original employers, who keep their bcrypt
      hash — that restores their sign-in with zero data loss to their Fundis records. It is NOT a full
      undo: the Stytch_Organization, Workspace, Membership, and Account_Link records the migration created
      are left in place, unused, not deleted. A teammate whose Membership was created by an Invitation
      accepted after the migration ran never had a Fundis password, so they cannot sign in through legacy
      auth after a revert and require manual intervention (see Requirement 21 criterion 12's documented
      limitation)
    - _Requirements: 21.1, 21.10, 21.11, 21.12_

  - [ ]* 20.6 Write example-based tests for the migration
    - Assert the dry-run report shape and that every collection except the two report collections is
      byte-identical before and after, including the Stytch fake's call log
    - Spy asserts the migration email names the new workspace name and the new sign-in method
    - Feature-flag routing: flag off → legacy path, flag on → Stytch path, both for the same user
    - _Requirements: 21.1, 21.8, 21.12, 21.13_

- [ ] 21. Wire everything together in `main.go` and close the outage guarantees
  - [~] 21.1 Construct and inject every new component
    - Build `stytch.Adapter` from `configs.Stytch*()`, `audit.Logger`, `ratelimit.Limiter`, the `Resolver`, and
      the six domain services, then register the four route groups
    - Run, in order: the Stytch variable validation, `workspaces.VerifyMatrix()`, the transaction probe, every
      `EnsureIndexes`, route registration, `RouteRegistry.Verify`, and `jobs.NewScheduler()`
    - _Requirements: 8.6, 14.3, 20.11_

  - [~] 21.2 Implement `GET /health/identity`
    - Unauthenticated, no secrets, reporting `employer_stytch_auth_enabled`, `legacy_employer_auth_enabled`,
      `transactions_supported`, the JWKS snapshot, and `last_call` from the atomic pointer
    - _Requirements: 20.13_

  - [~] 21.3 Implement the outage degradation branches
    - A read of workspace data is served on a locally verified session JWT even while Stytch is unreachable
    - A new signup returns `503 identity_provider_unavailable` with zero partial `users` documents
    - An invitation acceptance returns 503 with the invitation left pending and zero memberships
    - A switch returns 503 with the active workspace unchanged
    - A membership write applies the Fundis write anyway and flags the Stytch mirror for retry
    - Every code in the error catalogue is emitted through the existing `responses.SendErrorWithCode`, so no
      new response envelope is introduced
    - Implements design *Stytch outage degradation ladder*, *Machine-readable code catalogue*
    - _Requirements: 20.4, 20.5, 20.7, 20.8_

  - [ ]* 21.4 Write property test for partial-account prevention
    - **Property 30: Stytch failure never leaves a partial account**
    - Generator: `genCallSite` × `FailSpec` across signup, invitation acceptance, and workspace switching;
      oracle is the two-outcome disjunction with the `users` count unchanged for a rejected signup and the
      response carrying `503 identity_provider_unavailable`. Requires a real test database
    - **Validates: Requirements 3.5, 11.9, 11.10, 20.1, 20.2, 20.3, 20.4, 20.5, 20.7, 20.8**

- [~] 22. Checkpoint - Ensure all tests pass
  - Run the full suite with `-race`, confirming the five concurrency properties (P7, P12, P14, P19, P22) pass
  - Confirm all 30 property tests run at a minimum of 100 generated inputs each
  - Confirm the suite is green on a machine with no Stytch credentials, with the two round-trip tests skipped
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 23. Frontend: employer onboarding, workspaces, and permission-gated navigation
  - [~] 23.1 Add the Stytch dependency, feature flag, and workspace types
    - Add `@stytch/vanilla-js` and `@stytch/react` (B2B entrypoints) to `package.json`
    - Add `VITE_STYTCH_PUBLIC_TOKEN` and `VITE_EMPLOYER_STYTCH_AUTH=false` to `.env.example`, and
      `employerStytchAuth` to `src/config/features.ts` matching the existing `FEATURES.requests` pattern
    - Create `src/types/workspace.ts` with `WorkspaceRole`, the `Permission` string union transcribed from the
      backend's 26 constants, `MembershipStatus`, `InvitationStatus`, `WorkspaceSummary`, `MemberView`,
      `InvitationView`, `AuditEntry`, and `OnboardingStage`; add `linked_email` and `link_method` to `User`
    - _Requirements: 5.15, 7.16_

  - [~] 23.2 Create the five API modules
    - `src/api/employerAuth.ts`, `workspaces.ts`, `members.ts`, `invitations.ts`, and `audit.ts` with exactly
      the signatures the design declares
    - _Requirements: 7.16, 15.10, 16.10_

  - [~] 23.3 Create `workspaceStore` and update the axios interceptors
    - `src/store/workspaceStore.ts` holding `workspaces`, `activeWorkspaceId`, `role`, `permissions`,
      `switchWorkspace`, `can`, and `hydrate`; `permissions` is always the array the server returned and is
      never recomputed client-side, so the UI cannot drift into showing a control the API will reject
    - One bearer token storage key and one interceptor: the Stytch session JWT for employers on the Stytch
      path, the `jwtutil` token for everyone else
    - A response interceptor branching on `identity_provider_unavailable` (retryable-outage banner, **do not
      sign out**), `unlinked_member` (sign out and route to `/employer/sign-up`),
      `workspace_deletion_scheduled` (read-only banner with the effective date), `permission_denied` (inline
      message using `details.missing_permission`), and `use_stytch_sign_in` (route with a notice)
    - The existing 401 handler must not treat a 503 outage as a session expiry — it currently clears storage
      and hard-navigates on any 401 with a token present, which would log every employer out during a Stytch
      outage
    - _Requirements: 16.10, 18.4, 20.4, 21.11_

  - [~] 23.4 Build the onboarding and invitation screens
    - `EmployerSignUpPage` (Turnstile-gated, identical copy regardless of whether the address exists),
      `StytchCallbackPage` (redeems the `token` query parameter and branches on the returned `stage`),
      `OnboardingNamePage` (pre-filled from Stytch), `OnboardingWorkspacePage` (2–80 characters with the derived
      slug shown live and inline `duplicate_workspace_name` handling), `LinkAccountPage` (masked candidates with
      "This is me" / "Create a separate account" and the phone-OTP challenge)
    - `InvitationLandingPage` is **public**, showing workspace, inviter, role, and a plain-language permission
      summary with Accept / Decline, and distinct copy for the 404 and 410 codes; it offers sign-out and
      re-authentication when the authenticated address differs from the invited one
    - _Requirements: 1.15, 2.12, 4.2, 5.3, 11.1, 11.2, 11.5, 11.12, 11.13, 25.4_

  - [~] 23.5 Build the workspace selector and the settings screens
    - Add the workspace selector to `DashboardLayout`, `MenuBar`, and `Sidebar` — a dropdown with role badges
      rendered only when more than one active membership exists, plus a "Create workspace" entry; filter nav
      items by the permission set so an HR user never sees Billing and a Finance user never sees Vacancies
    - `MembersPage` (member list, invite form, role change, suspend, remove, pending invitations),
      `WorkspaceSettingsPage` (rename, ownership transfer, deletion scheduling with name confirmation,
      cancellation), and `AuditLogPage` (paginated, reverse-chronological, rendered only for `audit.view`)
    - Present pending invitations alongside the create-a-workspace option for a user with zero memberships
    - _Requirements: 15.4, 15.6, 15.10, 15.12, 18.1, 23.4, 24.6, 24.7_

  - [~] 23.6 Update the existing employer screens
    - `HomePage` links the employer tab to `/employer/sign-up` when the flag is on, leaving the artisan tab
      untouched; `BillingPage` shows the workspace name and gates on `billing.manage` / `subscription.manage`;
      `InvoicesPage` reads the new workspace invoices endpoint gated on `invoice.view`; `VacanciesPage`,
      `CreateVacancyPage`, `VacancyApplicationsPage`, `CompanyDetailsPage`, and `Dashboard` keep their
      contracts and only disable controls the permission set does not grant; `ChangePasswordPage` is hidden for
      employers holding an Account_Link
    - _Requirements: 21.1, 24.2, 24.6, 24.7_

  - [ ]* 23.7 Write frontend tests
    - The workspace selector renders with more than one active membership and is absent at exactly one
    - The sign-out-and-retry prompt appears on `invitation_email_mismatch`
    - A contract test fetching `/workspaces/:id/permissions` and asserting the returned names are a subset of
      the `Permission` union, so a backend addition the UI has not heard of fails loudly
    - _Requirements: 11.5, 15.4, 7.16_

- [ ]* 24. Optional: the design's deferred items
  - [ ]* 24.1 Move the Session_Resolver context cache to a shared store
    - Deferred item 3. The current 30-second bound already satisfies Req 17.8, so this is an optimization that
      cuts the per-instance Mongo read to one across the fleet, not a correctness fix
    - _Requirements: 16.7, 17.8_

  - [ ]* 24.2 Implement a sliding-window rate limiter
    - Deferred item 5, conditional on design Open Question 7 being resolved that way. A sorted-set
      implementation at roughly 3× the Redis cost
    - _Requirements: 25.1, 25.2, 25.6_

  - [ ]* 24.3 Convert the remaining `slog.Error` call sites in the new packages to the `Context` variants
    - Deferred item 4. The new packages already use `slog.*Context` throughout; this is the mechanical pass
      over the pre-existing call sites so every record carries `request.id` and `workspace.id`
    - _Requirements: 16.11_

  - [ ]* 24.4 Implement invitation and audit retention purges
    - Deferred item 7. Req 12.10 and 23.6 set minimums (180 days, 2 years) and the design never deletes, so
      no purge exists yet; a retention job waits on a policy setting the maximum
    - _Requirements: 12.10, 23.6_

- [~] 25. Final checkpoint - Ensure all tests pass
  - Run `go build ./...`, `go vet ./...`, `go test ./... -race`, and the frontend test suite
  - Confirm every one of the 30 properties has a corresponding test and every requirement in the traceability
    table is covered by at least one task above
  - Confirm the two non-component criteria hold as regression assertions: Req 9.8 (`publicVacancyFilter` and
    `listAvailableJobVacanciesForWorker` unchanged) and Req 22.4 (the artisan OTP, password reset, and social
    login paths in `users/routes.go` and `auth/routes.go` unchanged)
  - Ensure all tests pass, ask the user if questions arise.

## Notes

- Tasks marked with `*` are optional and can be skipped for a faster MVP. They are test sub-tasks and the four
  deferred items of task 24; no core implementation sub-task is marked optional.
- **Four hard sequencing constraints**, restated because violating any of them breaks the feature rather than
  merely reordering work: task 2.1 (`uniq_phone` becomes partial) must land before task 6.3 creates a
  phone-less employer; task 19.1 (start the scheduler) must land before tasks 19.2–19.4; task 1.3 (the
  transaction probe) must land before task 10.4 (`TransferOwnership`); and tasks 1.4 and 3.6 (`rapid` and
  `stytch.Fake`) must land before any property test.
- The `invoices` collection is **created** by task 5.4. `external_invoices` is the artisan→customer invoice
  keyed by `artisan_id` and is not repurposed.
- All 30 correctness properties are covered: P1 (4.3), P2 (4.4), P3 (6.6), P4 and P6 (8.7), P5 (8.8),
  P7 (8.9), P8 (8.10), P9 (10.8), P10 (10.9), P11 (11.7), P12 (11.8), P13 (11.9), P14 and P18 (10.10),
  P15 (12.3), P16 (13.6), P17 (7.7), P19 (7.8), P20 (16.3), P21 (3.8), P22 and P23 (8.11), P24 (8.13),
  P25 (8.12), P26 (18.3), P27 (6.7), P28 (17.3), P29 (4.7), P30 (21.4).
- Every property test runs at a minimum of 100 generated inputs, uses `stytch.Fake` rather than Stytch itself,
  and carries the `// Feature: stytch-employer-onboarding, Property N: <title>` tag with its
  `**Validates:**` line, per the design's testing standard.
- The sixteen Mongo-marked properties use a real test database with an `APP_ENV`-derived name; every other
  property uses in-memory repositories behind the service interfaces.
- Checkpoints at tasks 9, 22, and 25 bound the three integration risks: the identity and provisioning core,
  the full backend surface, and the frontend wiring.

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1", "1.3", "1.4", "2.1", "3.1", "4.1", "18.1", "23.1"] },
    { "id": 1, "tasks": ["1.2", "1.5", "2.2", "2.3", "3.2", "3.4", "4.2", "4.5", "18.2", "23.2"] },
    { "id": 2, "tasks": ["2.4", "3.3", "3.5", "4.3", "4.4", "4.6", "5.1", "18.3", "23.3"] },
    { "id": 3, "tasks": ["3.6", "3.8", "4.7", "5.2", "5.3", "5.4", "6.1", "8.1"] },
    { "id": 4, "tasks": ["3.7", "3.9", "5.5", "6.2", "7.1", "8.7", "11.1", "17.1"] },
    { "id": 5, "tasks": ["6.3", "6.6", "7.2", "8.4", "10.1", "11.2", "17.2", "20.1"] },
    { "id": 6, "tasks": ["6.4", "6.7", "7.3", "8.2", "8.5", "10.2", "11.3", "17.3"] },
    { "id": 7, "tasks": ["6.5", "6.8", "7.4", "8.3", "8.6", "10.3", "11.4", "17.4"] },
    { "id": 8, "tasks": ["7.5", "7.6", "8.8", "8.9", "8.11", "10.4", "11.5", "12.1"] },
    { "id": 9, "tasks": ["7.7", "8.10", "8.12", "8.13", "10.5", "10.6", "11.6", "12.2"] },
    { "id": 10, "tasks": ["7.8", "8.14", "10.7", "10.8", "11.7", "11.8", "12.3", "16.1"] },
    { "id": 11, "tasks": ["10.9", "10.10", "11.9", "11.10", "13.1", "16.2", "19.1"] },
    { "id": 12, "tasks": ["10.11", "13.2", "13.3", "13.4", "16.3", "16.4", "19.2", "19.3"] },
    { "id": 13, "tasks": ["13.5", "14.1", "15.1", "19.4", "19.5", "20.2"] },
    { "id": 14, "tasks": ["13.6", "13.7", "14.2", "15.2", "19.6", "20.3"] },
    { "id": 15, "tasks": ["14.3", "15.3", "15.4", "20.4", "20.5", "21.1"] },
    { "id": 16, "tasks": ["20.6", "21.2", "21.3", "23.4", "23.5"] },
    { "id": 17, "tasks": ["21.4", "23.6", "24.1", "24.2", "24.3", "24.4"] },
    { "id": 18, "tasks": ["23.7"] }
  ]
}
```
