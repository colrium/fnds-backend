# Requirements Document

## Introduction

Today an "employer" on Fundis is simply a `users` document with `user_type: "customer"` that happens to post job vacancies. Authentication is entirely home-grown: `auth/routes.go` registers users with a bcrypt password (cost 14) plus an SMS OTP, `jwtutil` mints a 24-hour HS256 JWT whose only claim is `user_id`, and `middleware.JWTAuth` loads the user on every request. Authorization is a flat `user_type` check (`middleware.IsAdmin`, `IsCustomer`, `IsArtisan`, `IsAssessor`), and every employer-owned resource — `companies`, `job_vacancies`, `user_subscriptions`, quotation attachments — is scoped by a single `owner_id` field pointing at one individual user. There is no notion of a company account with several people in it: if two people at the same business need access, they share one login.

This feature replaces employer account creation with Stytch and introduces a first-class multi-user tenant, the **Workspace**. A new employer authenticates through Stytch, names a default Workspace, and becomes its Owner. If the email presented to Stytch already exists in the Fundis `users` collection, the person is taken through a verified linking flow instead of getting a duplicate account. The Owner can then invite teammates into one of three additional roles — HR (jobs and applications), Admin (member and workspace administration plus read-only visibility of invoices and payouts, excluding workspace deletion and anything touching the Owner), and Finance (billing, invoices, payouts) — each governed by an explicit permission matrix.

Four properties shape the whole feature:

1. **Stytch owns identity; Fundis owns authority.** Stytch is the source of truth for "who is this person and have they proved control of this email". The Fundis database is the source of truth for "what may this person do in this Workspace". Every permission decision is made from Fundis data so that an outage or a misconfigured Stytch RBAC policy can never silently escalate privilege.
2. **No orphans, no duplicates.** Stytch and MongoDB are two separate systems with no shared transaction. Every write that spans both must be resumable and idempotent, so a crash mid-signup leaves a state that the next request can finish rather than a half-created employer or a second Workspace.
3. **A Workspace always has exactly one Owner.** Ownership is the only role that can delete the Workspace or transfer ownership, and the system must make it impossible to reach a state where a Workspace has zero Owners.
4. **Existing users are not disrupted.** Artisans, customers who only hire fundis, assessors, and admins keep the current authentication flow. The migration to Stytch runs behind a flag, is reversible, and never invalidates a live artisan session.

The scope is the Fundis backend (`/projects/sandbox/backend`) and the corresponding employer surfaces in the Fundis frontend. This document defines behavior only; component decomposition, collection schemas, and Stytch API call sequences belong to the design.

## Scoping Decisions

The following decisions are assumed by the requirements below. Each one is a genuine fork in the road, so each is called out explicitly for confirmation or override before design begins. Changing any of these changes the requirements that cite it.

- **D1 — Stytch product: B2B (multi-tenant) Authentication, not Consumer.** A Workspace maps one-to-one onto a Stytch Organization, and a Membership maps onto a Stytch Member. This is assumed because Organizations, Members, Member Sessions, organization-scoped invitations, and RBAC are exactly the primitives this feature needs, and rebuilding them on top of Stytch Consumer would mean hand-rolling tenancy. Consequence: employer identity is email-first, whereas Fundis identity is currently phone-first (see Requirement 1 criterion 12 and Requirement 4 criterion 9).
- **D2 — Exactly one Owner per Workspace.** Co-owners are not supported; ownership moves by explicit transfer (Requirement 14). The alternative — a set of Owners with a "last Owner cannot leave" rule — is simpler for customers with two founders but makes "who can delete the Workspace" ambiguous. Admin exists to absorb most of the day-to-day work an Owner would otherwise hoard.
- **D3 — Only employers move to Stytch in this feature.** Artisans (`user_type: "artisan"`), assessors, platform admins, and hiring customers who never create a Workspace keep the existing password/OTP/JWT flow (Requirement 22). A later spec may migrate them.
- **D4 — The Workspace is the billing boundary.** Subscriptions, invoices, and payment methods belong to the Workspace, not to the individual who bought them (Requirement 24). This is what makes the Finance role meaningful.
- **D5 — A Workspace contains many Companies.** The existing `companies` collection is retained and re-scoped from `owner_id` to `workspace_id`, so one Workspace can post vacancies for several company brands. The Workspace name and a Company name are independent values.
- **D6 — Domain-based auto-join is disabled.** Stytch email JIT provisioning and allowed-domain auto-join are switched off for every Workspace, because Kenyan SMEs overwhelmingly sign up with `gmail.com` addresses and domain auto-join would let strangers into each other's Workspaces. Membership is by invitation only (Requirement 10 criterion 14).

## Glossary

- **Fundis_Backend**: The Go/Echo API process defined by `main.go` in this repository.
- **Fundis_Frontend**: The React/Vite employer-facing web application in the sibling `frontend` repository.
- **Stytch**: The Stytch B2B (multi-tenant) Authentication service, accessed over the Stytch HTTP API.
- **Stytch_Organization**: A tenant record inside Stytch, holding Stytch_Members and that tenant's authentication settings.
- **Stytch_Member**: A person's identity inside one Stytch_Organization, identified by a `member_id` and an email address.
- **Stytch_Session**: A Stytch-issued member session, represented by a session token or session JWT that names both the `member_id` and the `organization_id`.
- **Stytch_Adapter**: The Fundis_Backend component that owns every outbound call to Stytch, including retries, timeouts, and error classification.
- **Employer**: A person who uses Fundis to advertise work and hire artisans, as distinct from an artisan who performs work.
- **Workspace**: The Fundis tenant record that owns Employer data. Each Workspace corresponds to exactly one Stytch_Organization.
- **Workspace_Name**: The human-readable Workspace label supplied by the Employer during onboarding.
- **Workspace_Slug**: The URL-safe, globally unique identifier derived from the Workspace_Name.
- **Default_Workspace**: The Workspace created during an Employer's own signup, as opposed to a Workspace joined by invitation.
- **Company**: An existing `companies` record describing a hiring brand, re-scoped by this feature from an individual owner to a Workspace.
- **Membership**: The record binding one Fundis user to one Workspace with exactly one Workspace_Role and one Membership_Status, and carrying that user's Stytch_Member identifier for that Workspace.
- **Workspace_Role**: One of exactly four values: `owner`, `admin`, `hr`, `finance`.
- **Owner**: The holder of the `owner` Workspace_Role, of which each Workspace has exactly one.
- **Membership_Status**: One of exactly three values: `active`, `suspended`, `removed`.
- **Permission**: A named capability such as `vacancy.create` or `billing.manage`, checked before a workspace-scoped action proceeds.
- **Permission_Matrix**: The static, code-resident mapping from each Workspace_Role to the set of Permissions that Workspace_Role grants.
- **Permission_Guard**: The Fundis_Backend middleware that resolves the Workspace_Session_Context and enforces a required Permission before a handler runs.
- **Workspace_Session_Context**: The per-request tuple of authenticated user identifier, active Workspace identifier, Workspace_Role, and granted Permission set.
- **Onboarding_Service**: The Fundis_Backend component that drives Employer signup from first Stytch authentication to a usable Workspace.
- **Onboarding_State**: The persisted progress record for one in-flight Employer signup, holding the values needed to resume after an interruption.
- **Link_Candidate**: A pre-existing Fundis `users` document whose stored email matches, after Email_Normalization, the email of an authenticating Stytch_Member.
- **Account_Link**: The persisted primary association between one Fundis `users` document and one Stytch_Member, namely the Stytch_Member in the Workspace where the association was established.
- **Link_Challenge**: The verification step an Employer completes to prove control of a Link_Candidate before an Account_Link is created.
- **Email_Normalization**: Removal of leading and trailing whitespace followed by conversion to lowercase, applied to an email address before comparison or storage.
- **Invitation**: A record offering a named email address a specified Workspace_Role in a specified Workspace.
- **Invitation_Status**: One of exactly five values: `pending`, `accepted`, `declined`, `revoked`, `expired`.
- **Invitation_Token**: The single-use, high-entropy secret that identifies one Invitation in an acceptance link.
- **Invitation_Service**: The Fundis_Backend component that issues, delivers, and resolves Invitations.
- **Membership_Service**: The Fundis_Backend component that creates, modifies, and revokes Memberships.
- **Workspace_Service**: The Fundis_Backend component that creates, renames, and deletes Workspaces.
- **Session_Resolver**: The Fundis_Backend component that converts an inbound credential into a Workspace_Session_Context.
- **Authority_Version**: A monotonically increasing integer stored on a Membership and incremented whenever that Membership's Workspace_Role or Membership_Status changes.
- **Webhook_Consumer**: The Fundis_Backend HTTP endpoint that receives and processes Stytch webhook deliveries.
- **Stytch_Webhook_Event**: One webhook delivery from Stytch, carrying an event identifier and a signature.
- **Audit_Logger**: The Fundis_Backend component that records Audit_Log_Entry values for privileged actions.
- **Audit_Log_Entry**: An append-only record of one privileged action, naming the actor, the Workspace, the action, the target, and the time.
- **Legacy_Auth**: The existing Fundis authentication flow comprising `POST /api/v1/register`, `POST /api/v1/login`, bcrypt password verification, SMS OTP, and `jwtutil` HS256 tokens.
- **Legacy_Employer**: A `users` document with `user_type: "customer"` that owns at least one `companies` or `job_vacancies` record and was created before this feature shipped.
- **Migration_Job**: The batch process that provisions Stytch_Organizations and Workspaces for Legacy_Employers.
- **Billing_Service**: The existing `subscriptions`, `payments`, and `transactions` packages taken together.
- **Rate_Limiter**: The Fundis_Backend component that bounds the number of attempts a caller may make against a named onboarding or invitation operation within a stated time window.

## Requirements

### Requirement 1: Create Employer Accounts Through Stytch

**User Story:** As a new Employer, I want to create a Fundis account using an email magic link or my Google account, so that I can start hiring without inventing and remembering another password.

#### Acceptance Criteria

1. WHEN an unauthenticated visitor submits an email address to the Employer signup endpoint, THE Onboarding_Service SHALL apply Email_Normalization to that address and SHALL request a Stytch email magic link for the normalized address.
2. WHEN an unauthenticated visitor chooses Google as the signup method, THE Onboarding_Service SHALL start the Stytch OAuth discovery flow for Google and SHALL treat the email address returned by Stytch as the Employer's email address after Email_Normalization.
3. WHEN Stytch reports a successful authentication for an email address that has zero Account_Link records and zero Link_Candidates, THE Onboarding_Service SHALL create an Onboarding_State record for that authentication and SHALL respond with a status indicating that a Workspace_Name is required.
4. WHILE an Onboarding_State record is in the `awaiting_workspace_name` stage, THE Permission_Guard SHALL reject every request to a workspace-scoped endpoint with HTTP status 403 and SHALL grant access to the Workspace_Name submission endpoint, the Account_Link endpoints, and the sign-out endpoint.
5. IF a visitor submits an email address that fails RFC 5322 address parsing, THEN THE Onboarding_Service SHALL respond with HTTP status 400 and a message naming the `email` field, and SHALL make zero calls to Stytch.
6. THE Onboarding_Service SHALL treat an email address as verified only when Stytch reports the address as verified for the authenticating Stytch_Member.
7. IF Stytch reports an authentication whose email address is not verified, THEN THE Onboarding_Service SHALL create zero Workspaces, create zero Memberships, and respond with HTTP status 403 and a message stating that email verification is required.
8. THE Onboarding_Service SHALL support exactly the following Employer signup methods at launch: Stytch email magic link and Stytch Google OAuth.
9. WHERE the Employer signup request carries a Cloudflare Turnstile token, THE Onboarding_Service SHALL verify that token with the existing `turnstile` package before calling Stytch, using the action name `employer_signup`.
10. IF Turnstile verification is rejected, THEN THE Onboarding_Service SHALL respond with HTTP status 400 and SHALL make zero calls to Stytch.
11. WHEN the Onboarding_Service creates a Fundis `users` document for a new Employer, THE Onboarding_Service SHALL set `user_type` to `customer`, set `status` to `active`, set `is_active` to `true`, and store zero password bytes.
12. WHEN the Onboarding_Service creates a Fundis `users` document for a new Employer who supplied no phone number, THE Onboarding_Service SHALL NOT store a phone number, so that phone uniqueness constraints do not apply to Employers without phone numbers.
13. WHEN the Onboarding_Service creates a Fundis `users` document for a new Employer, THE Onboarding_Service SHALL store the email address in Email_Normalization form.
14. WHERE an Employer supplies a phone number during or after onboarding, THE Onboarding_Service SHALL apply the existing phone uniqueness rule and SHALL respond with HTTP status 409 and a message naming the `phone` field when that phone number already belongs to another `users` document.
15. THE Onboarding_Service SHALL collect a first name and a last name for every new Employer before the Onboarding_State record reaches the `complete` stage, and SHALL use the name reported by Stytch as the pre-filled value when Stytch reports a name.
16. WHEN an Employer completes signup, THE Onboarding_Service SHALL record the signup method, the Stytch `member_id`, and the Stytch `organization_id` on the Fundis records that this feature creates.

