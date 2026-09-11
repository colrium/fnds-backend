# Repository Guidelines

## Project Structure & Module Organization

This repository is a Go 1.26 API built with Echo and MongoDB. `main.go` wires configuration, services, middleware, and routes explicitly. Business capabilities live in top-level packages such as `auth/`, `users/`, `jobrequests/`, `payments/`, and `workspaces/`; keep handlers, models, services, routes, and tests together within the relevant package. Shared infrastructure belongs in packages such as `configs/`, `middleware/`, `responses/`, `contextutil/`, and `logging/`. Seed data is under `seeders/data/`, deployment infrastructure under `infra/`, and operational scripts under `scripts/`. Consult `INVENTORY.md` for migration status and `CLAUDE.md` for detailed architectural rules.

## Build, Test, and Development Commands

- `make run` — run the API locally with `go run .`.
- `make dev` — run with Air live reload.
- `make build` — compile the server to `./main`.
- `make test` — run all package tests verbosely.
- `go test ./auth -run TestHandleUserLogin -v` — run one focused test.
- `make lint` — run `go vet ./...`.
- `make format-check` — verify `gofmt` formatting.
- `make docker-run` / `make docker-down` — start or stop local supporting services.

Before opening a PR, run `make format-check`, `make lint`, and `go test ./... -race`.

## Coding Style & Naming Conventions

Use `gofmt`; Go files use tabs as emitted by the formatter. Package names are short, lowercase, and capability-oriented. Exported identifiers use `PascalCase`; internal identifiers use `camelCase`. Keep route registration consistent with `RegisterPublicRoutes` and `RegisterAuthRoutes`. Use UUID strings for MongoDB `_id` fields and the helpers in `responses/` for API envelopes. Prefer explicit dependency wiring and injected clocks or clients over hidden globals.

## Testing Guidelines

Tests use Go’s standard `testing` package and live beside production code as `*_test.go`. Name tests `TestBehavior` and use table-driven subtests for variants. Mock MongoDB with `mtest`, use repository fakes such as `stytch.Fake`, and restore shared state with `t.Cleanup`. CI runs formatting, golangci-lint, vet, build, and `go test ./... -v -race`; reproduce ordering-sensitive failures with `go test -p 1 ./...`.

## Commit & Pull Request Guidelines

Write concise, imperative commit subjects, optionally using a prefix such as `feat:`. Keep each commit focused. PRs should explain the behavior change, link the issue or requirement, list validation performed, and include request/response examples for API changes. Call out configuration, migration, or Terraform effects explicitly; never commit `.env` files or secrets.
