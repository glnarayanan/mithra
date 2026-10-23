# Deploy with containers / Dokploy

This guide runs Mithra from the repository's `Dockerfile` and
`docker-compose.yml`. It is written for [Dokploy](https://dokploy.com), which
builds the compose project on every push, routes a domain through its Traefik
proxy and backs up named volumes. The same files work with plain
`docker compose` behind any reverse proxy on the same Docker network.

The [systemd install](self-hosting.md) is unchanged and remains the supported
path for a server you manage by hand. The two paths do not share data.

## What runs

| Service | Image command | Network | Writes to |
| --- | --- | --- | --- |
| `app` | `mithra serve` as uid 10001 | compose network only; no published host port | `/data`, `/backups`, `/tmp` (tmpfs) |
| `pdf-parser` | `mithra pdf-parser --listen …` as uid 10002 | `network_mode: none` | its socket on the `pdf-socket` tmpfs only |

Both services use one image and a read-only root filesystem, drop every Linux
capability and set `no-new-privileges`. The parser has no data volume, no
secrets, a 192 MB memory cap with no swap, half a CPU, 32 processes and 64
open files. It reads PDFs that the app hands it over a Unix socket. It exits
every five minutes and Docker restarts it as a clean process. A parse that
lands during that restart fails with "Mithra's PDF reader is restarting", and
the user can try the file again.

The app listens on `0.0.0.0:8080` inside its container. Traefik terminates TLS
and forwards to it over the private Docker network.

## Encryption at rest

Everything in the data volume is ciphertext:

- **Database.** The container image links SQLCipher 4.19.0, built from source
  and pinned to its release commit. Every database page, WAL frame and backup
  snapshot is AES-256 encrypted with a key that HKDF derives from the master
  key, using its own label. Container mode refuses to start if the binary lacks
  SQLCipher or if it finds an unencrypted database.
- **Uploaded sources.** These were already sealed with AES-GCM.
- **Backups.** Archives are authenticated with a key derived from the master
  key. The database inside each archive stays SQLCipher ciphertext.

Release binaries and the systemd install still use plain SQLite. They are not
affected by this.

## Key custody

The master key protects the database, every uploaded source, provider
credentials and every backup. **Losing it makes all of them unrecoverable.
Changing it has the same effect.** There is no reset.

- Generate it once:
  `openssl rand 32 | base64 | tr '+/' '-_' | tr -d '=\n'`
  This prints 43 characters. If you have built the image, `mithra generate-master-key` prints the same format.
- Store a copy outside the server, for example in your password manager. Do
  not store it next to the volume backups. A backup and its key in the same
  place protect nothing.
- Deliver it to the container as a file mount, never as an environment
  variable. Compose mounts it read-only at `/run/secrets/master_key`. It is
  never written to a volume and never included in a backup.

The Plunk API key is delivered the same way at `/run/secrets/plunk_key`.

Mithra accepts a secret file only when root or uid 10001 owns it and no group
or other user can write to it. Dokploy's file mounts meet these conditions as
written.

## First run on Dokploy

1. **Create the service.** In Dokploy, create a **Compose** service from the
   GitHub repository `glnarayanan/mithra`, branch `main`, compose path
   `./docker-compose.yml`. Turn on auto-deploy so every push to `main`
   rebuilds the stack.
2. **Add the two secrets.** Under **Advanced → Mounts**, add two **File Mounts**:
   - `master.key`, whose content is the generated master key
   - `plunk.key`, whose content is the Plunk secret key (`sk_…`)

   Dokploy writes file mounts to `../files/` relative to the compose file.
   `docker-compose.yml` reads them from there. If your Dokploy version uses
   another location, set `MITHRA_MASTER_KEY_PATH` and `MITHRA_PLUNK_KEY_PATH`
   to the host paths.
3. **Set the environment.** In the **Environment** tab, set the required
   variables listed below. To find the CIDR for `MITHRA_TRUSTED_PROXIES`, run
   this on the server:

   ```bash
   docker network inspect dokploy-network --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}'
   ```

4. **Add the domain.** In **Domains**, add `app.mithrahq.com` with service `app`,
   container port `8080`, HTTPS on and a Let's Encrypt certificate.
5. **Deploy.** Wait until both services report healthy, then check the stack
   from the server:

   ```bash
   docker compose -p <app-name> exec app mithra status
   ```

   A healthy stack prints `database=encrypted`, `database_check=ok` and
   `pdf_parser=reachable`. Dokploy shows the compose project name, which is
   `<app-name>` here.
6. **Sign in.** Open `https://app.mithrahq.com`. The first allowlisted adult uses
   **Set or reset your password** and becomes the household owner. The owner
   invites the partner in **Settings**. See
   [self-hosting](self-hosting.md#first-login-and-household-setup) for the rest
   of the setup, which is the same for both install paths.
7. **Set up volume backups.** Under **Volume Backups**, schedule backups of
   both named volumes, the ones ending in `mithra-data` and `mithra-backups`,
   to your S3 destination. Then copy the master key to its separate location
   if you have not already.

## Environment variables

Set these in Dokploy. Compose refuses to start when any of them is missing.

| Variable | Example | Purpose |
| --- | --- | --- |
| `ALLOWED_EMAILS` | `owner@example.com,partner@example.com` | The only addresses that can sign in. There is no public signup. |
| `MITHRA_CANONICAL_ORIGIN` | `https://app.mithrahq.com` | The public origin. Must be HTTPS. Used for links, cookie scope and the CSRF origin check. Never taken from `Host` or forwarding headers. |
| `MITHRA_PLUNK_FROM` | `Mithra <hello@mithrahq.com>` | The verified Plunk sender. |
| `MITHRA_TRUSTED_PROXIES` | `10.0.1.0/24` | Comma-separated private CIDRs of the proxy network. Only peers in these networks may supply `X-Forwarded-For`. |

Optional settings:

| Variable | Default | Purpose |
| --- | --- | --- |
| `MITHRA_BACKUP_TIME` | `03:30` | Daily in-app backup time in UTC, `HH:MM`, or `off`. |
| `MITHRA_MASTER_KEY_PATH` | `../files/master.key` | Host path of the master-key file, read by compose. |
| `MITHRA_PLUNK_KEY_PATH` | `../files/plunk.key` | Host path of the Plunk key file, read by compose. |
| `MITHRA_VERSION` | `dev` | Image tag and the version that `mithra version` reports. |

The image sets the rest. Change them only if you change the volume layout.

| Variable | Image value |
| --- | --- |
| `MITHRA_PROXY_MODE` | `container` |
| `MITHRA_ADDR` | `0.0.0.0:8080` |
| `MITHRA_DB` | `/data/mithra/mithra.sqlite3` |
| `MITHRA_SOURCE_DIR` | `/data/mithra/sources` |
| `MITHRA_LOCK_FILE` | `/data/.mithra.lock` |
| `MITHRA_BACKUP_DIR` | `/backups` |
| `MITHRA_PDF_PARSER_SOCKET` | `/run/mithra-pdf/pdf-parser.sock` |
| `MITHRA_CREDENTIALS_DIRECTORY` | `/run/secrets` |
| `MITHRA_MASTER_KEY_FILE` | `/run/secrets/master_key` |
| `MITHRA_PLUNK_KEY_FILE` | `/run/secrets/plunk_key` |

Container mode refuses to start if any of these is true:

- the trusted proxy list is missing, or names a public or overly broad network
- the origin is not HTTPS
- the listen address is not a literal IP
- SQLCipher is unavailable
- the database is unencrypted

## Volumes

| Volume | Mounted at | Holds | Back up? |
| --- | --- | --- | --- |
| `mithra-data` | `/data` (app) | `mithra/mithra.sqlite3` and its WAL (SQLCipher), `mithra/sources/*.enc` (AES-GCM sources), `mithra/deletion.journal`, the data lock | Yes |
| `mithra-backups` | `/backups` (app) | The seven newest verified `.mbackup` archives | Yes |
| `pdf-socket` | `/run/mithra-pdf` (both) | Only the parser's Unix socket; a 1 MB tmpfs that starts empty on every start | No |

The image creates `/data` and `/backups` owned by `10001:10001` with mode
`0700`. A new named volume inherits that ownership, so no `chown` step is
needed. Mithra checks the data paths' ownership and permissions on every start.

Dokploy's volume backup stops the service, archives each volume and starts it
again. A copy of `mithra-data` is consistent because the app is stopped. The
`.mbackup` archives in `mithra-backups` are verified generations you can
restore without that copy. Neither one contains the master key.

## Health

- `GET /healthz` returns `{"status":"ready"}` after migrations and the SQLite
  integrity checks pass. Traefik and uptime monitors can use it.
- The image `HEALTHCHECK` runs `mithra healthcheck`. That command requests
  `/healthz` on the local port, so the slim image needs no `curl`.
- The parser's health check runs `mithra pdf-parser-probe` against its socket.
- To check a real parse end to end:

  ```bash
  docker compose -p <app-name> exec app mithra pdf-parser-probe --sample
  ```

  It prints `pdf_parser=ok pages=1`.

## Backups and restore

The app makes a verified `.mbackup` archive every day at `MITHRA_BACKUP_TIME`.
It also makes one about a minute after a start when the newest archive is
missing or more than 24 hours old. Live writes pause for the few seconds the
snapshot takes. Archives are pruned to the newest seven. To take a backup now,
for example before an upgrade:

```bash
docker compose -p <app-name> kill -s SIGUSR1 app
docker compose -p <app-name> logs app | grep backup_verified
```

To restore, run these commands on the server. Restore needs the same master
key and the current `ALLOWED_EMAILS`.

```bash
docker compose -p <app-name> exec app ls /backups
docker compose -p <app-name> exec app mithra verify-backup --archive /backups/<archive>
docker compose -p <app-name> stop app
docker compose -p <app-name> run --rm --no-deps app restore --archive /backups/<archive>
docker compose -p <app-name> start app
```

Restore authenticates and stages the archive before it changes live data. It
replays deletions, swaps `/data/mithra` atomically and opens the restored
database with the key. If that check fails, it puts the previous generation
back. As with the systemd path, restore clears passwords, sessions and provider
credentials. Each adult uses **Set or reset your password** again, and the
owner re-enters the model provider. See [backup and restore](backup-restore.md)
for the details.

To restore from a Dokploy volume backup instead, restore both volumes from the
same night while the service is stopped.

## Admin commands

Run read-only commands against the running app with `exec`. Commands that
change data take the data lock, so they refuse to run while the app is up.
Stop the app first and use `run`.

| Task | Command |
| --- | --- |
| Status | `docker compose -p <app-name> exec app mithra status` |
| Verify an archive | `docker compose -p <app-name> exec app mithra verify-backup --archive /backups/<archive>` |
| Backup now (running) | `docker compose -p <app-name> kill -s SIGUSR1 app` |
| Backup (stopped) | `docker compose -p <app-name> run --rm --no-deps app backup` |
| Restore | `docker compose -p <app-name> run --rm --no-deps app restore --archive /backups/<archive>` |
| Recover the owner | `docker compose -p <app-name> run --rm --no-deps app recover-owner --household <id> --email owner@example.com` |
| Reset the demo household | `docker compose -p <app-name> run --rm --no-deps app reset-demo --owner-email … --partner-email …` |

`reset-demo` only works on the marked demo household. It makes a verified
backup first, and both addresses must be in `ALLOWED_EMAILS`. It keeps
existing password hashes unless you also supply `--owner-password-file` and
`--partner-password-file`. Those files must be extra compose secrets under
`/run/secrets`.

To change the allowlist or the origin, edit the environment in Dokploy and
redeploy.

## Upgrades

Each push to `main` makes Dokploy rebuild the image and recreate both services.
Mithra applies migrations at start, and the migrations are checksum-locked and
forward-only. Before a release that changes the schema, take a manual backup
with `SIGUSR1`. If a new release does not become healthy, redeploy the previous
commit, or restore the archive.

## Proxy trust

Mithra uses `X-Forwarded-For` only to tell apart sign-in attempts from
different clients for rate limiting. It never uses the header for
authorization. The header is read only when the TCP peer is inside
`MITHRA_TRUSTED_PROXIES`, and only when it carries exactly one IP address.
Traefik replaces any client-supplied value by default, so each visitor gets
their own throttle.

If you put a CDN in front of Traefik, configure Traefik's
`forwardedHeaders.trustedIPs` for that CDN. Otherwise Mithra sees a
multi-hop header and applies one shared throttle to all visitors. That is
safe, but stricter.

Secure cookies use the `__Host-` prefix. `Origin` and `Referer` must match
`MITHRA_CANONICAL_ORIGIN` on every mutation.

## Running without Dokploy

```bash
openssl rand 32 | base64 | tr '+/' '-_' | tr -d '=\n' > ../files/master.key
printf '%s' 'sk_your_plunk_key' > ../files/plunk.key
chmod 0644 ../files/*.key   # root-owned and not writable by others is enough
cat > .env <<'EOF'
ALLOWED_EMAILS=owner@example.com,partner@example.com
MITHRA_CANONICAL_ORIGIN=https://mithra.example.com
MITHRA_PLUNK_FROM=Mithra <hello@mithra.example.com>
MITHRA_TRUSTED_PROXIES=172.16.0.0/12
EOF
docker compose up -d --build --wait
```

Then attach your reverse proxy to the compose network and route to `app:8080`.
Nothing is published on the host.

## Building and testing the image

```bash
docker build --target test .           # full test suite against SQLCipher
docker build --build-arg VERSION=v1.7.0 -t mithra:v1.7.0 .
```

CI runs both commands. It then brings up the stack with a throwaway key and
checks health, a sandboxed parse and a restart.