### Requirement 2: Name And Provision The Default Workspace

**User Story:** As a new Employer, I want to name my workspace during signup, so that my team recognizes the account and my company brand appears on the vacancies I post.

#### Acceptance Criteria

1. WHEN an Employer whose Onboarding_State record is in the `awaiting_workspace_name` stage submits a Workspace_Name, THE Workspace_Service SHALL create one Stytch_Organization and one Workspace, and SHALL create one Membership binding that Employer to that Workspace with Workspace_Role `owner` and Membership_Status `active`.
2. THE Workspace_Service SHALL accept a Workspace_Name that contains between 2 and 80 characters inclusive after leading and trailing whitespace removal.
3. IF a submitted Workspace_Name contains fewer than 2 characters after leading and trailing whitespace removal, THEN THE Workspace_Service SHALL respond with HTTP status 400 and a message naming the `workspace_name` field, and SHALL create zero Stytch_Organizations.
4. IF a submitted Workspace_Name contains more than 80 characters after leading and trailing whitespace removal, THEN THE Workspace_Service SHALL respond with HTTP status 400 and a message naming the `workspace_name` field, and SHALL create zero Stytch_Organizations.
5. IF a submitted Workspace_Name contains zero characters in the Unicode letter or number categories, THEN THE Workspace_Service SHALL respond with HTTP status 400 and a message naming the `workspace_name` field.
6. THE Workspace_Service SHALL store the Workspace_Name with leading and trailing whitespace removed and with each run of consecutive whitespace characters replaced by one space character.
7. THE Workspace_Service SHALL derive a candidate Workspace_Slug from the Workspace_Name by converting to lowercase, replacing each run of characters outside the set of lowercase letters, digits, and hyphen with one hyphen, and removing leading and trailing hyphens.
8. WHEN a derived candidate Workspace_Slug is already held by another Workspace, THE Workspace_Service SHALL append a hyphen followed by an integer, starting at 2 (the bare base slug itself is the implicit first attempt, so no Workspace_Slug ever ends in `-1`), and incrementing by 1 until an unheld Workspace_Slug is found, making at most 100 such attempts in total including the bare base slug.
9. IF a derived candidate Workspace_Slug contains zero characters after the derivation stated in criterion 7, THEN THE Workspace_Service SHALL use the string `workspace` as the base for the uniqueness attempts stated in criterion 8.
10. THE Workspace_Service SHALL enforce Workspace_Slug uniqueness through a unique database index rather than through a read-then-write check.
11. THE Workspace_Service SHALL permit two Workspaces to hold the same Workspace_Name while holding different Workspace_Slugs.
12. WHEN an Employer submits a Workspace_Name identical, after the normalization stated in criterion 6 and case-insensitive comparison, to the Workspace_Name of a Workspace in which that Employer already holds an `active` Membership, THE Workspace_Service SHALL respond with HTTP status 409, a machine-readable code of `duplicate_workspace_name`, and the identifier of the existing Workspace.
13. WHEN the Workspace_Service creates a Stytch_Organization, THE Stytch_Adapter SHALL set email JIT provisioning to the value that disables domain-based auto-join and SHALL set the allowed-invite policy to the value that permits invitations to any email domain.
14. WHEN the Workspace_Service creates a Workspace, THE Workspace_Service SHALL record the Stytch `organization_id`, the Workspace_Name, the Workspace_Slug, the creating user identifier, and the creation time.
15. WHERE an Employer holds Permission `workspace.rename`, THE Workspace_Service SHALL accept a new Workspace_Name for that Workspace and SHALL apply criteria 2 through 6 to the new value.
16. WHEN a Workspace_Name changes, THE Workspace_Service SHALL retain the existing Workspace_Slug and SHALL propagate the new Workspace_Name to the corresponding Stytch_Organization.
17. WHEN a Workspace is created, THE Workspace_Service SHALL create zero Companies, and SHALL leave Company creation to the existing vacancy-posting flow.

### Requirement 3: Complete Onboarding Atomically And Resume Interrupted Onboarding

**User Story:** As a new Employer, I want a dropped connection during signup to leave me able to continue, so that I never end up with two workspaces or an account I cannot get into.

#### Acceptance Criteria

1. THE Onboarding_Service SHALL persist an Onboarding_State record whose stage is exactly one of `awaiting_verification`, `awaiting_link_decision`, `awaiting_workspace_name`, `provisioning`, and `complete`, where `awaiting_verification` is the transient stage recorded immediately after Stytch reports a successful authentication and before candidate discovery (Requirement 4 criterion 1) determines whether the record advances to `awaiting_link_decision` or `awaiting_workspace_name`.
2. THE Onboarding_Service SHALL record the Stytch `organization_id` on the Onboarding_State record before creating the corresponding Workspace document.
3. WHEN an authenticated Employer whose Onboarding_State record is in the `provisioning` stage submits a Workspace_Name a second time, THE Workspace_Service SHALL reuse the Stytch `organization_id` already recorded on that Onboarding_State record and SHALL create zero additional Stytch_Organizations.
4. WHEN the Fundis_Backend receives two concurrent Workspace_Name submissions for the same Onboarding_State record, THE Workspace_Service SHALL create exactly one Workspace and exactly one Membership.
5. IF the Workspace_Service creates a Stytch_Organization and then fails to create the corresponding Workspace document, THEN THE Onboarding_Service SHALL retain the Stytch `organization_id` on the Onboarding_State record and SHALL respond with HTTP status 503 and a message inviting the Employer to retry.
6. WHEN an Employer with an Onboarding_State record in any stage other than `complete` authenticates again, THE Onboarding_Service SHALL resume that Onboarding_State record at its recorded stage and SHALL create zero additional Onboarding_State records for that Stytch_Member.
7. THE Onboarding_Service SHALL treat every Onboarding_State record that has remained outside the `complete` stage for more than 72 hours as abandoned.
8. WHEN an Onboarding_State record is treated as abandoned and holds a Stytch `organization_id` with zero corresponding Workspace documents and zero Stytch_Members beyond the creating Stytch_Member, THE Onboarding_Service SHALL be the sole owner of deleting that Stytch_Organization: it SHALL call the Stytch delete API directly and handle cleanup synchronously, and SHALL write one Audit_Log_Entry recording the deletion. THE Webhook_Consumer SHALL NOT independently delete Workspace records upon receiving the resulting organization-deletion webhook for an organization the Onboarding_Service has already reaped; Requirement 19 criterion 9 applies only to organization-deletion events not originated by this reaping process.
9. THE Fundis_Backend SHALL expose an operational report listing every Stytch_Organization with zero corresponding Workspace documents and every Workspace whose recorded Stytch `organization_id` returns a not-found result from Stytch.
10. WHEN the Onboarding_Service reaches the `complete` stage for an Employer, THE Onboarding_Service SHALL guarantee that exactly one `users` document, exactly one Account_Link, exactly one Workspace, and exactly one Membership with Workspace_Role `owner` exist for that Employer.
11. WHEN an Employer abandons signup after Stytch authentication and before Workspace creation, THE Onboarding_Service SHALL retain the `users` document and SHALL allow that Employer to reach the `awaiting_workspace_name` stage on the next authentication.
12. THE Onboarding_Service SHALL assign every Workspace provisioning attempt an Idempotency value derived from the Onboarding_State record identifier and SHALL send that value to Stytch as the idempotency key where the Stytch endpoint accepts one.

### Requirement 4: Detect A Pre-Existing Fundis Account During Signup

**User Story:** As someone who already used Fundis to hire a fundi, I want the system to notice that my email is already registered, so that my history is not split across two accounts.

#### Acceptance Criteria

1. WHEN Stytch reports a successful authentication for an email address, THE Onboarding_Service SHALL search the `users` collection for documents whose stored email equals that address after Email_Normalization is applied to both values.
2. WHEN the search stated in criterion 1 returns at least one `users` document with zero Account_Link records, THE Onboarding_Service SHALL set the Onboarding_State stage to `awaiting_link_decision` and SHALL respond with a payload naming each Link_Candidate by masked email, masked phone, `user_type`, and account creation date.
3. THE Onboarding_Service SHALL mask a Link_Candidate email address by revealing at most the first character of the local part and the full domain, and SHALL mask a Link_Candidate phone number by revealing at most the final 3 digits.
4. WHEN the search stated in criterion 1 returns a `users` document that already holds an Account_Link to a different Stytch_Member, THE Onboarding_Service SHALL exclude that document from the Link_Candidate set and SHALL respond with HTTP status 409, a machine-readable code of `email_already_linked`, and a message directing the caller to sign in.
5. WHILE an Onboarding_State record is in the `awaiting_link_decision` stage, THE Onboarding_Service SHALL accept exactly two decisions from the Employer: link to a named Link_Candidate, or create a separate new Employer account.
6. WHEN an Employer chooses to create a separate new Employer account while a Link_Candidate exists, THE Onboarding_Service SHALL require an email address that differs, after Email_Normalization, from every Link_Candidate email address, because the `uniq_email` index permits one document per email address.
7. THE Onboarding_Service SHALL treat email addresses that differ only in letter case as the same address, and SHALL treat email addresses that differ in any other character, including addresses that differ only by a plus-suffix in the local part or only by dot placement in the local part, as different addresses. This is a known limitation: Gmail (and some other providers) treat `user+tag@gmail.com` and `user@gmail.com`, or `u.ser@gmail.com` and `user@gmail.com`, as the same mailbox, so the same person can create more than one Fundis Employer account using such aliases of the same underlying mailbox. The business decision is to accept this risk at launch rather than implement provider-specific alias normalization.
8. WHEN more than one `users` document matches the authenticating email address after Email_Normalization, because historical records were stored with differing letter case, THE Onboarding_Service SHALL present every such document as a Link_Candidate and SHALL create at most one Account_Link across the whole Link_Candidate set.
9. WHERE a Link_Candidate holds `user_type` equal to `artisan`, `assessor`, or `admin`, THE Onboarding_Service SHALL withhold the link option for that Link_Candidate, SHALL respond with a machine-readable code of `email_belongs_to_non_employer`, and SHALL direct the caller to use a different email address or to contact support.
10. IF a Link_Candidate holds `status` equal to `suspended`, THEN THE Onboarding_Service SHALL withhold the link option for that Link_Candidate, SHALL create zero Account_Links, and SHALL respond with a message directing the caller to contact support.
11. WHERE a Link_Candidate holds `migrated` equal to `true`, THE Onboarding_Service SHALL offer the link option and SHALL require zero password entry, because migrated accounts hold no usable password hash.
12. WHERE a Link_Candidate holds `is_active` equal to `false`, THE Onboarding_Service SHALL offer the link option and SHALL set `is_active` to `true` and `status` to `active` on that document when the Account_Link is created, because Stytch has already verified control of the email address.

### Requirement 5: Verify Control Of A Pre-Existing Account Before Linking

**User Story:** As an existing Fundis user, I want anyone claiming my email to prove they control my account before it is merged, so that my hiring history and payment records cannot be hijacked.

#### Acceptance Criteria

