# Mithra

Mithra is a low-dependency household application for factual finance, health,
and planning records. It is an invitation-only, two-adult Go/SQLite application
with encrypted sources, evidence links, a shared Family Brief, and a separate
Only you view for each adult.

Mithra can help structure an update, but it never changes a record without
confirmation, gives medical or financial advice, judges either partner, spends
money, books anything, or synchronizes another calendar.

See the [product tour](documentation/product-tour.md) for the Family Brief,
finance, health, planning, and Week in Review screens.

## Start here

- [User guide](documentation/user-guide.md) — Capture, Import, Shared and Only
  you, Family Brief, Week in Review, and model-provider boundaries.
- [Product tour](documentation/product-tour.md) — current views of the main
  household workflows.
- [Self-hosting](documentation/self-hosting.md) — verified release install and
  the complete operator lifecycle.
- [Architecture](documentation/architecture.md), [security](documentation/security.md),
  [operations](documentation/operations.md), and
  [backup and restore](documentation/backup-restore.md) — implementation and
  recovery contracts.
- [Security audit summary](documentation/security-audit.md) — reviewed surfaces,
  controls, residual risks, and recovery expectations.

## Run locally

Copy `.env.example` to `.env`, replace its placeholders with absolute paths to
service-user-owned `0600` credential files, and retain the base64url-encoded
32-byte master key independently. Mithra does not read `.env` itself:

```bash
set -a
source .env
set +a
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go run ./cmd/mithra
```

Open `http://127.0.0.1:8090`; readiness is `/healthz`. Use **Set or reset your
password** for the first allowlisted account. There is no public signup.

### Demo data

A clean local run starts empty. The checked-in acceptance path creates a safe
sample household through the same import, capture, evidence, and privacy code
used by ordinary households. See
[`internal/app/acceptance_test.go`](internal/app/acceptance_test.go) and the
[judge path](documentation/demo-script.md). An installed marked demo household
can be restored with:

```bash
sudo mithra-installer reset-demo \
  --owner-email judge-owner@example.com \
  --partner-email judge-partner@example.com
```

The command refuses ordinary households and takes a verified encrypted backup
before it changes the demo fixture.

## Verify

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go test -count=1 ./...

node --check web/static/app.js
node --test web/static/*.test.mjs
```

## Built with Codex and GPT-5.6

Codex was the main build environment from product discovery through the Go and
SQLite implementation, tests, browser checks, installer, and releases. GPT-5.6
Sol reviewed architecture, security, correctness, and release fixes. GPT-5.6
Terra and Luna implemented and debugged focused slices. The product owner made
the scope, privacy, safety, and user-experience decisions.

The commit history keeps each change reviewable. The
[Build Week submission guide](documentation/build-week-submission.md) maps the
main Codex work to the shipped commits and judge path.

The repeatable judge path is in [the demo script](documentation/demo-script.md).
Build Week evidence and submission checks are in
[the submission guide](documentation/build-week-submission.md).
