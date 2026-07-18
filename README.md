# Mithra

Mithra is a low-dependency household application for factual finance, health,
and planning records. It ships as one Go binary with embedded web assets,
SQLite, invitation-only two-adult households, and cookie-only browser sessions.

## Run locally

Copy `.env.example` to `.env`, replace its placeholders, and create the Resend
credential file as an absolute, service-user-owned `0600` file. Mithra does not
parse `.env`; load it through your shell or a systemd `EnvironmentFile`:

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
[security guide](documentation/security.md). The implementation plan lives
under `documentation/plans/`.