1. WHEN an Employer chooses to link a named Link_Candidate, THE Link_Service SHALL issue a Link_Challenge before creating an Account_Link.
2. WHERE the Link_Candidate email address equals the Stytch-verified email address after Email_Normalization, THE Link_Service SHALL treat the Stytch email verification as a satisfied Link_Challenge and SHALL require zero additional proof.
3. WHERE the Link_Candidate holds a phone number and the Link_Candidate email address does not equal the Stytch-verified email address, THE Link_Service SHALL issue a one-time code to that phone number using the existing `users.IssuePhoneOTP` flow and SHALL create the Account_Link only after that code is submitted correctly.
4. THE Link_Service SHALL expire an unsatisfied Link_Challenge 15 minutes after issuance.
5. THE Link_Service SHALL accept at most 5 Link_Challenge submissions per Link_Candidate per hour, and SHALL respond with HTTP status 429 for further submissions within that hour.
6. IF a Link_Challenge submission carries an incorrect value, THEN THE Link_Service SHALL respond with HTTP status 401, create zero Account_Links, and increment the recorded attempt count for that Link_Challenge.
7. WHEN a Link_Challenge is satisfied, THE Link_Service SHALL create exactly one Account_Link between the Link_Candidate `users` document and the authenticating Stytch_Member, and SHALL advance the Onboarding_State stage to `awaiting_workspace_name`.
8. THE Link_Service SHALL enforce, through a unique database index, that one `users` document holds at most one Account_Link and that one Stytch_Member is referenced by at most one Account_Link, SHALL set that Account_Link to reference the Stytch_Member belonging to the Workspace in which the Account_Link was established, and SHALL treat the Account_Link as the primary binding between one person and one `users` document, while THE Membership SHALL carry the Stytch_Member identifier for the Workspace that Membership binds.
9. WHEN an Account_Link is created, THE Fundis_Backend SHALL send a notification to the linked account's email address and, where a phone number is present, to that phone number, stating that the account is now connected to a Stytch sign-in and naming the time of the change.
10. WHEN an Account_Link is created, THE Audit_Logger SHALL write one Audit_Log_Entry naming the `users` document identifier, the Stytch `member_id`, the Link_Challenge method used, and the requesting IP address.
11. WHEN an Account_Link is created for a `users` document that holds a bcrypt password, THE Link_Service SHALL retain that password hash and SHALL keep Legacy_Auth sign-in available for that document until the cutover stated in Requirement 21 criterion 11.
12. WHERE an Account_Link exists for a `users` document, THE Onboarding_Service SHALL sign that Employer in through Stytch on subsequent authentications and SHALL create zero additional `users` documents for that Stytch_Member.
13. IF Stytch reports that the email address of a linked Stytch_Member has changed, THEN THE Link_Service SHALL retain the Account_Link, SHALL update the stored email on the linked `users` document only when the new address is unheld by any other `users` document, and SHALL write one Audit_Log_Entry recording the outcome.
14. IF Stytch reports an email address change to an address already held by another `users` document, THEN THE Link_Service SHALL retain the previously stored email on the linked document, SHALL flag the Account_Link for manual review, and SHALL write one Audit_Log_Entry naming both addresses in masked form.
15. WHERE an Employer holds an Account_Link, THE Fundis_Backend SHALL expose that Employer's linked email address and linked sign-in method on the account settings endpoint.
16. THE Link_Service SHALL support unlinking an Account_Link only for a user who holds zero `owner` Memberships, and SHALL respond with HTTP status 409 and a message naming the affected Workspaces for a user who holds at least one.


### Requirement 6: Preserve Existing Employer Data Across Linking

**User Story:** As a returning Employer, I want my existing companies, vacancies, and subscription to appear inside my new workspace, so that linking my account does not cost me my history.

#### Acceptance Criteria

1. WHEN an Account_Link is created for a `users` document and that Employer subsequently creates a Default_Workspace, THE Workspace_Service SHALL set `workspace_id` on every `companies` document whose `owner_id` equals that user identifier to the new Workspace identifier.
2. WHEN an Account_Link is created for a `users` document and that Employer subsequently creates a Default_Workspace, THE Workspace_Service SHALL set `workspace_id` on every `job_vacancies` document whose `owner_id` equals that user identifier to the new Workspace identifier.
3. WHEN an Account_Link is created for a `users` document and that Employer subsequently creates a Default_Workspace, THE Billing_Service SHALL set `workspace_id` on every `user_subscriptions` document whose `user_id` equals that user identifier and whose audience denotes an Employer subscription to the new Workspace identifier.
4. THE Workspace_Service SHALL retain the existing `owner_id` value on every re-scoped document so that the individual who created each record remains attributable.
5. WHEN the re-scoping stated in criteria 1 through 3 is applied a second time for the same user and Workspace, THE Workspace_Service SHALL leave every affected document unchanged.
6. IF the re-scoping stated in criteria 1 through 3 fails part-way, THEN THE Workspace_Service SHALL record the failure, SHALL leave the Workspace usable, and SHALL retry the remaining documents on the next authenticated request from that Employer.
7. WHEN an Employer who already holds an `active` Membership in a Workspace links an account, THE Workspace_Service SHALL apply zero re-scoping to that existing Workspace and SHALL respond with a payload naming the Workspaces the Employer may transfer data into.
8. WHERE an Employer holds Permission `workspace.import_personal_data`, THE Workspace_Service SHALL accept a request to re-scope that Employer's own unscoped `companies` and `job_vacancies` documents into a named Workspace in which that Employer holds an `active` Membership.
9. THE Workspace_Service SHALL reject a re-scoping request naming a Workspace in which the requesting Employer holds no `active` Membership, with HTTP status 403.
10. THE Fundis_Backend SHALL retain every quotation, review, transaction, and notification record associated with a linked `users` document, applying zero deletions as a consequence of linking.

### Requirement 7: Define The Workspace Role And Permission Model

**User Story:** As a Workspace Owner, I want each teammate to hold exactly the authority their job needs, so that HR staff cannot touch billing and finance staff cannot delete my workspace.

#### Acceptance Criteria

1. THE Fundis_Backend SHALL support exactly four Workspace_Roles: `owner`, `admin`, `hr`, and `finance`.
2. THE Fundis_Backend SHALL assign exactly one Workspace_Role to each Membership.
3. THE Permission_Matrix SHALL grant Workspace_Role `owner` every Permission the Fundis_Backend defines.
4. THE Permission_Matrix SHALL grant Workspace_Role `admin` every Permission needed to manage members and day-to-day workspace administration — including vacancy and application management, company management, and artisan browsing — plus read-only visibility of billing, invoices, and payouts, while excluding `workspace.delete`, `ownership.transfer`, and every Permission this requirement reserves to `owner` and `finance` alone (criteria 7 through 9). The exact Permission set is a design and code-level concern (see the design document and P1/P2), not a requirements-level enumeration, so that adding a new Permission constant does not require editing this document.
5. THE Permission_Matrix SHALL grant Workspace_Role `hr` every Permission needed to manage vacancies, review and decide applications, manage companies, and browse artisans, and SHALL grant `hr` zero billing, invoice, payout, or member-management Permissions.
6. THE Permission_Matrix SHALL grant Workspace_Role `finance` every Permission needed to manage billing, payment methods, invoices, payouts, and subscriptions, plus read-only workspace and member visibility, and SHALL grant `finance` zero vacancy or application Permissions.
7. THE Permission_Matrix SHALL grant the Permission `workspace.delete` to Workspace_Role `owner` alone.
8. THE Permission_Matrix SHALL grant the Permission `ownership.transfer` to Workspace_Role `owner` alone.
9. THE Permission_Matrix SHALL grant the Permissions `billing.manage`, `payment_method.manage`, `invoice.pay`, and `subscription.manage` to Workspace_Roles `owner` and `finance` alone.
10. THE Membership_Service SHALL reject every request by a holder of Workspace_Role `admin` to change the Workspace_Role of the Membership holding Workspace_Role `owner`, with HTTP status 403 and a machine-readable code of `owner_protected`.
11. THE Membership_Service SHALL reject every request by a holder of Workspace_Role `admin` to remove the Membership holding Workspace_Role `owner`, with HTTP status 403 and a machine-readable code of `owner_protected`.
12. THE Membership_Service SHALL reject every request by a holder of Workspace_Role `admin` to grant Workspace_Role `owner` to any Membership, with HTTP status 403.
13. THE Permission_Matrix SHALL reside in Fundis_Backend source code as a single declaration, and the Fundis_Backend SHALL apply zero database reads and zero Stytch reads when evaluating whether a Workspace_Role grants a Permission.
14. WHEN the Fundis_Backend evaluates a Permission for a Membership, THE Permission_Guard SHALL use the Workspace_Role recorded on the Fundis Membership document as the sole input, applying zero reliance on any Stytch RBAC decision.
15. WHEN a Membership is created or its Workspace_Role changes, THE Stytch_Adapter SHALL set the corresponding Stytch_Member roles to mirror the Fundis Workspace_Role, and a failure to do so SHALL leave the Fundis Membership unchanged and produce one recorded warning.
16. THE Fundis_Backend SHALL expose an endpoint returning the Permission_Matrix and the requesting Employer's granted Permission set for the active Workspace.
17. THE Fundis_Backend SHALL reject every Membership write carrying a Workspace_Role value outside the four values stated in criterion 1, with HTTP status 400.
18. WHERE a Workspace_Role holds a Permission, THE Permission_Matrix SHALL apply that grant identically in every Workspace, applying zero per-Workspace customization of the Permission_Matrix at launch.

### Requirement 8: Enforce Permissions On Every Workspace-Scoped Request

**User Story:** As a Workspace Owner, I want the API itself to enforce roles, so that a teammate cannot reach a restricted action by calling an endpoint directly.

#### Acceptance Criteria

1. WHEN the Fundis_Backend receives a request to a workspace-scoped endpoint, THE Session_Resolver SHALL resolve a Workspace_Session_Context before the handler runs.
2. IF the Session_Resolver cannot resolve an authenticated user, THEN THE Permission_Guard SHALL respond with HTTP status 401 and the handler SHALL run zero times.
3. IF the Session_Resolver resolves an authenticated user who holds no `active` Membership in the named Workspace, THEN THE Permission_Guard SHALL respond with HTTP status 404 and the handler SHALL run zero times.
4. IF the resolved Workspace_Session_Context lacks the Permission that an endpoint requires, THEN THE Permission_Guard SHALL respond with HTTP status 403, a machine-readable code of `permission_denied`, and the name of the missing Permission, and the handler SHALL run zero times.
5. THE Fundis_Backend SHALL declare a required Permission for every workspace-scoped endpoint it registers.
6. THE Fundis_Backend SHALL fail startup when at least one endpoint registered inside a workspace-scoped route group declares zero required Permissions.
7. WHILE a Membership holds Membership_Status `suspended`, THE Permission_Guard SHALL respond with HTTP status 403 and a machine-readable code of `membership_suspended` for every workspace-scoped request from that Membership.
8. WHILE a Membership holds Membership_Status `removed`, THE Permission_Guard SHALL respond with HTTP status 404 for every workspace-scoped request naming that Workspace.
9. WHILE the `users` document behind a Membership holds `status` equal to `suspended`, THE Permission_Guard SHALL respond with HTTP status 403 for every workspace-scoped request from that user regardless of Workspace_Role.
10. Because a Stytch_Session is scoped to exactly one Stytch_Organization (see Requirement 16 criterion 5), the active Workspace named by the Workspace_Session_Context is always the Workspace named by the URL for a regular workspace-scoped endpoint — there is no scenario where the two differ for such an endpoint. Where a platform-administrator endpoint impersonates or inspects a Workspace on behalf of a Workspace member, THE Permission_Guard SHALL resolve the Membership for the Workspace named in the URL path and SHALL apply criteria 3 and 4 against that Membership, independently of the administrator's own Workspace_Session_Context.
11. THE Permission_Guard SHALL apply zero authorization decisions based on the `user_type` field for workspace-scoped endpoints.
12. THE Fundis_Backend SHALL retain the existing `middleware.IsAdmin` platform-administrator check for platform administration endpoints, independently of the Permission_Matrix.

### Requirement 9: Isolate Data Between Workspaces

