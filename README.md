# Mithra

Mithra is a low-dependency household application for factual finance, health,
and planning records. The U1 runtime is a single Go binary with embedded web
assets and SQLite; household authentication, capture, imports, and domain
behaviour arrive in later units.

## Run locally

Use the Go version declared in `go.mod` and the required SQLite feature tags:

```bash
GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension \
GOCACHE=/private/tmp/mithra-build-cache \
go run ./cmd/mithra --addr 127.0.0.1:8090 --db .local/mithra.sqlite3
```

Open `http://127.0.0.1:8090`. The readiness endpoint is
`http://127.0.0.1:8090/healthz`.

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
Mithra does not trust forwarded headers.

## Documentation

See [the architecture guide](documentation/architecture.md) for the runtime,
database, and browser-boundary design. The implementation plan lives under
`documentation/plans/`.
