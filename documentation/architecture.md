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
`SIGINT`/`SIGTERM` shutdown, a one-megabyte request-body ceiling, panic
recovery, and a response request ID. Every route receives restrictive cache and
browser security headers, including a self-only CSP and frame denial. Health is
reported at `/healthz` (and `/api/health`) only after database initialization
has succeeded. Runtime failure logs contain only an applicable request ID and a
stable safe error code; startup emits only its safe error code. The server does
not log listener addresses, and its standard-library HTTP error log is discarded
because it can include untrusted request details.

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
revision checks. `internal/providers` uses fixed OpenAI HTTPS endpoints, strict
Responses schemas with `store: false`, bounded responses, and the dedicated
audio transcription endpoint. The composition root owns these concrete
services; there is no provider abstraction or browser-visible credential.

## Browser shell

`web/templates/shell.html`, `web/static/styles.css`, and `web/static/app.js`
are embedded first-party assets. The mobile-first shell renders without
JavaScript, exposes Brief, Finance, Health, and Planning navigation, has a
keyboard-visible skip link and focus treatment, and declares an accessible
status region plus an honest empty state. The tiny JavaScript enhancement writes
updates with `textContent`, never HTML, so untrusted future import/model text
remains text.

Authentication, encrypted source infrastructure, durable jobs, and the OpenAI
boundary now build on this runtime. Typed finance, health, planning, capture,
import, and coaching services remain in their dedicated units.

## Verification

CI uses Go 1.25.12, the required SQLite tags, `gofmt`, `go mod verify`,
`go vet`, tests, an application build, native Node syntax/test checks, and a
pinned `govulncheck`. It does not install a frontend package manager.