**User Story:** As an Employer, I want to be certain that no other business can read my vacancies, applicants, or invoices, so that I can trust the platform with commercially sensitive hiring information.

#### Acceptance Criteria

1. THE Fundis_Backend SHALL store a `workspace_id` value on every `companies`, `job_vacancies`, `job_applications`, `user_subscriptions`, `invoices`, and workspace-scoped attachment document it creates after this feature ships.
2. WHEN the Fundis_Backend reads a workspace-scoped document, THE Fundis_Backend SHALL include the active Workspace identifier in the query filter.
3. IF a workspace-scoped read names a document whose `workspace_id` differs from the active Workspace identifier, THEN THE Fundis_Backend SHALL respond with HTTP status 404 rather than HTTP status 403, so that document existence is not disclosed.
4. IF a workspace-scoped write names a document whose `workspace_id` differs from the active Workspace identifier, THEN THE Fundis_Backend SHALL apply zero modifications and SHALL respond with HTTP status 404.
5. THE Fundis_Backend SHALL scope the existing employer dashboard aggregation to the active Workspace identifier rather than to a single `owner_id`.
6. THE Fundis_Backend SHALL scope the existing employer vacancy listing to the active Workspace identifier rather than to a single `owner_id`.
7. WHERE a `companies` or `job_vacancies` document holds zero `workspace_id` values, THE Fundis_Backend SHALL treat that document as visible only to the `users` document named by its `owner_id`.
8. THE Fundis_Backend SHALL apply zero changes to the visibility of published vacancies to artisans, which remain publicly listable independently of Workspace membership.
9. WHEN an artisan submits an application to a vacancy, THE Fundis_Backend SHALL set the application `workspace_id` to the `workspace_id` of the target vacancy.
10. WHEN a member of a Workspace views an application, THE Fundis_Backend SHALL disclose the applicant fields the existing application review flow already discloses, and SHALL apply zero additional disclosure as a consequence of Workspace_Role.

### Requirement 10: Invite Teammates Into A Workspace

**User Story:** As a Workspace Owner, I want to invite my HR lead, my operations admin, and my accountant by email with the right role, so that each of them can do their job without sharing my login.

#### Acceptance Criteria

1. WHEN a caller holding Permission `member.invite` submits an email address and a Workspace_Role for the active Workspace, THE Invitation_Service SHALL create one Invitation with Invitation_Status `pending`, SHALL generate one Invitation_Token, and SHALL send one invitation email to that address.
2. THE Invitation_Service SHALL apply Email_Normalization to every submitted invitation email address before storage and comparison.
3. THE Invitation_Service SHALL accept exactly the Workspace_Roles `admin`, `hr`, and `finance` on an Invitation, and SHALL respond with HTTP status 400 and a machine-readable code of `owner_not_invitable` for a submitted Workspace_Role of `owner`.
4. IF a submitted invitation email address fails RFC 5322 address parsing, THEN THE Invitation_Service SHALL respond with HTTP status 400 and a message naming the `email` field, and SHALL send zero emails.
5. IF the submitted invitation email address equals, after Email_Normalization, the email address of a `users` document that already holds an `active` Membership in the target Workspace, THEN THE Invitation_Service SHALL respond with HTTP status 409, a machine-readable code of `already_a_member`, and the existing Workspace_Role.
6. IF a `pending` Invitation already exists for the submitted email address and target Workspace, THEN THE Invitation_Service SHALL apply zero new Invitation creations, SHALL update the existing Invitation's Workspace_Role to the submitted value, SHALL extend the existing Invitation's expiry, and SHALL respond with HTTP status 200 and a machine-readable code of `invitation_updated`.
7. IF the submitted invitation email address equals, after Email_Normalization, the email address of a `users` document holding a Membership with Membership_Status `removed` in the target Workspace, THEN THE Invitation_Service SHALL create a `pending` Invitation and SHALL reactivate that Membership only upon acceptance.
8. THE Invitation_Service SHALL generate an Invitation_Token containing at least 128 bits of entropy from a cryptographically secure source, and SHALL store only a hash of that Invitation_Token.
9. THE Invitation_Service SHALL set every Invitation to expire 7 days after issuance.
10. THE Invitation_Service SHALL record the inviting user identifier, the target Workspace identifier, the invited email address, the offered Workspace_Role, the issuance time, and the expiry time on every Invitation.
11. THE Invitation_Service SHALL include the Workspace_Name, the inviter's display name, the offered Workspace_Role, a plain-language summary of the Permissions that Workspace_Role grants, and the expiry date in every invitation email.
12. THE Invitation_Service SHALL accept at most 20 Invitation creations per Workspace per hour, and SHALL respond with HTTP status 429 and a machine-readable code of `invite_rate_limited` for further creations within that hour.
13. THE Invitation_Service SHALL accept at most 100 `pending` Invitations per Workspace at any time, and SHALL respond with HTTP status 409 and a machine-readable code of `pending_invite_limit` for further creations while that count is reached.
14. THE Invitation_Service SHALL treat an emailed Invitation as the only path into a Workspace, applying zero domain-based auto-join and zero self-service join-by-Workspace_Slug.
15. WHERE the invited email address matches no existing `users` document, THE Invitation_Service SHALL create the Invitation and SHALL defer `users` document creation until acceptance.
16. IF sending an invitation email fails, THEN THE Invitation_Service SHALL retain the `pending` Invitation, SHALL respond with HTTP status 202 and a machine-readable code of `invitation_created_delivery_pending`, and SHALL make the Invitation resendable.
17. WHEN a caller holding Permission `member.view` lists Invitations for the active Workspace, THE Invitation_Service SHALL return every Invitation with its Invitation_Status, offered Workspace_Role, invited email address, and expiry time, and SHALL return zero Invitation_Token values.
18. THE Invitation_Service SHALL create zero Invitations for a Workspace whose deletion is scheduled or complete, responding with HTTP status 409.

### Requirement 11: Accept A Workspace Invitation

**User Story:** As an invited teammate, I want a single link that gets me into the workspace with the right role, so that I can start work without a support ticket.

#### Acceptance Criteria

1. WHEN a visitor presents an Invitation_Token, THE Invitation_Service SHALL resolve the corresponding Invitation and SHALL return the Workspace_Name, the offered Workspace_Role, the inviter's display name, and the invited email address in masked form before requiring authentication.
2. IF a presented Invitation_Token matches zero stored Invitation_Token hashes, THEN THE Invitation_Service SHALL respond with HTTP status 404 and a machine-readable code of `invitation_not_found`.
3. WHEN an invited person authenticates through Stytch with the invited email address and presents a `pending`, unexpired Invitation_Token, THE Membership_Service SHALL create one Membership in the target Workspace with the offered Workspace_Role and Membership_Status `active`, and THE Invitation_Service SHALL set that Invitation's Invitation_Status to `accepted`.
4. IF the authenticated email address differs, after Email_Normalization, from the invited email address, THEN THE Invitation_Service SHALL create zero Memberships and SHALL respond with HTTP status 403, a machine-readable code of `invitation_email_mismatch`, and the invited email address in masked form.
5. WHEN an already-authenticated Employer presents an Invitation_Token whose invited email address differs from that Employer's own email address, THE Invitation_Service SHALL respond with HTTP status 403, a machine-readable code of `invitation_email_mismatch`, and the invited email address in masked form, and SHALL create zero Memberships until the authenticated email address matches.
6. WHEN an invited email address matches an existing `users` document with an Account_Link, THE Membership_Service SHALL create the Membership against that existing `users` document and SHALL create zero additional `users` documents.
7. WHEN an invited email address matches an existing `users` document without an Account_Link, THE Link_Service SHALL apply Requirement 5 before the Membership is created.
8. WHEN an invited email address matches zero `users` documents, THE Membership_Service SHALL create one `users` document following Requirement 1 criteria 11 through 13 and SHALL create the Membership against that document.
9. WHEN a Membership is created by Invitation acceptance, THE Stytch_Adapter SHALL create the corresponding Stytch_Member inside the Workspace's Stytch_Organization.
10. IF the Stytch_Member creation stated in criterion 9 fails, THEN THE Membership_Service SHALL create zero Memberships, SHALL leave the Invitation with Invitation_Status `pending`, and SHALL respond with HTTP status 503.
11. WHEN the same Invitation_Token is presented twice and the first presentation resulted in an `accepted` Invitation_Status, THE Invitation_Service SHALL create zero additional Memberships and SHALL respond with HTTP status 200 naming the existing Membership.
12. IF a presented Invitation holds Invitation_Status `revoked`, THEN THE Invitation_Service SHALL create zero Memberships and SHALL respond with HTTP status 410 and a machine-readable code of `invitation_revoked`.
13. IF a presented Invitation holds Invitation_Status `expired`, or holds Invitation_Status `pending` with an expiry time in the past, THEN THE Invitation_Service SHALL create zero Memberships and SHALL respond with HTTP status 410, a machine-readable code of `invitation_expired`, and an option to request a fresh Invitation from the Workspace.
14. IF a presented Invitation names a Workspace whose deletion is scheduled or complete, THEN THE Invitation_Service SHALL create zero Memberships and SHALL respond with HTTP status 410.
15. WHEN an Invitation is accepted, THE Invitation_Service SHALL notify the inviting user by email that the Invitation was accepted, naming the accepting email address and the granted Workspace_Role.
16. WHEN an Invitation is accepted, THE Stytch_Adapter SHALL create a Stytch_Session scoped to the target Stytch_Organization and SHALL return it in the acceptance response, so that the accepting person's next request resolves the newly joined Workspace as the active Workspace through the ordinary session-resolution path (Requirement 16 criterion 5).
17. WHEN an Invitation is accepted, THE Invitation_Service SHALL set every other `pending` Invitation for the same email address and the same Workspace to Invitation_Status `revoked`.
18. THE Invitation_Service SHALL accept at most 10 Invitation_Token resolution attempts per IP address per minute, and SHALL respond with HTTP status 429 for further attempts within that minute.

### Requirement 12: Manage The Invitation Lifecycle

**User Story:** As a Workspace Admin, I want to resend, revoke, and see the state of every invitation, so that stale invitations never become a way into my workspace.

#### Acceptance Criteria

1. THE Invitation_Service SHALL hold every Invitation in exactly one Invitation_Status at any time.
2. WHEN a caller holding Permission `member.invite` revokes a `pending` Invitation, THE Invitation_Service SHALL set that Invitation's Invitation_Status to `revoked` and SHALL invalidate the associated Invitation_Token.
3. WHEN a caller holding Permission `member.invite` resends a `pending` Invitation, THE Invitation_Service SHALL generate a fresh Invitation_Token, SHALL invalidate the previous Invitation_Token, SHALL reset the expiry to 7 days after the resend, and SHALL send one invitation email.
4. THE Invitation_Service SHALL accept at most 3 resends per Invitation per hour, and SHALL respond with HTTP status 429 for further resends within that hour.
5. WHEN an invited person declines an Invitation, THE Invitation_Service SHALL set that Invitation's Invitation_Status to `declined`, SHALL create zero Memberships, and SHALL notify the inviting user by email.
6. THE Invitation_Service SHALL permit a decline without requiring the invited person to authenticate, using the Invitation_Token as the sole authorization.
7. WHEN an Invitation with Invitation_Status `pending` passes its expiry time, THE Invitation_Service SHALL report that Invitation's Invitation_Status as `expired` on every read.
8. THE Fundis_Backend SHALL set the stored Invitation_Status of every `pending` Invitation whose expiry time has passed to `expired` at least once every 24 hours, using the existing `jobs` scheduler.
9. THE Invitation_Service SHALL reject a revoke or resend request naming an Invitation whose Invitation_Status is `accepted`, `declined`, or `revoked`, with HTTP status 409.
10. THE Invitation_Service SHALL retain every Invitation record for at least 180 days after it leaves Invitation_Status `pending`, so that the invitation history remains auditable.
11. WHEN the Membership of the inviting user is removed, THE Invitation_Service SHALL retain every `pending` Invitation that user created and SHALL keep those Invitations acceptable.
12. WHEN a caller revokes a `pending` Invitation, THE Audit_Logger SHALL write one Audit_Log_Entry naming the revoking user, the Workspace, and the invited email address.

