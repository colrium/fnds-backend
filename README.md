# Fundis Backend

Go API for **Fundis**, a platform connecting artisans (skilled workers) with customers in Kenya.
Migrated from a Laravel/PHP/MySQL application; `INVENTORY.md` tracks what has and has not moved.

**Stack:** Go 1.26 · [Echo v4](https://echo.labstack.com/) · MongoDB · ECS Fargate

> `CLAUDE.md` holds the working agreements for this repo (architecture rules, conventions).
> This file is the practical guide: how to run it, how auth works, how to ship it.

---

## Quick start

```bash
cp .env.example .env      # then fill in the values below
make docker-run           # start MongoDB
make run                  # or: make dev  (live reload via air)
```

The API listens on `PORT` (default 8080) and answers `GET /health`.

### Make targets

| Target | What it does |
|---|---|
| `make build` | Compile to `./main` |
| `make run` | `go run .` |
| `make dev` | Live reload; prompts to install `air` if it is missing |
| `make test` | `go test ./...` |
| `make lint` | `go vet ./...` |
| `make format` / `format-check` | `gofmt -w .` / fail if unformatted |
| `make docker-run` / `docker-down` | MongoDB and supporting services |

Run one package or one test:

```bash
go test ./workspaces/... -v
go test ./auth -run TestHandleUserLogin -v
```

`make test` runs packages in parallel. Some suites share the mocked-Mongo client, so when
debugging a cross-package failure use `go test -p 1 ./...` — that is also what CI runs.

---

## Architecture

`main.go` builds everything explicitly: it loads config, connects Mongo via `configs.ConnectDB()`,
constructs services, and registers route groups. There is no DI container — if you want to know what
depends on what, read `main.go` top to bottom.

### Route groups

Each domain package exports `RegisterPublicRoutes` and/or `RegisterAuthRoutes`:

- **Public** — no credential required.
- **Authenticated** — behind `middleware.JWTAuth`, plus a role guard where relevant
  (`IsAdmin`, `IsArtisan`, `IsCustomer`, `IsAssessor`).
- **Workspace-scoped** — employer routes behind `workspaces.Resolver`, which additionally
  resolves the caller's workspace and permissions.

### Packages

Domain packages are organised by business capability, not by layer:

```
auth  users  profiles  jobrequests  quotations  payments  transactions
subscriptions  reviews  notifications  banks  institutions  locations
skills  chat  documenttypes  applications  referrals  files  admin
assessments  scoring  cvparser  invitations  onboarding  workspaces
```

Supporting packages:

| Package | Role |
|---|---|
| `configs` | Mongo handle (`configs.MI.DB`), env accessors |
| `middleware` | `JWTAuth`, role guards, request logging, caching |
| `responses` | Response envelopes — `SendOK`, `SendCreated`, `SendError` |
| `contextutil` | Per-request user ID |
| `jwtutil` | Legacy HS256 token issue/validate |
| `stytch` | Stytch B2B adapter, retry policy, and a `Fake` for tests |
| `workspaces` | Employer workspaces, memberships, permissions, session resolver |
| `onboarding` | Employer sign-up state machine |
| `identity`, `audit`, `ratelimit`, `logging`, `timeutil` | Cross-cutting infrastructure |
| `jobs` | Hourly scheduler (expire subscriptions, close vacancies, retention) |

### Data model

MongoDB with **UUID string `_id`** values, not ObjectIDs. Collections are reached through
`configs.MI.DB.Collection("name")`. There is no ORM and no migration tool — indexes are created
idempotently at startup by each package's `EnsureIndexes`.

### Responses

Every endpoint returns the same envelope, so clients parse one shape:

```go
responses.SendOK(c, data, "message")       // 200
responses.SendCreated(c, data, "message")  // 201
responses.SendError(c, 400, "message")     // error
```

Errors that clients branch on carry a stable machine-readable code as well:

```go
responses.SendErrorWithCode(c, 409, "workspace_limit_reached", "…", field, action, details)
```

Prefer `SendErrorWithCode` for anything the UI must react to differently — clients should switch on
`error.code`, never on the human-readable message.

---

## Authentication

Four user types drive the role guards: `admin`, `artisan`, `customer`, `assessor`.
Employers are `customer` documents.

**Two credential systems coexist**, and the two middlewares try them in opposite
orders. This is the single most surprising thing about the codebase, so it is
worth reading before touching auth:

| Credential | Shape | Issued by | Verified by |
|---|---|---|---|
| Stytch session JWT | RS256, JWT | Stytch, on employer B2B sign-in | Locally, against cached JWKS — no network |
| Legacy JWT | HS256, JWT | `jwtutil`, on password/OTP login | `jwtutil.ValidateToken` — local |
| Stytch session token | opaque | Stytch | One remote call to Stytch |

`workspaces.Resolver` (`authenticateHeader`) dispatches on the credential's
**shape**, not down a single ordered chain — worth being precise about, because it
is easy to mis-describe:

- **JWT-shaped** (exactly two dots): Stytch RS256 first, since it needs no
  network, then legacy HS256. If neither verifies, the request fails — an opaque
  lookup is never attempted for a JWT-shaped token.
- **Anything else**: treated as an opaque Stytch session token and authenticated
  remotely.

Whichever succeeds resolves to a `(user_id, workspace_id, role, permissions)`
context.

`middleware.JWTAuth` guards the older routes and deliberately uses the **opposite**
order: legacy HS256 first, then the resolver chain as a fallback. Legacy is the
credential every artisan, customer, assessor, and admin holds, so trying it first
keeps the common path free of any Stytch work. The fallback is what lets an
employer holding only a Stytch session reach pre-workspace endpoints such as
`/notifications` — without it they authenticate successfully and then get a 401
from every one of them.

The Resolver answers every failure with one fixed `"Unauthorized"` body, so no
failure mode reveals which credential form was tried. `JWTAuth` predates that
rule and still distinguishes a missing header from a malformed one from an
unusable token — useful while debugging, but it means the two guards are not
interchangeable if you are relying on opaque failures.

Two feature flags control the employer rollout:

- `STYTCH_EMPLOYER_AUTH_ENABLED` — master switch. When false, the five `STYTCH_*` variables are
  optional and every employer stays on password/OTP.
- `LEGACY_EMPLOYER_AUTH_ENABLED` — keeps `POST /api/v1/login` working for employers who already
  hold an `account_links` record. **This is the rollback switch.** Set it to false only after a
  migration is verified; reverting to true needs no data change. Note the app treats an *unset*
  value as false, so it must be set explicitly in every environment.

---

## Configuration

`cp .env.example .env`. That file is the authoritative list and documents each variable inline;
the essentials are:

| Variable | Notes |
|---|---|
| `APP_ENV` | `development` / `staging` / `production` |
| `MONGODB_URI`, `PORT` | |
| `SESSION_KEY` | HS256 signing key. Employer onboarding tickets additionally require ≥32 bytes, and fail at mint time (not startup) if it is shorter |
| `AWS_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY` / `AWS_REGION` | |
| `AWS_S3_BUCKET`, `AWS_S3_REGION` | Set `AWS_S3_REGION` only when the bucket is in a different region from the service; a mismatch fails presigned uploads with an opaque CORS-looking error |
| `STYTCH_PROJECT_ID`, `STYTCH_SECRET`, `STYTCH_PUBLIC_TOKEN`, `STYTCH_ENV`, `STYTCH_WEBHOOK_SECRET` | All five required when Stytch employer auth is on; startup fails naming the first missing one |
| `SENDGRID_*`, `ADVANTA_*` | Email and SMS/OTP delivery |
| `REDIS_URL`, `NOTIFICATIONS_CACHE_*` | Optional notifications cache; Mongo stays authoritative |
| `TURNSTILE_SECRET_KEY` | Required in production |

Never log or echo `STYTCH_SECRET`, `STYTCH_WEBHOOK_SECRET`, `SESSION_KEY`, or any AWS credential.

---

## Deployment

Normal path: **push to `main` → GitHub Actions builds, pushes to ECR, and runs `terraform apply`.**
Infrastructure lives in `infra/` with per-environment values in `infra/env/*.tfvars`.

Terraform reads the image to deploy from SSM (`/fundis/<env>/backend/image`), stored as a digest so
the reference is immutable. CI writes that parameter before applying.

### When Actions cannot run

`scripts/deploy-manual.sh <staging|production>` reproduces the workflow when CI is unavailable. It
verifies (build, vet, gofmt, full tests), refuses a dirty tree, confirms before production, and
prints the rollback command if the rollout fails.

It deliberately **skips `terraform apply`**, which needs ~20 `TF_VAR_*` secrets held only in GitHub
Environments — without them `infra`'s `unset_secrets_check` precondition aborts, and that guard is
what stops blank values overwriting real secrets. So the script clones the running task definition
and swaps only the image:

> **Pending `infra/` changes do not ship through the script — only code does.** Run the real
> workflow once CI is healthy to pick up any infrastructure change.

It writes the SSM image parameter, so the next `terraform apply` converges on the same image instead
of rolling the service back.

Rollback is always the previous task definition:

```bash
aws ecs update-service --cluster fundis-staging-cluster \
  --service fundis-staging-backend --task-definition fundis-staging-backend:<N> \
  --region af-south-1
```

---

## Testing

Standard `go test`; no build tags and no external test runner.

- **Mocked Mongo** — `mtest` with `ClientType(mtest.Mock)`, swapping `configs.MI` and restoring it
  in `t.Cleanup`. Mock responses are consumed **in order**, so adding a query to production code
  breaks any test whose script no longer lines up.
- **Stytch** — `stytch.NewFake(clock)` records every call and can inject failures per attempt via
  `FailAt`. Assert on the recorded call arguments, not just the outcome: the Fake is permissive by
  design and will happily accept parameters real Stytch rejects.
- **Time** — inject a clock. Never call `time.Now()` in code under test.

Where a behaviour is a guard against a specific past failure, say so in the test comment. Several
existing tests exist only because production once shipped the opposite behaviour.

---

## Conventions

- `gofmt` clean, `go vet` clean. CI enforces both.
- Comments explain **why**, not what. A comment restating the function name is noise; a comment
  naming the failure mode a line prevents is the point.
- Errors wrap with context (`fmt.Errorf("insert membership: %w", err)`).
- Never swallow an error to reach a fallback without logging the reason first.
- Config values belong in `.env` and `configs`, not inline constants — but genuine invariants
  (retention floors, retry caps) belong in code as named constants.
