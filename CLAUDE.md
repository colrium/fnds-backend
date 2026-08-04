# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Go backend API for **Fundis** - a platform connecting artisans (skilled workers) with customers in Kenya. Originally a Laravel/PHP/MySQL application, it has been migrated to Go with Echo framework and MongoDB.

## Build & Development Commands

```bash
# Build the application
make build
# Or: go build -o main .

# Run the application
make run
# Or: go run .

# Run with live reload (development)
make dev

# Run all tests
make test
# Or: go test ./... -v

# Run tests for a specific package
go test ./auth -v
go test ./users -v

# Run a single test
go test ./auth -v -run TestHandleUserLogin

# Clean build artifacts
make clean
```

## Environment Setup

Copy `.env.example` to `.env` and configure:
- `APP_ENV` - Application environment (development/production)
- `MONGODB_URI` - MongoDB connection string
- `PORT` - Server port (default: 8080)
- `SESSION_KEY` - JWT secret key
- `PAYPACK_CLIENT_ID` / `PAYPACK_CLIENT_SECRET` - Payment integration
- `USE_PLUNK` - Email service key
- `TELEGRAM_BOT_ID` / `TELEGRAM_CHAT_ID` - Telegram notifications

### Redis Notifications Cache Rollout

The authenticated `GET /api/v1/notifications` endpoint is backed by a Redis cache-aside decorator (`notifications.CachedStore`). MongoDB stays authoritative; Redis is purely a performance layer. Env vars: `NOTIFICATIONS_CACHE_ENABLED` (default `true`), `NOTIFICATIONS_CACHE_TTL` (default `30s`, range `5s`–`5m`), `NOTIFICATIONS_CACHE_OP_TIMEOUT` (default `100ms`, range `10ms`–`1s`).

**Rollout sequence:**
1. Deploy with `NOTIFICATIONS_CACHE_ENABLED=false`. Confirm unchanged endpoint behavior and mutation traffic (identical status codes, messages, and JSON shape).
2. Enable in staging with a short TTL. Exercise every invalidation path — create, mark-read, mark-all-read — and simulate a Redis outage to confirm fail-open behavior.
3. Enable on a production canary instance or a limited traffic slice before a full rollout.
4. Roll back instantly by setting `NOTIFICATIONS_CACHE_ENABLED=false` — no Redis flush is required. Versioned, expiring entries are harmless while disabled.

**What to monitor:** cache hit ratio, Redis operation errors, MongoDB `List`/`CountDocuments` call rate on this endpoint, invalidation failures (`notifications cache operation failed operation=invalidation`), Redis memory usage, and `GET /api/v1/notifications` p50/p95/p99 latency.

**Kill switch:** `NOTIFICATIONS_CACHE_ENABLED=false` bypasses Redis entirely for both reads and invalidation — no cache flush needed, and it takes effect on the next request with no restart-time side effects beyond normal config reload.

**Infrastructure requirements:** production Redis must be network-restricted and reachable only via the private network, authenticated through `REDIS_URL`, and use TLS (`rediss://`) where the deployment supports it. Never log `REDIS_URL` or notification payloads. Because notification content can be personal or business-sensitive, Redis persistence, backups, and access controls must meet the same data-handling standard as MongoDB.

**Memory/eviction:** cache payload size is bounded (at most 100 notifications per entry). An eviction or full flush is simply an ordinary cache miss — no prewarming, no migration, and no synchronous cleanup is required; entries repopulate through normal request traffic. Old generation/schema-version entries become unreachable immediately and expire naturally via TTL, so no key scan is ever needed on the request path.

## Architecture

### Entry Point
`main.go` initializes Echo, connects to MongoDB via `configs.ConnectDB()`, and registers route groups.

### Route Organization
Routes follow a two-tier pattern:
- **Public routes** (`RegisterPublicRoutes`) - No authentication required
- **Authenticated routes** (`RegisterAuthRoutes`) - Protected by `middleware.JWTAuth`

Each domain package exports `RegisterPublicRoutes` and/or `RegisterAuthRoutes` functions.

### Package Structure
Domain packages are organized by business capability:
- `auth` - Registration, login, social auth, JWT token generation
- `users` - User model, CRUD operations, user management
- `profiles` - User profiles, educational history, working hours
- `jobrequests` - Job requests, vacancies, applications
- `quotations` - Quotations, external quotations, invoices, payment bills
- `payments` - Flutterwave, Cellulant, KopoKopo payment integrations
- `transactions` - Transaction records, wallet transactions
- `subscriptions` - Subscription plans, user subscriptions
- `reviews` - User reviews
- `notifications` - SMS (Advanta, AfricasTalking, Twilio), email, push notifications
- `banks` - Bank listings, bank accounts
- `institutions` - Training institutions
- `locations` - Geographic locations
- `skills` - Skill categories
- `chat` - Chat functionality
- `documenttypes` - Document type management

### Supporting Packages
- `configs` - Database connection (`configs.MI.DB`), environment variables
- `middleware` - `JWTAuth` middleware, role guards (`IsAdmin`, `IsArtisan`, `IsCustomer`, `IsAssessor`)
- `responses` - Standardized JSON responses (`SendOK`, `SendCreated`, `SendError`)
- `contextutil` - Extract/store user ID from Echo context
- `jwtutil` - JWT token generation and validation
- `utils` - File utilities, email validation
- `jobs` - Scheduled jobs (subscription expiry, vacancy closing)

### User Types
Four user types with role-based access:
- `admin` - Administrative access
- `artisan` - Skilled workers (service providers)
- `customer` - Service seekers
- `assessor` - Assessment evaluators

### MongoDB Pattern
Models use UUID strings as `_id`. Collections are accessed via `configs.MI.DB.Collection("collection_name")`.

### Response Format
All endpoints use standardized response envelopes from `responses` package:
```go
responses.SendOK(c, data, "message")      // 200 OK
responses.SendCreated(c, data, "message") // 201 Created
responses.SendError(c, 400, "message")    // Error with status
```

### Scheduler
Background jobs run hourly via `jobs/scheduler.go`:
- Close expired job vacancies
- Expire subscriptions

## Testing

Tests use standard Go testing. Each package has `*_test.go` files. MongoDB operations are tested against a test database determined by `APP_ENV`.

## Migration Status

See `INVENTORY.md` for the complete PHP→Go migration tracking. Most core features are migrated (✅), some are partial (🟡), and a few remain (🔴).