### Requirement 13: Change And Revoke Memberships

**User Story:** As a Workspace Owner, I want to change a teammate's role or remove them when they leave, so that access always matches who actually works here.

#### Acceptance Criteria

1. WHEN a caller holding Permission `member.role_change` submits a new Workspace_Role for a Membership in the active Workspace, THE Membership_Service SHALL set that Membership's Workspace_Role to the submitted value and SHALL increment that Membership's Authority_Version.
2. WHEN a caller holding Permission `member.remove` removes a Membership in the active Workspace, THE Membership_Service SHALL set that Membership's Membership_Status to `removed`, SHALL increment that Membership's Authority_Version, and SHALL retain the Membership record.
3. WHEN a Membership's Membership_Status is set to `removed`, THE Stytch_Adapter SHALL delete the corresponding Stytch_Member from the Workspace's Stytch_Organization and SHALL revoke every Stytch_Session held by that Stytch_Member.
4. IF the Stytch_Member deletion stated in criterion 3 fails, THEN THE Membership_Service SHALL retain Membership_Status `removed`, SHALL record the failure, and SHALL retry the deletion at least once every 15 minutes for at least 24 hours. Because Membership_Status is already `removed` for the duration of the retry window, a Stytch_Webhook_Event that arrives for this Stytch_Member during that window (for example, an email-change event) finds the Membership already in its terminal state; Requirement 19 criterion 7's transition to `removed` is therefore idempotent against this in-progress deletion and produces no additional effect regardless of arrival order.
5. THE Permission_Guard SHALL deny every workspace-scoped request from a Membership whose Membership_Status is `removed` regardless of the state of the corresponding Stytch_Member. This is deliberate eventual consistency: Fundis access is revoked immediately on removal, while the corresponding Stytch_Member may still exist in Stytch for up to 24 hours during the retry window stated in criterion 4. That window is a bounded, accepted risk, not a defect — Fundis authorization never depends on the Stytch-side state.
6. WHEN a caller removes a Membership, THE Fundis_Backend SHALL send an email to the removed person's address stating that access to the named Workspace has ended.
7. THE Membership_Service SHALL permit a Membership holder to remove that holder's own Membership, and SHALL reject that request with HTTP status 409 and a machine-readable code of `owner_must_transfer_first` when the Workspace_Role is `owner`.
8. THE Membership_Service SHALL reject a request by a caller to change that caller's own Workspace_Role, with HTTP status 409 and a machine-readable code of `self_role_change_denied`.
9. WHEN a Membership's Workspace_Role changes, THE Fundis_Backend SHALL send an email to that person's address naming the previous Workspace_Role and the new Workspace_Role.
10. WHEN a Membership's Workspace_Role or Membership_Status changes, THE Audit_Logger SHALL write one Audit_Log_Entry naming the acting user, the target user, the Workspace, the previous value, and the new value.
11. WHEN a caller holding Permission `member.remove` suspends a Membership, THE Membership_Service SHALL set that Membership's Membership_Status to `suspended`, SHALL increment that Membership's Authority_Version, and SHALL retain the corresponding Stytch_Member.
12. WHEN a caller holding Permission `member.remove` reinstates a Membership whose Membership_Status is `suspended` or `removed`, THE Membership_Service SHALL set that Membership's Membership_Status to `active`, SHALL increment that Membership's Authority_Version, and SHALL recreate the corresponding Stytch_Member when zero Stytch_Members exist for that person in the Workspace's Stytch_Organization.
13. WHEN a Membership is removed, THE Fundis_Backend SHALL retain every `companies`, `job_vacancies`, and `job_applications` document that Membership created and SHALL keep those documents accessible to the remaining Workspace members.
14. THE Membership_Service SHALL apply zero deletions to a person's `users` document as a consequence of Membership removal.
15. WHEN a caller holding Permission `member.view` lists Memberships for the active Workspace, THE Membership_Service SHALL return each member's display name, email address, Workspace_Role, Membership_Status, and join date.


### Requirement 14: Protect The Owner Role And Transfer Ownership

**User Story:** As a Workspace Owner leaving the company, I want to hand ownership to a colleague, so that the workspace is never left without someone who can administer it.

#### Acceptance Criteria

1. THE Fundis_Backend SHALL hold exactly one Membership with Workspace_Role `owner` and Membership_Status `active` for every Workspace that is not scheduled for deletion.
2. THE Fundis_Backend SHALL enforce the constraint stated in criterion 1 through a partial unique database index on the Workspace identifier for Memberships holding Workspace_Role `owner`.
3. WHEN a caller holding Permission `ownership.transfer` names a target Membership in the active Workspace, THE Membership_Service SHALL set the target Membership's Workspace_Role to `owner` and SHALL set the caller's Membership Workspace_Role to `admin` in one atomic operation.
4. IF the atomic operation stated in criterion 3 fails part-way, THEN THE Membership_Service SHALL leave both Memberships at their previous Workspace_Roles.
5. THE Membership_Service SHALL reject an ownership transfer naming a target Membership whose Membership_Status is not `active`, with HTTP status 409.
6. THE Membership_Service SHALL reject an ownership transfer naming a target `users` document whose `status` is `suspended`, with HTTP status 409.
7. THE Membership_Service SHALL reject an ownership transfer naming the caller's own Membership, with HTTP status 400.
8. WHEN an ownership transfer completes, THE Membership_Service SHALL increment the Authority_Version of both affected Memberships.
9. WHEN an ownership transfer completes, THE Fundis_Backend SHALL send an email to the previous Owner and to the new Owner naming the Workspace, both parties, and the time of the transfer.
10. WHEN an ownership transfer completes, THE Audit_Logger SHALL write one Audit_Log_Entry naming both Memberships, the Workspace, and the time of the transfer.
11. THE Membership_Service SHALL reject every attempt to set the Workspace_Role of the sole `owner` Membership to a value other than `owner` outside the transfer operation stated in criterion 3, with HTTP status 409 and a machine-readable code of `last_owner_protected`.
12. THE Membership_Service SHALL reject every attempt to set the Membership_Status of the sole `owner` Membership to `suspended` or `removed`, with HTTP status 409 and a machine-readable code of `last_owner_protected`.
13. WHERE a `users` document holding an `owner` Membership is suspended by a platform administrator, THE Fundis_Backend SHALL retain that `owner` Membership and SHALL expose an administrator-only operation that transfers ownership to a named `active` Membership in that Workspace.
14. WHERE a Workspace holds exactly one Membership and the corresponding person requests removal of that Membership, THE Membership_Service SHALL respond with HTTP status 409 and SHALL direct the caller to the Workspace deletion flow.
15. IF the Fundis_Backend detects a Workspace with zero `active` Memberships holding Workspace_Role `owner`, THEN THE Fundis_Backend SHALL record one error-severity log naming that Workspace and SHALL include that Workspace in the operational report stated in Requirement 3 criterion 9.

### Requirement 15: Belong To Several Workspaces And Switch Between Them

**User Story:** As a recruitment consultant working with two businesses, I want one login that reaches both workspaces, so that I do not maintain two email addresses.

#### Acceptance Criteria

1. THE Fundis_Backend SHALL permit one `users` document to hold at most one Membership per Workspace and to hold Memberships in an unbounded number of Workspaces.
2. THE Fundis_Backend SHALL enforce the at-most-one-Membership-per-Workspace constraint through a unique database index on the pair of user identifier and Workspace identifier.
3. WHEN an Employer authenticates and holds exactly one `active` Membership, THE Session_Resolver SHALL set that Membership's Workspace as the active Workspace.
4. WHEN an Employer authenticates and holds more than one `active` Membership, THE Session_Resolver SHALL set the most recently active Workspace as the active Workspace and THE Fundis_Frontend SHALL present a Workspace selector.
5. WHEN an Employer authenticates and holds zero `active` Memberships and zero `pending` Invitations, THE Onboarding_Service SHALL set the Onboarding_State stage to `awaiting_workspace_name`.
6. WHEN an Employer authenticates and holds zero `active` Memberships and at least one `pending` Invitation for that Employer's email address, THE Fundis_Frontend SHALL present those Invitations alongside the option to create a new Workspace.
7. WHEN an Employer requests a switch to a named Workspace in which that Employer holds an `active` Membership, THE Session_Resolver SHALL set that Workspace as the active Workspace and SHALL return the Workspace_Role and granted Permission set for that Workspace.
8. IF an Employer requests a switch to a Workspace in which that Employer holds no `active` Membership, THEN THE Session_Resolver SHALL leave the active Workspace unchanged and SHALL respond with HTTP status 404.
9. WHEN an Employer switches the active Workspace, THE Stytch_Adapter SHALL exchange the current Stytch_Session for a Stytch_Session scoped to the target Stytch_Organization.
10. THE Fundis_Backend SHALL expose an endpoint listing every Workspace in which the authenticated Employer holds an `active` Membership, with the Workspace_Name, Workspace_Slug, Workspace_Role, and member count for each.
11. WHERE an Employer holds an `owner` Membership in one Workspace and an `hr` Membership in another, THE Permission_Guard SHALL apply the Permissions of the Membership belonging to the Workspace named by the request and SHALL apply zero Permissions from any other Membership.
12. THE Workspace_Service SHALL permit an Employer holding at least one `active` Membership to create an additional Workspace, and SHALL apply Requirement 2 criteria 2 through 14 to that creation.
13. THE Workspace_Service SHALL permit one Employer to create at most 5 Workspaces within any 24-hour period (see the rate-limit strategy table under Requirement 25, dimension: Employer, identified by `users` document identifier once one exists), and SHALL respond with HTTP status 429 for further creations within that period.

### Requirement 16: Represent The Active Workspace In The Session

**User Story:** As a developer, I want the active workspace and role available on every request without a database round trip per check, so that authorization stays fast and predictable.

#### Acceptance Criteria

1. THE Session_Resolver SHALL accept a Stytch_Session credential presented in the `Authorization` header as a bearer value.
2. WHEN the Session_Resolver validates a Stytch_Session, THE Session_Resolver SHALL verify the session locally against the Stytch JSON Web Key Set where the credential is a session JWT, and SHALL call Stytch to authenticate the session where the credential is an opaque session token.
3. THE Session_Resolver SHALL derive the authenticated user identifier by resolving the Stytch `member_id` reported by the validated Stytch_Session through the Membership field holding the per-Workspace Stytch_Member identifier, and SHALL require an Account_Link to exist for that derived user identifier.
4. IF zero Memberships hold the Stytch `member_id` reported by a validated Stytch_Session, or zero Account_Links exist for the user identifier that Stytch `member_id` resolves to, THEN THE Session_Resolver SHALL respond with HTTP status 401 and a machine-readable code of `unlinked_member`.
5. THE Session_Resolver SHALL derive the active Workspace identifier from the Stytch `organization_id` reported by the validated Stytch_Session.
6. THE Session_Resolver SHALL derive the Workspace_Role from the Fundis Membership document matching the authenticated user identifier and the active Workspace identifier, applying zero reliance on any role claim carried in the Stytch_Session.
7. THE Session_Resolver SHALL cache a resolved Workspace_Session_Context for at most 30 seconds keyed on the pair of user identifier and Workspace identifier together with the Membership Authority_Version.
8. WHEN a Membership's Authority_Version changes, THE Session_Resolver SHALL resolve a fresh Workspace_Session_Context on the next request from that Membership.
9. THE Session_Resolver SHALL apply zero Stytch network calls when a valid cached Workspace_Session_Context exists for the request.
10. THE Fundis_Backend SHALL expose the active Workspace identifier, the Workspace_Role, and the granted Permission set on the authenticated-session endpoint.
11. THE Fundis_Backend SHALL include the active Workspace identifier and the acting user identifier on every request log entry for a workspace-scoped request.
12. THE Fundis_Backend SHALL keep the existing `jwtutil` HS256 validation path available for non-Employer credentials for the duration stated in Requirement 21.
13. WHEN an inbound credential validates under neither the Stytch path nor the `jwtutil` path, THE Session_Resolver SHALL respond with HTTP status 401 and SHALL disclose zero information about which path was attempted.

