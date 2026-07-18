# Mithra

Mithra is a low-dependency household application for factual finance, health,
and planning records. It ships as one Go binary with embedded web assets,
SQLite, invitation-only two-adult households, and cookie-only browser sessions.

Mithra is also a calm, composed, objective AI coach. It turns text, voice,
CSV, XLSX, and PDF updates into user-reviewed records; then combines the facts
into a shared Family Brief and each adult's private Week in Review overlay. It
never invents missing values, exposes one adult's personal records to the
other, gives medical or financial advice, judges relationships, spends money,
books anything, or changes a record without confirmation.

The deterministic finance, health, planning, calendar, and evidence views work
without OpenAI. When the household owner adds an API key, Mithra uses
`gpt-5.4-mini` only for structured extraction and evidence-linked coaching and
`gpt-4o-mini-transcribe` for voice; originals stay local unless an explicit
visual-PDF fallback is confirmed.

## Run locally

Copy `.env.example` to `.env`, replace its placeholders, and create the Resend
and master-key credential files as absolute, service-user-owned `0600` files.
The master-key file contains one base64url-encoded 32-byte random key and must be
retained independently for recovery. Mithra does not parse `.env`; load it
through your shell or a systemd `EnvironmentFile`:

```bash
set -a
source .env
set +a
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go run ./cmd/mithra
```

Open `http://127.0.0.1:8090`. The readiness endpoint is
`http://127.0.0.1:8090/healthz`. Use **Set or reset your password** for the
first allowlisted account; public signup does not exist.

If allowlist removal closes a household, an operator can reassign its owner
without starting the web listener. The candidate must be present in the current
allowlist and either belong to that closed household or be unassigned:

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
go run ./cmd/mithra recover-owner \
  --db /var/lib/mithra/mithra.sqlite3 \
  --allowed-emails owner@example.com,partner@example.com \
  --household HOUSEHOLD_ID \
  --email owner@example.com
```

A pending recovered owner remains unable to sign in until completing the normal
password-link flow.

## Seeded judge household

The production installer can reset only Mithra's cryptographically backed,
database-marked demo household. It stops application writes, creates and
verifies an encrypted backup, runs the same source/import/capture/domain paths
as ordinary data, verifies both private overlays, and restores the complete
prior generation on failure:

```bash
sudo mithra-installer reset-demo \
  --owner-email judge-owner@example.com \
  --partner-email judge-partner@example.com
```

The two emails must already be in `ALLOWED_EMAILS`. Use the normal **Set or
reset your password** flow for private judge access. The seed is not required:
the acceptance suite separately imports an unrelated household's finance CSV,
health PDF, and planning capture through the same services and verifies the
result after an application restart.

Reviewable sample inputs live in [`testdata/demo`](testdata/demo/): a finance
CSV, text-bearing health PDF, and planning transcript. They contain synthetic
data only and can also be uploaded manually through the ordinary UI.

## Verify

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go test -count=1 ./...

node --check web/static/app.js
node --test web/static/*.test.mjs
```

## Listener modes

Loopback TCP is the default. A reverse proxy can instead connect through an
explicit Unix-domain socket; set either `--socket`/`MITHRA_SOCKET` or
`--addr`/`MITHRA_ADDR`, never both:

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
go run ./cmd/mithra --socket /run/mithra/mithra.sock --db /var/lib/mithra/mithra.sqlite3
```

The socket directory must already be owned by the Mithra service user, deny all
permissions to other users, and not be writable by its group. It may grant
read/execute access to the intended service/proxy group (for example, `0750`).
Mithra creates the socket with `0660` permissions, rejects any pre-existing
entry (including a stale socket), and removes its own socket on shutdown. The
installer is responsible for provisioning the service/proxy group and directory;
only Unix-socket mode accepts one validated `X-Forwarded-For` IP, solely for
authentication throttling. Links and authorization always use configured or
server-derived values.

## Documentation

See the [architecture guide](documentation/architecture.md) and
[security guide](documentation/security.md). Deployment and recovery are in
[operations](documentation/operations.md) and
[backup and restore](documentation/backup-restore.md). The implementation plan
lives under `documentation/plans/`.

The repeatable judge path is in [the demo script](documentation/demo-script.md),
and the exact Build Week field/checklist draft is in
[the submission guide](documentation/build-week-submission.md).

## OpenAI Build Week build evidence

Mithra was created during the Build Week submission period. Codex was the
implementation environment from the first product interrogation through the
Go/SQLite architecture, eleven atomic feature units, tests, browser QA, and
installer hardening. GPT-5.6 Sol supplied consolidated high-rigor reviews and
root-cause security fixes; GPT-5.6 Luna assisted implementation and debugging.
The product owner kept authority over scope and explicitly chose the
low-dependency binary, privacy boundaries, no-execution coach, number-only V1,
and two-adult model.

GPT-5.6 is used meaningfully to build and review Mithra; it is not claimed as a
runtime dependency. The optional in-product OpenAI models are listed above.
The primary build task is `019f7561-247d-7d60-b17e-e046156f8fdf`; its dated
commit evidence and submission checklist are documented in the submission
guide. The required `/feedback` Session ID must be generated from that primary
Codex task and entered only in Devpost.
