# Mithra architecture

## U1 runtime boundary

Mithra is a Go single-binary monolith. `cmd/mithra` composes the application,
opens SQLite, and serves an embedded, server-rendered browser shell. The
runtime defaults to an explicit literal TCP loopback address (`127.0.0.1` or
`::1`). A proxied deployment instead selects an absolute Unix-domain socket
with `--socket` or `MITHRA_SOCKET`; it is mutually exclusive with
`--addr`/`MITHRA_ADDR`. The socket parent must be an existing real directory
owned by the service user, with no permissions for other users and no group
write permission. It may grant read/execute to the intended service/proxy group
(for example, `0750`). Mithra creates its socket with `0660` permissions,
refuses every pre-existing path (including stale sockets), and unlinks its
socket during HTTP shutdown. The installer owns the service/proxy group and
socket-directory provisioning. Unix-socket mode accepts one validated
`X-Forwarded-For` address only as an opaque throttle identity; TCP mode ignores
it, and neither mode derives links, origins, actors, or authorization from
forwarded headers.

The server has bounded header, read, write, and idle timeouts, graceful
`SIGINT`/`SIGTERM` shutdown, a one-megabyte default request-body ceiling, panic
recovery, and a response request ID. Every route receives restrictive cache and
browser security headers, including a self-only CSP and frame denial. Health is
reported at `/healthz` (and `/api/health`) only after database initialization
has succeeded. Runtime failure logs contain only an applicable request ID and a
stable safe error code; startup emits only its safe error code. The server does
not log listener addresses, and its standard-library HTTP error log is discarded
because it can include untrusted request details.

The voice-capture route alone permits a nine-megabyte multipart envelope so it
can enforce the product's eight-megabyte, 90-second audio limit. It keeps the
uploaded part in bounded memory, accepts only supported audio media types, and
rejects invalid, busy, or unauthenticated requests before any provider call.

## Database lifecycle

`internal/database` opens one SQLite connection with WAL mode, foreign-key
enforcement, a five-second busy timeout, normal synchronous mode, and a WAL
checkpoint target. Startup verifies the effective journal mode and restricts
the database, WAL, and shared-memory files to the service owner. Database
configuration accepts a plain filesystem path only, rejects symbolic or
non-regular leaves, and requires an owner-controlled parent directory that is
not writable by its group or other users. It proves FTS5 is available and fails
startup unless the binary was built with SQLite extension loading omitted. The
required build flags are:

```text
-tags=sqlite_fts5,sqlite_omit_load_extension
```

Numbered SQL files in `migrations/` are compiled into the binary. The migration
ledger stores each version, filename, SHA-256 checksum, and application time.
Startup rejects a changed historical checksum and a database created by a newer
binary. Migration SQL cannot include transaction-control statements, so every
accepted migration remains inside the runtime's outer atomic transaction. After
migrations, readiness also runs SQLite foreign-key, FTS external-content, and
integrity checks. Mithra deliberately creates no seeded household or synthetic
insight data: later import and household paths use the same runtime path for
every valid household.

## Secure processing spine

`internal/secrets` derives separate settings, source, and backup AES-256-GCM
keys from one 32-byte master key. `internal/storage` writes authenticated source
ciphertext to a same-directory staging file, syncs and atomically renames it,
then commits immutable source metadata. Startup reconciliation removes only
recognized staging and unreferenced ciphertext and refuses a live row with a
missing file. SQLite scope triggers bind source, provenance, and search rows to
an active household member; external-content FTS triggers and readiness checks
reject index orphans.

`internal/jobs` stores only identifiers and revision snapshots. Lease tokens
are hashed, lease generations fence stale workers, and publication happens in
the same transaction as active membership, live source, and shared/personal
revision checks. `internal/providers` has one fixed provider registry and
validates provider addresses, request size, redirects, responses, and JSON
objects. It supports OpenAI Responses, OpenAI-compatible chat, Gemini, and
Anthropic text requests. Only OpenAI receives audio or an explicitly confirmed
visual PDF. The browser never receives a saved key.

## Finance domain

`internal/finance` stores income, spending, assets, liabilities, budgets, and
obligations in six typed tables over the shared source/evidence spine. Amounts
use bounded integer coefficient-and-scale values; Go owns exact totals,
month-to-month category changes, and upcoming-date queries. Invalid or missing
amounts and dates remain source-linked incomplete records and are excluded from
affected calculations. A source may declare at most one validation-only
currency context; no currency field, selector, conversion, or symbol is stored
as finance meaning or rendered by the lens.

Finance reads recheck active membership and apply the same shared/personal
scope used by encrypted source downloads. Corrections use optimistic versions,
create a user-owned superseding record, remove the old search entry, and bump
only the applicable shared or personal revision. The server-rendered finance
lens remains useful without a model provider and exposes exact totals, factual changes,
upcoming obligations, incomplete explanations, and authorized provenance.

## Health domain

