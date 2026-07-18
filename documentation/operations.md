# Operations

Mithra ships two static Go binaries: the application and `mithra-installer`.
The installer is intentionally not a server bootstrapper. Linux, systemd,
SQLite support, the selected reverse proxy, TLS, DNS, and required commands
must already exist. It never installs packages or changes firewall policy.

## Trust and preflight

Release builds contain an Ed25519 publisher public key. `install.sh` verifies
the detached signature over the canonical release manifest, then checks the
application and installer byte counts and SHA-256 digests before executing the
installer. The installer performs the same verification before any owned path
is staged. Replacing an artifact and its checksum is therefore insufficient.

Configure these repository release settings before the first tag:

- variable `MITHRA_RELEASE_PUBLIC_KEY_RAW_B64`: raw 32-byte Ed25519 public key,
  standard unpadded base64, compiled into `mithra-installer`;
- variable `MITHRA_RELEASE_PUBLIC_KEY_PEM_B64`: the public PEM used by the
  bootstrap shell;
- secret `MITHRA_RELEASE_PRIVATE_KEY_PEM_B64`: the matching private PEM.

Keep the private key offline except for the GitHub secret. A tag build produces
amd64 and arm64 binaries, a canonical manifest, its detached signature, and a
release-specific `install.sh` with the public key pinned.

The read-only plan detects architecture, systemd, commands, occupied ports,
Caddy/Nginx/Apache vhosts, domain collisions, existing Mithra recovery
evidence, storage, and any Arivu installation. App-only mode proves the
loopback port can actually be bound. Proxy modes use `/run/mithra/mithra.sock`.
Every mutation must be one of the exact Mithra-owned paths in the plan; an
Arivu path is never eligible.

Caddy mode requires the existing global Caddyfile to import
`/etc/caddy/conf.d/*` (or `conf.d/*`). Mithra owns only its file inside that
directory and never edits the global Caddyfile.

```bash
mithra-installer plan \
  --domain family.example.com \
  --proxy caddy \
  --allowed-emails owner@example.com,partner@example.com
```

## Install and reconfigure

Download the release `install.sh`, inspect it, set an exact tag, and provide the
Resend credential through an existing `0600` file:

```bash
MITHRA_VERSION=v1.0.0 sh install.sh \
  --domain family.example.com \
  --proxy caddy \
  --allowed-emails owner@example.com,partner@example.com \
  --resend-from 'Mithra <mithra@example.com>' \
  --resend-key-file /root/mithra-resend.key
```

The service receives the master and Resend credentials through systemd
`LoadCredential`; neither is placed in ordinary environment values, command
arguments, logs, backups, or the ownership manifest. Reconfigure replaces the
Resend credential atomically and preserves the independently retained master
key and all household data. Upgrade and reconfigure require a recognized
migration history, clean SQLite, matching key evidence, and a verified
pre-mutation backup. Files are staged before activation and restored if service
or health validation fails; schema down-migrations are never attempted.

On a shared VPS, Mithra owns only its user, binaries, `/etc/mithra`,
`/var/lib/mithra`, `/var/backups/mithra`, `/run/mithra`, its systemd units, and
the single selected proxy fragment. It does not rewrite a global proxy file.

## Status and removal

`mithra-installer status` reports application/database presence, version,
listener or socket, latest backup, timer, credential presence without values,
and whether data remains preserved.

`mithra-installer uninstall` removes only runtime binaries, service/timer,
non-secret configuration, the Resend credential, and Mithra's proxy fragment.
It preserves the database, encrypted sources, deletion journal, backups, and
master key. `purge` prints and requires confirmation of the exact Mithra data,
backup, and master-key targets. It does not use a wildcard and cannot target an
Arivu path.

## Recovery ownership

Keep three things independently: the master-key credential, at least one
verified backup, and the current allowlist. Losing the key makes encrypted
sources and authenticated backups unrecoverable. See
[backup and restore](backup-restore.md) for the recovery drill.