### Requirement 17: Revoke Authority Promptly After A Permission Change

**User Story:** As a Workspace Owner who just removed a departing employee, I want their access to stop within seconds, so that a stolen laptop with a live session is not a breach.

#### Acceptance Criteria

1. WHEN a Membership's Workspace_Role or Membership_Status changes, THE Membership_Service SHALL increment that Membership's Authority_Version in the same write that applies the change.
2. WHEN a Membership's Authority_Version changes, THE Session_Resolver SHALL apply the new Workspace_Role to every subsequent request from that Membership within 30 seconds of the change.
3. WHEN a Membership's Membership_Status is set to `removed` or `suspended`, THE Stytch_Adapter SHALL revoke every Stytch_Session held by the corresponding Stytch_Member.
4. WHEN a Membership's Membership_Status is set to `removed` or `suspended`, THE Session_Resolver SHALL invalidate every cached Workspace_Session_Context for that Membership immediately rather than waiting for the cache lifetime to elapse.
5. WHEN a `users` document's `status` is set to `suspended`, THE Stytch_Adapter SHALL revoke every Stytch_Session held by every Stytch_Member linked to that document across every Stytch_Organization.
6. THE Fundis_Backend SHALL expose an endpoint that allows an authenticated Employer to revoke every Stytch_Session held by that Employer across every Workspace.
7. IF a Stytch_Session revocation call fails, THEN THE Fundis_Backend SHALL rely on the Membership check stated in Requirement 8 criteria 7 and 8 to deny access, and SHALL record one warning-severity log naming the Membership.
8. Criteria 8 and 9 apply to a Workspace_Role change (a demotion or promotion that leaves Membership_Status at `active`), which is the only authority change without immediate invalidation. THE Session_Resolver SHALL bound the interval during which a previously issued credential may act under a superseded Workspace_Role to at most 30 seconds, relying on the Authority_Version-keyed cache TTL stated in Requirement 16 criterion 7 rather than an immediate invalidation. This is distinct from criterion 4: a Membership_Status change to `removed` or `suspended` invalidates the cache immediately (0 seconds), while a Workspace_Role change with Membership_Status remaining `active` is bounded by this 30-second cache TTL.
9. WHEN a Workspace_Role change reduces the granted Permission set, THE Permission_Guard SHALL deny requests requiring a removed Permission from the moment the Session_Resolver resolves the new Workspace_Role, applying zero grace period beyond the interval stated in criterion 8.
10. THE Fundis_Backend SHALL apply zero long-lived credentials for Employers whose lifetime exceeds the configured Stytch_Session duration.

### Requirement 18: Delete A Workspace

**User Story:** As a Workspace Owner closing my business, I want to delete the workspace and know what happens to the data, so that I meet my obligations to applicants and to the tax authority.

#### Acceptance Criteria

1. WHEN a caller holding Permission `workspace.delete` requests deletion of the active Workspace, THE Workspace_Service SHALL require a confirmation value equal to the Workspace_Name and SHALL respond with HTTP status 400 when the submitted confirmation value differs.
2. THE Workspace_Service SHALL reject a deletion request from a caller holding any Workspace_Role other than `owner`, with HTTP status 403.
3. WHEN a deletion request is confirmed, THE Workspace_Service SHALL set the Workspace deletion state to `scheduled` with an effective time 7 days after confirmation, and SHALL retain every Workspace-scoped document until that effective time.
4. WHILE a Workspace deletion state is `scheduled`, THE Permission_Guard SHALL grant read access for every `active` Membership, SHALL grant the Owner the ability to cancel the deletion, and SHALL deny every other workspace-scoped write with HTTP status 409 and a machine-readable code of `workspace_deletion_scheduled`.
5. WHILE a Workspace deletion state is `scheduled`, THE Fundis_Backend SHALL set every open `job_vacancies` document in that Workspace to closed.
6. WHEN a Workspace deletion state is set to `scheduled`, THE Fundis_Backend SHALL send an email to every `active` Membership in that Workspace naming the effective time and the cancelling procedure.
7. WHEN the Owner cancels a `scheduled` deletion before the effective time, THE Workspace_Service SHALL clear the deletion state and SHALL restore write access, applying zero reopening of vacancies closed under criterion 5.
8. WHEN the effective time of a `scheduled` deletion passes, THE Workspace_Service SHALL set the Workspace deletion state to `deleted`, SHALL set every Membership in that Workspace to Membership_Status `removed`, and SHALL delete the corresponding Stytch_Organization.
9. WHEN a Workspace deletion state is set to `deleted`, THE Fundis_Backend SHALL retain every `job_applications`, `transactions`, `invoices`, and `user_subscriptions` document belonging to that Workspace for the legally required retention period (minimum 7 years per current Kenyan regulations), so that financial and employment records remain available for statutory purposes.
10. WHEN a Workspace deletion state is set to `deleted`, THE Fundis_Backend SHALL remove the Workspace_Name, the Workspace_Slug, and every Company name in that Workspace from public listings and from search results within 24 hours.
11. THE Workspace_Service SHALL reject a deletion request for a Workspace holding at least one `user_subscriptions` document with `status` equal to `active`, with HTTP status 409, a machine-readable code of `active_subscription`, and instructions to cancel the subscription first.
12. THE Workspace_Service SHALL reject a deletion request for a Workspace holding at least one unsettled artisan payment obligation, with HTTP status 409 and a machine-readable code of `outstanding_obligations`.
13. WHEN a Workspace deletion state changes, THE Audit_Logger SHALL write one Audit_Log_Entry naming the acting user, the Workspace, the previous deletion state, and the new deletion state.
14. WHERE a `users` document holds zero Memberships after a Workspace deletion, THE Fundis_Backend SHALL retain that `users` document and SHALL allow that person to create a new Workspace.

### Requirement 19: Consume Stytch Webhooks Idempotently

**User Story:** As a developer, I want Stytch-side changes reflected in Fundis exactly once, so that a retried webhook does not duplicate a member or resurrect a removed one.

#### Acceptance Criteria

1. THE Webhook_Consumer SHALL verify the signature of every Stytch_Webhook_Event against the configured webhook signing secret before applying any state change.
2. IF a Stytch_Webhook_Event signature fails verification, THEN THE Webhook_Consumer SHALL apply zero state changes, SHALL respond with HTTP status 401, and SHALL record one warning-severity log naming the source IP address.
3. IF a Stytch_Webhook_Event carries a timestamp more than 5 minutes from the Fundis_Backend clock, THEN THE Webhook_Consumer SHALL apply zero state changes and SHALL respond with HTTP status 400.
4. THE Webhook_Consumer SHALL record the identifier of every processed Stytch_Webhook_Event and SHALL retain each such identifier for at least 7 days.
5. WHEN a Stytch_Webhook_Event carries an identifier already recorded as processed, THE Webhook_Consumer SHALL apply zero additional state changes and SHALL respond with HTTP status 200.
6. WHEN a Stytch_Webhook_Event reports a member email address change, THE Webhook_Consumer SHALL apply Requirement 5 criteria 13 and 14.
7. WHEN a Stytch_Webhook_Event reports a member deletion, THE Webhook_Consumer SHALL set the corresponding Membership's Membership_Status to `removed` and SHALL increment that Membership's Authority_Version, except where that Membership holds Workspace_Role `owner`.
8. IF a Stytch_Webhook_Event reports deletion of the Stytch_Member corresponding to an `owner` Membership, THEN THE Webhook_Consumer SHALL retain that Membership, SHALL recreate the Stytch_Member, and SHALL record one error-severity log naming the Workspace.
9. WHEN a Stytch_Webhook_Event reports an organization deletion, THE Webhook_Consumer SHALL set the corresponding Workspace deletion state to `deleted` only where the Fundis deletion state is already `scheduled` or `deleted`, and SHALL record one error-severity log naming the Workspace otherwise.
10. Deduplication by event identifier (criteria 4 and 5) is the primary and sufficient safeguard against reprocessing. Ordering is a secondary concern, handled by processing events in Fundis receipt order rather than by trusting Stytch-reported timestamps, because Stytch may deliver events out of order and clock drift makes timestamp comparison an unreliable ordering signal; THE Webhook_Consumer SHALL NOT reject or discard an event solely because its reported timestamp precedes that of a previously applied event for the same target record.
11. THE Webhook_Consumer SHALL respond within 5 seconds for every Stytch_Webhook_Event, deferring work that exceeds that bound to a background queue.
12. IF processing a Stytch_Webhook_Event fails after signature verification succeeds, THEN THE Webhook_Consumer SHALL respond with HTTP status 500 so that Stytch retries, and SHALL leave the event identifier unrecorded.
13. THE Fundis_Backend SHALL treat the Fundis Membership document as authoritative for authorization at all times, applying zero authorization decision derived directly from webhook payload contents.

### Requirement 20: Degrade Safely When Stytch Is Unavailable

**User Story:** As an Employer already signed in, I want to keep working during a Stytch outage, so that a third-party incident does not stop my hiring.

#### Acceptance Criteria

1. THE Stytch_Adapter SHALL apply a request timeout of at most 5 seconds to every outbound Stytch call.
2. IF a Stytch call fails with a network error or a status in the range 500 through 599, THEN THE Stytch_Adapter SHALL retry that call at most 2 additional times with exponentially increasing delays, and SHALL retry zero calls that create or modify state without an idempotency value.
3. IF a Stytch call returns HTTP status 429, THEN THE Stytch_Adapter SHALL retry after the delay Stytch reports and SHALL make at most 3 total attempts.
4. IF every attempt of a Stytch call fails, THEN THE endpoint that initiated the call SHALL respond with HTTP status 503, a machine-readable code of `identity_provider_unavailable`, and a message inviting the caller to retry.
5. WHILE Stytch is unreachable, THE Session_Resolver SHALL continue to accept Stytch session JWTs that verify locally against the cached Stytch JSON Web Key Set and remain within their validity period.
6. THE Session_Resolver SHALL cache the Stytch JSON Web Key Set for at least 1 hour and SHALL continue to serve the last successfully fetched key set while a refresh fails.
7. WHILE Stytch is unreachable, THE Fundis_Backend SHALL continue to serve every read of workspace-scoped data to callers holding a locally verifiable Stytch_Session.
8. WHILE Stytch is unreachable, THE Onboarding_Service SHALL respond with HTTP status 503 for every new signup request and SHALL create zero partial `users` documents.
9. THE Fundis_Backend SHALL record one error-severity log per Stytch call that exhausts every retry, naming the operation and the final status, naming the Workspace identifier for every such call issued under a resolved Workspace_Session_Context, and excluding every Stytch secret.
10. THE Fundis_Backend SHALL read the Stytch project identifier, secret, public token, environment, and webhook signing secret exclusively from environment variables and SHALL declare each of those variables in `.env.example`.
11. IF a required Stytch environment variable is unset or empty after whitespace removal at startup, THEN THE Fundis_Backend SHALL fail startup with one error-severity log naming that variable and SHALL apply zero Stytch calls.
12. THE Fundis_Backend SHALL exclude every Stytch secret, session token, and Invitation_Token from every log entry and from every error response body.
13. THE Fundis_Backend SHALL expose a health indicator reporting the outcome of the most recent Stytch call and the time of that call.

### Requirement 21: Migrate Existing Employer Accounts To Stytch

**User Story:** As an existing Employer with live vacancies, I want to move to the new sign-in without losing access, so that my recruitment continues uninterrupted.

#### Acceptance Criteria

