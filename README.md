# Mithra

Mithra is a family operating system for busy couples. It brings finance, health
records, household plans, and upcoming obligations into one calm household
view, while each adult can keep selected records **Only you**.

The app turns text, voice, CSV, XLSX, and PDF inputs into reviewable,
evidence-linked records. It never changes a record without confirmation,
spends money, makes bookings, gives medical or financial advice, judges either
partner, or mediates a relationship.

## Week in Review

Week in Review turns valid shared records into a short weekly plan:

- A deterministic status: **On track**, **Mostly on track**, or **Needs
  attention**.
- No more than three ranked priorities, with the reason, date, status, and a
  grounded next step.
- A factual observation from recorded totals, budgets, and trends.
- Recent progress and a compact upcoming timeline.
- A separate **Only you** area for each adult's private records and
  data-quality notices.

Mithra groups a planning item and a finance obligation only when their titles,
dates, and privacy scope strongly match. It does not expose a private record,
or even the existence of a private issue, in shared status or copy. Invalid or
incompatible health readings never support a comparison or coaching claim.

Try the hosted app at [mithrahq.com](https://mithrahq.com). It is
invitation-only; there is no public signup.

## Start here

- [User guide](documentation/user-guide.md) — capture, imports, Shared,
  Only you, Family Brief, Week in Review, and model-provider boundaries.
- [Product tour](documentation/product-tour.md) — the main household views.
- [Self-hosting](documentation/self-hosting.md) — verified install and the
  operator lifecycle.
- [Architecture](documentation/architecture.md),
  [security](documentation/security.md),
  [operations](documentation/operations.md), and
  [backup and restore](documentation/backup-restore.md) — implementation and
  recovery contracts.
- [Build Week submission guide](documentation/build-week-submission.md) —
  judge setup, public assets, and final checks.

## Run locally

Copy `.env.example` to `.env`, replace its placeholders with absolute paths to
service-user-owned `0600` credential files, and keep the base64url-encoded
32-byte master key apart. Mithra does not read `.env` itself:

```bash
set -a
source .env
set +a
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go run ./cmd/mithra
```

Open `http://127.0.0.1:8090`; readiness is `/healthz`. An allowlisted person
uses **Set or reset your password** on first visit.

## Synthetic judge demo

An operator can reset the marked synthetic household and set two judge
passwords without publishing them. Both email addresses must already be in the
installed allowlist. Each password file must be a private regular file owned by
root, and contain a 12–128 byte password.

```bash
sudo mithra-installer reset-demo \
  --owner-email judge-owner@example.com \
  --partner-email judge-partner@example.com \
  --owner-password-file /root/mithra-demo-owner.password \
  --partner-password-file /root/mithra-demo-partner.password
```

The reset command works only on the marked demo household. It takes a verified
encrypted backup, restores the fixture through normal product services, revokes
old demo sessions and reset links, then starts the service again. It cannot
change an ordinary household. Send the chosen credentials only in Devpost's
private testing instructions; do not commit them.

## Submission assets

- [Devpost copy](submission/copy/devpost.md)
- [Private judge-instructions template](submission/copy/judge-instructions.template.md)
- [Screenshot manifest](submission/copy/screenshots.md)
- [Video render](submission/video/mithra-build-week-demo.mp4) and
  [render notes](submission/copy/video.md)

## Verify

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go test -count=1 ./...

node --check web/static/app.js
node --test web/static/*.test.mjs
```

## Built with Codex and GPT-5.6

Codex inspected and changed the Go and SQLite application, traced the Family
Brief and Week in Review paths, added typed weekly handling, tests, browser
checks, and the Remotion demo. GPT-5.6 supported product critique, UX choices,
prioritisation, prompt work, implementation, and review. The builder made the
scope, privacy, safety, and product decisions.

GPT-5.6 is build evidence, not a Mithra runtime requirement. Mithra uses an
optional configured model provider for grounded wording; deterministic facts
remain available when no provider is configured or a request fails.