`internal/health` keeps observations, appointments, and care routines in three
typed tables over the same source, evidence, visibility, and revision spine.
Observed values and reported ranges use bounded coefficient-and-scale numbers.
A small explicit registry handles only identity and simple dimension-preserving
unit conversions; analyte-specific and unknown mismatches remain separate until
the user supplies a corrected value and unit.

Longitudinal series require the same analyte, subject, specimen, available
method, reference context, and compatible-unit family. Corrections create an
active user-owned superseding revision without changing the retained source.
The health lens reports only stored observations and dates, links every item to
an authorized source, and maintains a visible boundary against diagnosis,
clinical interpretation, or treatment recommendations.

## Planning domain

`internal/planning` stores goals, plans, milestones, calendar events, owners,
dependencies, constraints, and completion state in separate typed tables. One
authorized eligible-event query feeds month, week, and agenda presentations;
personal records remain owner-only, while shared records remain visible to both
active adults. Deterministic overlap checks flag only events assigned to a
common owner and never turn the calendar into a generic reminder queue.

The household owner confirms one IANA timezone in Settings; browser detection
is only a prefilled suggestion and is never saved without that form submission.
Calendar exports implement the required RFC 5545 event subset with CRLF line
endings, escaping, UTF-8-safe folding, and correct exclusive all-day end dates.
Google Calendar links open prefilled drafts for review. Mithra stores no OAuth
token, calendar credential, subscription, or background synchronization state.

## Conversational capture

`internal/capture` accepts one strict finance, health, or planning proposal and
commits it only through the corresponding typed domain service. A model request
receives quoted user text, no household identifiers, and a closed schema.
OpenAI Responses also set `store: false`. A clear text update is confirmed immediately
with a ten-minute revision-fenced Undo; a missing material owner, date, unit, or
status creates one focused clarification and no derived record. User answers
can fill only the field Mithra requested.

Browser audio uses `MediaRecorder` as progressive enhancement. The server
encrypts raw bytes in the source store before transcription, exposes no raw
audio download route, and retains ciphertext only for a bounded retry or review
window. The transcript and proposed typed record require confirmation; confirm
deletes raw audio while retaining transcript provenance. Cancellation, terminal
failure, expiry, and startup reconciliation clean abandoned ciphertext. Two
in-process voice slots bound provider work without adding a queue service.

## Document imports

CSV and XLSX extraction runs locally with bounded structure and output. PDF
bytes cross bounded Unix-socket IPC to a separate, no-network parser identity;
the parser receives no plaintext path, database, source store, or credentials.
Ordinary provider requests contain only minimized locator-bearing text. A
scanned PDF requires one actor-, source-, digest-, visibility-, membership-,
version-, and expiry-bound confirmation before a single inline transfer.

AI proposals remain in a revision-fenced review until one cross-domain SQLite
transaction publishes every valid record. Source deletion first appends and
fsyncs an authenticated encrypted intent outside SQLite, then atomically
tombstones the source and dependent records, evidence, search entries, and jobs.
Startup replays the journal before serving; ciphertext cleanup is idempotent.

## Browser shell

`web/templates/shared/shell.html`, `web/static/styles.css`, and `web/static/app.js`
are embedded first-party assets. The mobile-first shell renders without
JavaScript, exposes Family Brief, Week in Review, Capture, Import, Finance,
Health, Planning, Settings, and Help navigation, has a
keyboard-visible skip link and focus treatment, and declares an accessible
status region plus an honest empty state. The tiny JavaScript enhancement writes
updates with `textContent`, never HTML, so untrusted future import/model text
remains text.

Authentication, encrypted source infrastructure, durable jobs, the model-provider
boundary, typed finance, typed health, typed planning, conversational capture,
and document imports now build on this runtime. Coaching remains in its
dedicated unit.

## Verification

CI uses Go 1.25.12, the required SQLite tags, `gofmt`, `go mod verify`,
`go vet`, tests, an application build, native Node syntax/test checks, and a
pinned `govulncheck`. It does not install a frontend package manager.

Release tags additionally build the application and installer for Linux amd64
and arm64. A canonical manifest is signed with Ed25519; both the bootstrap
shell and compiled installer pin and verify the publisher key before owned
paths are staged. The lifecycle package constrains every mutation to Mithra's
manifested paths and treats an Arivu installation as read-only host context.
Backups authenticate a complete SQLite, encrypted-source, and deletion-journal
generation; restore verifies and sanitizes that generation before atomic swap.

## Coaching boundary

`internal/coaching` owns actor-scoped evidence contexts, deterministic Family
Brief and Week in Review fallbacks, separately keyed shared/private caches,
generated-output validation, and the one-nudge lifecycle. Browser page loads
perform SQLite reads only. The selected provider is invoked only by an explicit refresh, with
shared and personal facts sent in separate calls and publication fenced by a
fresh membership, revision, source, and evidence check. See
[`coaching.md`](coaching.md) for the privacy and notification contract.