1. THE Fundis_Backend SHALL gate Employer Stytch authentication behind an environment variable and SHALL apply Legacy_Auth for Employers while that variable disables Stytch authentication.
2. THE Migration_Job SHALL identify every Legacy_Employer and SHALL create, for each Legacy_Employer holding a non-empty email address, one Stytch_Organization, one Workspace, one Membership with Workspace_Role `owner`, and one Account_Link.
3. THE Migration_Job SHALL derive each migrated Workspace_Name from the name of the Legacy_Employer's most recently created Company, and SHALL use the Legacy_Employer's first and last name joined by one space where that Legacy_Employer owns zero Companies.
4. THE Migration_Job SHALL apply the re-scoping stated in Requirement 6 criteria 1 through 3 for every migrated Legacy_Employer.
5. WHEN the Migration_Job runs a second time over an already-migrated Legacy_Employer, THE Migration_Job SHALL apply zero additional Stytch_Organizations, Workspaces, Memberships, and Account_Links.
6. IF a Legacy_Employer holds an empty email address, THEN THE Migration_Job SHALL create zero Stytch_Organizations for that Legacy_Employer and SHALL list that Legacy_Employer in a report of accounts requiring an email address.
7. IF two Legacy_Employers hold email addresses that are equal after Email_Normalization but differ in stored form, THEN THE Migration_Job SHALL create zero Stytch_Organizations, zero Workspaces, and zero Account_Links for any of the colliding documents, SHALL list every such collision in a report naming every colliding document identifier, and SHALL require a platform administrator to resolve each collision (by choosing which document to migrate, or migrating none) before that collision's Legacy_Employers are migrated. THE Migration_Job SHALL NOT select a "winner" automatically, because an automatic choice by recency can silently orphan the data owned by the other colliding document.
8. THE Migration_Job SHALL support a dry-run mode that produces the full report of intended changes and applies zero writes to Stytch and zero writes to MongoDB.
9. THE Migration_Job SHALL process Legacy_Employers in batches whose size is configurable and SHALL apply at most 10 Stytch state-changing calls per second.
10. WHILE the Employer Stytch authentication variable enables Stytch authentication, THE Fundis_Backend SHALL accept both a valid Stytch_Session and a valid `jwtutil` token for a migrated Employer, and SHALL resolve the Workspace_Session_Context identically for both.
11. WHEN Legacy_Auth for Employers is disabled by configuration, THE Fundis_Backend SHALL respond to a `POST /api/v1/login` request naming a migrated Employer with HTTP status 409, a machine-readable code of `use_stytch_sign_in`, and the Stytch sign-in location.
12. THE Fundis_Backend SHALL support reverting to Legacy_Auth for Employers by changing the environment variable alone, for at least 60 days after the Migration_Job completes, for every Legacy_Employer who had a usable password before migration: the environment-variable flip alone restores their old sign-in path with zero data loss to their Fundis records (the Stytch_Organization, Workspace, Membership, and Account_Link records created by migration are left in place, unused, rather than deleted). Reverting is NOT a full undo of the migration and does not delete or roll back any created Stytch or Workspace record — it only stops routing sign-in through Stytch. THE Fundis_Backend SHALL document, as a known and accepted limitation, that a teammate whose Membership was created by an Invitation accepted after the Migration_Job ran never had a Fundis password and therefore cannot sign in through Legacy_Auth after a revert; such a teammate requires manual intervention (for example, a password-reset flow scoped to this migration) to regain access.
13. WHEN the Migration_Job creates a Workspace for a Legacy_Employer, THE Fundis_Backend SHALL send an email to that Legacy_Employer's address naming the new Workspace_Name and the new sign-in method.
14. THE Migration_Job SHALL apply zero Stytch_Organization creations for `users` documents whose `user_type` differs from `customer`.
15. THE Migration_Job SHALL apply zero Stytch_Organization creations for a `customer` `users` document that owns zero `companies` documents and zero `job_vacancies` documents, so that hiring customers who never posted a vacancy remain on Legacy_Auth.
16. THE Migration_Job SHALL record, for every processed Legacy_Employer, the outcome and the created identifiers, and SHALL record the failure reason for every processed Legacy_Employer whose outcome is a failure.

### Requirement 22: Keep Non-Employer Authentication Unchanged

**User Story:** As an artisan with the mobile app installed, I want my existing login to keep working, so that this employer-side change does not lock me out of finding work.

#### Acceptance Criteria

1. THE Fundis_Backend SHALL continue to serve `POST /api/v1/register` for `user_type` values `customer` and `artisan` with the request and response shapes it serves today.
2. THE Fundis_Backend SHALL continue to serve `POST /api/v1/login` for every `users` document holding zero Account_Links.
3. THE Fundis_Backend SHALL continue to accept every unexpired `jwtutil` token issued before this feature ships, for the duration stated in Requirement 21 criterion 12.
4. THE Fundis_Backend SHALL apply zero changes to the artisan SMS OTP verification flow, the password reset flow, and the social login flow for `user_type` `artisan`.
5. THE Fundis_Backend SHALL apply zero Membership requirements to any endpoint that an artisan calls.
6. WHERE a `users` document holds `user_type` equal to `artisan` and an email address equal, after Email_Normalization, to that of an Employer, THE Fundis_Backend SHALL keep both documents functional and SHALL apply Requirement 4 criterion 9, which withholds the Account_Link option for the artisan document. This is a known launch limitation, accepted deliberately rather than by omission: a person who is both an artisan and an employer under the same email cannot link the two accounts and must use two Fundis accounts (optionally with different emails for the employer side) for as long as they hold both roles. Allowing that link is out of scope for this feature; see the "Out Of Scope" section.
7. THE Fundis_Backend SHALL continue to serve `middleware.IsArtisan`, `middleware.IsCustomer`, `middleware.IsAssessor`, and `middleware.IsAdmin` for the endpoints that use those guards today.
8. WHEN an artisan applies to a vacancy belonging to a Workspace, THE Fundis_Backend SHALL apply the existing application limits and subscription checks against that artisan's own `users` document.
9. THE Fundis_Backend SHALL notify the Workspace members holding Permission `application.view` when an application arrives, in place of notifying the single vacancy `owner_id` alone.

### Requirement 23: Audit Privileged Workspace Actions

**User Story:** As a Workspace Owner, I want a record of who invited, promoted, or removed whom, so that I can answer questions about how someone got access.

#### Acceptance Criteria

1. WHEN any of the following occurs, THE Audit_Logger SHALL write exactly one Audit_Log_Entry: Workspace creation, Workspace rename, Workspace deletion state change, Invitation creation, Invitation revocation, Invitation acceptance, Membership creation, Workspace_Role change, Membership_Status change, ownership transfer, Account_Link creation, Account_Link removal, and billing payment method change.
2. THE Audit_Logger SHALL record on every Audit_Log_Entry the acting user identifier, the Workspace identifier, the action name, the target identifier, the request IP address, and the time in UTC.
3. THE Audit_Logger SHALL write Audit_Log_Entry records append-only, and THE Fundis_Backend SHALL expose zero endpoints that modify or delete an Audit_Log_Entry.
4. WHEN a caller holding Permission `audit.view` lists Audit_Log_Entry records for the active Workspace, THE Fundis_Backend SHALL return those records in reverse chronological order with pagination.
5. THE Fundis_Backend SHALL return zero Audit_Log_Entry records belonging to any Workspace other than the one named by the request.
6. THE Fundis_Backend SHALL retain every Audit_Log_Entry for at least 2 years.
7. THE Audit_Logger SHALL exclude every password hash, session token, Invitation_Token, and full payment instrument number from every Audit_Log_Entry.
8. IF writing an Audit_Log_Entry fails, THEN THE Fundis_Backend SHALL complete the underlying action, SHALL record one error-severity log naming the action and the target, and SHALL apply zero request failures attributable to the audit write.
9. WHERE an action is performed by a platform administrator rather than by a Workspace member, THE Audit_Logger SHALL record the platform administrator identifier and SHALL mark the Audit_Log_Entry as administrator-initiated.
10. THE Audit_Logger SHALL record the previous value and the new value on every Audit_Log_Entry whose action changes a stored value, and SHALL record zero previous and new values on every Audit_Log_Entry whose action changes no stored value.

### Requirement 24: Scope Billing To The Workspace

**User Story:** As a Workspace Owner, I want my accountant to manage the subscription and invoices without seeing applicant data, so that finance work does not require broad access.

#### Acceptance Criteria

1. THE Billing_Service SHALL associate every Employer subscription, invoice, payment method, and payment attempt created after this feature ships with exactly one Workspace identifier.
2. WHEN a caller holding Permission `subscription.manage` purchases or cancels an Employer subscription, THE Billing_Service SHALL apply that change to the active Workspace rather than to the calling user.
3. THE Billing_Service SHALL apply every Workspace entitlement derived from a subscription to every `active` Membership in that Workspace.
4. WHEN a Membership is removed, THE Billing_Service SHALL retain the Workspace subscription unchanged.
5. WHEN the paying Membership is removed, THE Billing_Service SHALL retain the recorded payment method and SHALL notify every Membership holding Permission `billing.manage` that the paying member has left.
6. THE Permission_Guard SHALL deny every request to a billing endpoint from a Membership holding Workspace_Role `hr`, with HTTP status 403.
7. THE Permission_Matrix SHALL grant Workspace_Role `finance` zero vacancy or application Permissions (see Requirement 7 criterion 6), so THE Permission_Guard denies every vacancy write and application read request from a Membership holding Workspace_Role `finance` through the ordinary allow-list check rather than through a separate deny rule. P2 covers this as a correctness property.
8. WHEN a caller holding Permission `billing.view` reads billing history for the active Workspace, THE Billing_Service SHALL return every invoice, payment, and subscription record belonging to that Workspace and zero records belonging to any other Workspace.
9. THE Billing_Service SHALL name the Workspace_Name on every invoice it generates for a Workspace.
10. WHERE a `user_subscriptions` document holds zero `workspace_id` values, THE Billing_Service SHALL continue to apply that subscription to the individual named by its `user_id`.
11. WHEN a Workspace subscription lapses, THE Fundis_Backend SHALL notify every Membership holding Permission `billing.manage` and SHALL apply the existing lapsed-subscription restrictions at Workspace scope.

### Requirement 25: Bound Abuse Of Onboarding And Invitation Endpoints

**User Story:** As a platform operator, I want signup and invitation endpoints rate-limited, so that a script cannot mint thousands of workspaces or use our email reputation to spam.

