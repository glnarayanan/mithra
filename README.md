# Mithra

Mithra is a low-dependency household application for factual finance, health,
and planning records. It is an invitation-only, two-adult Go/SQLite application
with encrypted sources, evidence links, a shared Family Brief, and a separate
Only you view for each adult.

Mithra can help structure an update, but it never changes a record without
confirmation, gives medical or financial advice, judges either partner, spends
money, books anything, or synchronizes another calendar.

## Start here

- [User guide](documentation/user-guide.md) — Capture, Import, Shared and Only
  you, Family Brief, Week in Review, and model-provider boundaries.
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

## Verify

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go test -count=1 ./...

node --check web/static/app.js
node --test web/static/*.test.mjs
```

The repeatable judge path is in [the demo script](documentation/demo-script.md).
Build Week evidence and submission checks are in
[the submission guide](documentation/build-week-submission.md).