**Rate-limit strategy.** Every rate limit in this document (Requirement 10 criteria 12 and 13, Requirement 12 criterion 4, Requirement 15 criterion 13, and this requirement's own criteria 1, 2, and 6) is an instance of one strategy: a named limit has a dimension (the key the count is scoped to — IP address, email address, user identifier, Workspace identifier, or Invitation identifier), a window size, and a maximum count. The table below is the complete set at launch; a new limit is added to this table rather than invented ad hoc in a requirement's prose.

| Named limit | Dimension | Window | Max |
|---|---|---|---|
| Employer signup initiation | IP address | 10 minutes | 5 |
| Employer signup initiation | email address | 10 minutes | 3 |
| Invitation creation | Workspace | 1 hour | 20 |
| Invitation resend | Invitation | 1 hour | 3 |
| Invitation_Token resolution | IP address | 1 hour | 30 |
| Additional Workspace creation | Employer (identified by `users` document once one exists, otherwise by Stytch `member_id`) | 24 hours | 5 |

#### Acceptance Criteria

1. THE Rate_Limiter SHALL accept at most 5 Employer signup initiations per IP address per 10-minute period, and SHALL respond with HTTP status 429 for further initiations within that period.
2. THE Rate_Limiter SHALL accept at most 3 Employer signup initiations per email address per 10-minute period, and SHALL respond with HTTP status 429 for further initiations within that period.
3. THE Rate_Limiter SHALL respond with HTTP status 429 and a `Retry-After` header naming the remaining seconds for every request it rejects.
4. THE Onboarding_Service SHALL return an identical response body and an identical HTTP status for a signup initiation naming an email address that exists in the `users` collection and for one naming an email address that does not, so that account existence is not disclosed to an unauthenticated caller.
5. THE Onboarding_Service SHALL disclose the existence of a Link_Candidate only after Stytch reports a verified authentication for the matching email address.
6. THE Rate_Limiter SHALL accept at most 30 Invitation_Token resolution attempts per IP address per hour, and SHALL respond with HTTP status 429 for further attempts within that hour.
7. THE Fundis_Backend SHALL apply the existing Cloudflare Turnstile verification to the Employer signup initiation endpoint and to the Invitation acceptance endpoint.
8. THE Fundis_Backend SHALL record one warning-severity log for every Rate_Limiter rejection, naming the endpoint, the limit that was reached, and the IP address.
9. THE Fundis_Backend SHALL apply the Rate_Limiter limits per instance and SHALL apply the limits through a shared store where the deployment runs more than one instance.

## Correctness Properties For Property-Based Testing

These properties are the machine-checkable core of the requirements above. Each one holds for arbitrary generated inputs and is a candidate for a property-based test with at least 100 iterations. All of them exercise Fundis code against an in-memory or in-process fake of Stytch, so the cost per iteration stays low. Stytch itself is never called from a property test.

**P1 — The Permission_Matrix is a total function with an Owner superset (invariant, Requirement 7 criteria 1 through 6 and 18).** For an arbitrary Workspace_Role drawn from the four defined values and an arbitrary Permission drawn from the defined Permission set, the matrix lookup returns a boolean without panic, and the Permission set granted to `owner` is a superset of the union of the sets granted to `admin`, `hr`, and `finance`.

**P2 — Restricted Permissions are never reachable from a non-Owner Workspace_Role (invariant, Requirement 7 criteria 7, 8, and 10 through 12).** For an arbitrary Workspace_Role other than `owner`, the granted Permission set excludes `workspace.delete` and `ownership.transfer`, and for an arbitrary Workspace_Role other than `owner` and `finance`, the granted Permission set excludes `billing.manage`, `payment_method.manage`, `invoice.pay`, and `subscription.manage`.

**P3 — Email_Normalization is idempotent and case-collapsing (idempotence, Requirement 4 criteria 1 and 7).** For an arbitrary string, `normalize(normalize(s))` equals `normalize(s)`, and for arbitrary strings that differ only in letter case or in surrounding whitespace, the normalized values are equal, while arbitrary strings differing by a plus-suffix or by dot placement in the local part yield unequal normalized values.

**P4 — Workspace_Slug derivation is idempotent and yields a valid slug (idempotence and invariant, Requirement 2 criteria 7 through 9).** For an arbitrary Workspace_Name, including names composed entirely of emoji, Arabic script, punctuation, or whitespace, the derived base slug contains only lowercase letters, digits, and hyphens, contains no leading or trailing hyphen, contains no repeated hyphen, contains at least one character, and satisfies `derive(derive(name)) == derive(name)`.

**P5 — Slug allocation never collides and never skips a suffix (invariant, Requirement 2 criteria 8 and 10).** For an arbitrary sequence of Workspace_Names, including sequences containing many names that derive to the same base slug, every successfully created Workspace holds a distinct Workspace_Slug, every Workspace_Slug starts with its derived base slug, and the sequence of suffixed slugs allocated for one base slug is exactly the bare base slug followed by `-2`, `-3`, `-4`, ... with no gap and no `-1`.

**P6 — Workspace_Name validation accepts exactly the specified range (model-based, Requirement 2 criteria 2 through 5).** For an arbitrary string, the validator accepts the string if and only if the trimmed length lies in the closed interval 2 through 80 and the trimmed string contains at least one Unicode letter or number, which is a two-line reference model.

**P7 — Onboarding provisioning is idempotent (idempotence, Requirement 3 criteria 3, 4, 10, and 12).** For an arbitrary number of concurrent and sequential Workspace_Name submissions against one Onboarding_State record, the resulting counts of Stytch_Organizations, Workspace documents, Membership documents, and Account_Link documents each equal exactly 1.

**P8 — Onboarding is resumable from any interruption point (invariant, Requirement 3 criteria 5, 6, and 11).** For an arbitrary injected failure point in the provisioning sequence, replaying the flow to completion yields exactly one Workspace, exactly one `owner` Membership, and zero orphaned Stytch_Organizations that lack a Workspace document.

**P9 — Every Workspace has exactly one Owner (invariant, Requirement 14 criteria 1, 2, 11, and 12).** For an arbitrary sequence of Membership operations drawn from invite, accept, role change, suspend, remove, reinstate, self-remove, and ownership transfer, every Workspace that is not scheduled for deletion holds exactly one Membership with Workspace_Role `owner` and Membership_Status `active` at every observation point.

**P10 — Ownership transfer conserves the Membership count and swaps exactly two roles (invariant, Requirement 14 criteria 3, 4, and 8).** For an arbitrary Workspace holding an arbitrary set of Memberships, an ownership transfer either fails and leaves every Workspace_Role unchanged, or succeeds and changes exactly two Workspace_Roles while leaving the Membership count and the identity set unchanged.

**P11 — Invitation state transitions follow the declared machine (model-based, Requirement 12 criteria 1, 2, 3, 5, 7, and 9).** For an arbitrary sequence of invitation operations drawn from create, resend, revoke, decline, accept, and clock advance, the Invitation_Status equals the status produced by a five-state reference transition table, and no operation moves an Invitation out of `accepted`, `declined`, or `revoked`.

**P12 — An Invitation_Token is single-use and grants exactly one Membership (invariant, Requirement 11 criteria 3, 11, and 17).** For an arbitrary number of concurrent and sequential presentations of one Invitation_Token by the correctly matching email address, exactly one Membership is created in the target Workspace.

**P13 — Invitation acceptance never grants a role other than the offered one (invariant, Requirement 10 criterion 3 and Requirement 11 criteria 3 and 4).** For an arbitrary Invitation and an arbitrary authenticated email address, either zero Memberships are created, or exactly one Membership is created whose Workspace_Role equals the offered Workspace_Role, which is never `owner`.

**P14 — A user holds at most one Membership per Workspace (invariant, Requirement 15 criteria 1 and 2).** For an arbitrary interleaving of invitations, acceptances, removals, and reinstatements involving one email address and one Workspace, the count of Membership documents for that pair never exceeds 1.

**P15 — Workspace switching yields only the target Workspace's authority (invariant, Requirement 15 criteria 7, 8, and 11).** For an arbitrary user holding an arbitrary set of Memberships across Workspaces and an arbitrary sequence of switch requests, the resolved Permission set always equals the Permission_Matrix entry for the Membership belonging to the active Workspace, and equals the empty set for any Workspace in which the user holds no `active` Membership.

**P16 — Tenancy isolation holds for every generated access pattern (invariant, Requirement 9 criteria 2 through 4).** For arbitrary pairs of Workspaces populated with arbitrary Companies, vacancies, and applications, every read and every write issued under one Workspace's Workspace_Session_Context returns or modifies zero documents belonging to the other Workspace, and every cross-Workspace attempt yields HTTP status 404.

**P17 — A suspended or removed Membership is denied every workspace-scoped Permission (invariant, Requirement 8 criteria 7 through 9 and Requirement 13 criterion 5).** For an arbitrary Workspace_Role and an arbitrary Permission, a Membership whose Membership_Status is `suspended` or `removed`, or whose `users` document `status` is `suspended`, is denied.

**P18 — Authority_Version increases monotonically and exactly once per authority change (invariant, Requirement 13 criteria 1, 2, 11, and 12, and Requirement 17 criterion 1).** For an arbitrary sequence of Membership operations, the Authority_Version never decreases and increases by exactly 1 for each operation that changes the Workspace_Role or the Membership_Status, and by 0 for every operation that changes neither.

**P19 — Session_Resolver caching never serves stale authority beyond the bound (metamorphic, Requirement 16 criteria 7 and 8, and Requirement 17 criteria 2, 8, and 9).** For an arbitrary interleaving of permission changes and requests against a controllable clock, every resolved Workspace_Session_Context matches the stored Membership whenever the Authority_Version differs from the cached one, and the interval during which a superseded Workspace_Role is applied never exceeds 30 seconds of simulated time.

**P20 — Webhook processing is idempotent under duplication (idempotence, Requirement 19 criteria 4, 5, and 10).** For an arbitrary sequence of Stytch_Webhook_Events processed in one fixed receipt order, inserting arbitrary duplicate deliveries of events already present in that sequence, at arbitrary positions, yields the same final Membership and Workspace state as processing the sequence without those duplicates.

**P21 — Webhook signature verification rejects every tampered payload (error conditions, Requirement 19 criteria 1 through 3).** For an arbitrary payload and an arbitrary single-byte mutation of the payload or of the signature, verification fails, zero state changes are applied, and the handler never panics.

**P22 — Account_Link uniqueness holds in both directions (invariant, Requirement 5 criterion 8).** For an arbitrary interleaving of link attempts across arbitrary `users` documents and Stytch_Members, every `users` document is referenced by at most one Account_Link and every Stytch_Member is referenced by at most one Account_Link.

**P23 — Linking never creates a duplicate user (invariant, Requirement 4 criteria 4 and 6, and Requirement 5 criterion 12).** For an arbitrary sequence of signup and link attempts using an arbitrary set of email addresses that vary only in letter case and surrounding whitespace, the count of `users` documents whose normalized email equals a given value never exceeds the count that existed before the sequence plus 1.

**P24 — Data re-scoping is idempotent and lossless (idempotence and invariant, Requirement 6 criteria 1 through 5).** For an arbitrary set of Companies, vacancies, and subscriptions owned by one user, applying re-scoping twice yields the same documents as applying it once, the document count is unchanged, and every original `owner_id` value is preserved.

**P25 — Link_Challenge attempt limits are enforced exactly (invariant, Requirement 5 criteria 4 through 6).** For an arbitrary sequence of correct and incorrect Link_Challenge submissions against a controllable clock, zero Account_Links are created before a correct submission within the validity window, and the count of accepted submissions per Link_Candidate per hour never exceeds 5.

**P26 — Rate limits never admit more than the stated count (invariant, Requirement 10 criteria 12 and 13, Requirement 12 criterion 4, Requirement 15 criterion 13, and Requirement 25 criteria 1, 2, and 6).** For an arbitrary burst of requests against a controllable clock, the count of admitted requests in any window never exceeds the configured limit, and every rejection carries HTTP status 429 with a `Retry-After` header.

**P27 — Signup responses do not disclose account existence (invariant, Requirement 25 criteria 4 and 5).** For an arbitrary email address and an arbitrary population of the `users` collection, the response body and status of a signup initiation are byte-identical whether or not a matching `users` document exists.

**P28 — Secrets never leave the process (invariant, Requirement 20 criterion 12 and Requirement 23 criterion 7).** For arbitrary Stytch secrets, session tokens, and Invitation_Tokens, no log line, no error response body, and no Audit_Log_Entry contains any of those values.

**P29 — Every registered workspace-scoped endpoint declares a Permission (invariant, Requirement 8 criteria 5 and 6).** For the full set of routes the Fundis_Backend registers, every route inside a workspace-scoped group maps to exactly one declared Permission that exists in the Permission_Matrix.

**P30 — Stytch failure never leaves a partial account (invariant, Requirement 20 criteria 4 and 8, and Requirement 11 criterion 10).** For an arbitrary injected Stytch failure at an arbitrary call site during signup or invitation acceptance, the resulting state contains either a complete account with a Workspace and an `owner` Membership, or an Onboarding_State record that can be resumed, and contains zero Memberships without a corresponding Stytch_Member for a successful path.

### Behavior Better Suited To Example-Based Tests

The following behaviors vary little with input, depend on Stytch itself rather than on Fundis logic, or cost too much to run 100 times. Each is covered by one to three representative integration or unit tests instead: the Stytch email magic link and Google OAuth round trips against the Stytch test environment; the contents and rendering of the invitation, removal, role-change, link-notification, and deletion-warning emails; the Turnstile verification wiring; the startup failure on a missing Stytch environment variable; the Stytch JSON Web Key Set fetch and its cache-on-failure behavior; the health indicator payload; the deletion confirmation copy and the 7-day retention window; the operational orphan report; the Migration_Job dry-run report format; and the Fundis_Frontend Workspace selector and invitation-mismatch sign-out prompt.

## Out Of Scope

The following are deliberately excluded from this feature and are candidates for later specs: SAML and OIDC enterprise single sign-on; SCIM directory provisioning; multi-factor authentication policy per Workspace; custom or per-Workspace roles beyond the four defined Workspace_Roles; migration of artisans, assessors, and platform administrators to Stytch; merging two existing Workspaces; moving a Company from one Workspace to another; per-member spending limits inside the Finance role; and phone-number-based sign-in through Stytch.
