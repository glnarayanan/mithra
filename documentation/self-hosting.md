# Self-hosting Mithra

This guide installs Mithra on a server you operate. It does not prepare a VPS:
you own Linux updates, systemd, SQLite support, DNS, TLS, firewall policy,
reverse-proxy selection, monitoring, backup storage, and access to the server.
Mithra never installs packages, changes global firewall policy, manages DNS, or
claims unrelated proxy configuration.

Read [operations](operations.md) for ownership and release mechanics,
[backup and restore](backup-restore.md) for recovery semantics, and
[security](security.md) for the security contract.

## Prerequisites and choices

Use Linux amd64 or arm64 with systemd, SQLite support, and these commands:
`curl`, `openssl`, `awk`, `base64`, `sha256sum`, and `mktemp`. The release
bootstrap checks them and stops if one is missing.

Before installing, choose one listener mode:

- **app-only** keeps Mithra on `127.0.0.1:PORT` (default port `8090`) with a
  loopback HTTP canonical origin. It is for local or server-local access; it
  does not expose Mithra or configure a reverse proxy.
- **caddy**, **nginx**, or **apache** connects the selected existing proxy to
  Mithra's Unix socket. Supply a DNS hostname whose TLS and routing you operate.
  Mithra creates only its own proxy fragment. Caddy additionally requires the
  pre-existing global Caddyfile to import `/etc/caddy/conf.d/*`; the installer
  will not edit it. Nginx and Apache also require their existing TLS-capable
  configuration and command to be present.

Run a read-only plan first. Bare `plan` means an install plan; it never stops
the service or writes Mithra paths.

```bash
sudo mithra-installer plan \
  --domain mithra.example.com \
  --proxy caddy \
  --allowed-emails owner@example.com,partner@example.com \
  --plunk-from 'Mithra <hello@mithra.example.com>'
```

For app-only use `--proxy app-only --port 8090` and omit `--domain`. The plan
will refuse an occupied loopback port, a domain already owned by another vhost,
or missing prerequisites. Fix those conditions yourself; the installer will
not take over another service.

## Credentials

Mithra needs a verified Plunk sender identity and its Plunk API-key file for
password and invitation email. Create the key file outside the repository with
permissions that keep it private; never put its value in a command, `.env`,
shell history, Git, or a support ticket. Pass its path only to installation or
reconfiguration. The installer delivers it to the service as a systemd
credential.

The installer creates a 32-byte master-key credential on first install. Keep a
separate protected copy of it and at least one verified `.mbackup` archive.
The master key is required to authenticate backups and read encrypted sources;
losing it makes that material unrecoverable.

## Download, verify, then install

Do not use `curl | sudo sh`. Download release assets as an unprivileged user,
check the public-key fingerprint, verify the signed manifest, verify the
bootstrap-script digest from that manifest, and only then execute the script.
Replace the example release tag, hostname, email addresses, sender, and key
path before running this:

```bash
release=vX.Y.Z
stage=$(mktemp -d)
base="https://github.com/glnarayanan/mithra/releases/download/$release"
for name in RELEASE-MANIFEST RELEASE-MANIFEST.sig release-public-key.pem install.sh; do
  curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
    --output "$stage/$name" "$base/$name"
done

fingerprint=$(openssl pkey -pubin -in "$stage/release-public-key.pem" -pubout -outform DER | sha256sum | awk '{print $1}')
test "$fingerprint" = 044843e7944e940c1dfe513f61425902fde751348bd4d7dedb4f10d8a0f720c9
openssl pkeyutl -verify -pubin -inkey "$stage/release-public-key.pem" -rawin \
  -in "$stage/RELEASE-MANIFEST" -sigfile "$stage/RELEASE-MANIFEST.sig"
expected=$(awk '$1=="artifact" && $2=="install.sh" {print $4}' "$stage/RELEASE-MANIFEST")
test "$(sha256sum "$stage/install.sh" | awk '{print $1}')" = "$expected"

sudo env MITHRA_VERSION="$release" sh "$stage/install.sh" \
  --domain mithra.example.com \
  --proxy caddy \
  --allowed-emails owner@example.com,partner@example.com \
  --plunk-from 'Mithra <hello@mithra.example.com>' \
  --plunk-key-file /secure/path/plunk.key
```

The verified script downloads the matching architecture binaries, verifies
their manifest digests (and its own digest) before activation, stages only
Mithra-owned paths, verifies health, and creates the first encrypted backup.
For app-only, replace the domain/proxy flags with `--proxy app-only --port
8090`. Installation failure leaves the previous state untouched; do not delete
the staging directory or credentials until you have read the safe error output.

## First login and household setup

Open the configured HTTPS hostname in proxied mode or the loopback address in
app-only mode. Use **Set or reset your password** for the first allowlisted
adult. That adult becomes the owner and sends the partner invitation in
**Settings**. The invitee must already be in `--allowed-emails` and completes
the normal password flow.

Only the owner can connect OpenAI in **Settings**. It is optional: connect an
OpenAI API key to enable Capture, Import mapping, and explicit coaching
refreshes; deterministic records remain usable without it. See
[the user guide](user-guide.md#openai-and-privacy).

## Normal checks and backups

`status` prints JSON with installation, database, listener, version, backup,
credential-presence, and service facts. `ServiceActive` and
`ServiceHealthy` are separate: a running systemd unit is not proof that the
application is healthy.

```bash
sudo mithra-installer status
curl --fail --silent --show-error https://mithra.example.com/healthz
```

In app-only mode, use `http://127.0.0.1:8090/healthz` from the server. A daily
timer creates encrypted backups; copy successful archives and the master key to
separate protected storage. Verify an archive without stopping the service or
changing data:

```bash
sudo mithra-installer verify-backup \
  --archive /secure/mithra-YYYYMMDDTHHMMSS.mbackup
```

The command emits a small JSON receipt with the archive basename and
`"verified": true`; it never exposes keys. Create an additional backup before
risky operator work with `sudo mithra-installer backup`; this briefly quiesces
writes to capture one consistent generation.

## Upgrade and reconfigure

The installed CLI securely downloads and verifies the latest signed release,
creates a backup, and activates it atomically:

```bash
sudo mithra-installer upgrade
```

Installations older than `v1.2.1` need one final verified-script upgrade to
`v1.2.1`; the installed CLI handles later upgrades with the command above.

Plan or pin a release manually only when required:

```bash
sudo mithra-installer plan upgrade
sudo mithra-installer plan reconfigure \
  --allowed-emails owner@example.com,partner@example.com \
  --plunk-from 'Mithra <hello@mithra.example.com>'
```

For a manually pinned upgrade, repeat the complete verified-download sequence
above with the new exact tag, then replace the final command with:

```bash
sudo env MITHRA_VERSION="$release" sh "$stage/install.sh" upgrade
```

Upgrade verifies the signed release, requires a recognized installation, clean
SQLite and migrations, the retained key, and a verified backup. It creates a
pre-mutation backup, stages and rehearses the candidate, then activates it
atomically. Failed health checks restore the prior complete generation; Mithra
does not attempt a schema down-migration.

Use the installed installer to change the allowlist or Plunk sender/key without
replacing application binaries:

```bash
sudo mithra-installer reconfigure \
  --allowed-emails owner@example.com,partner@example.com \
  --plunk-from 'Mithra <hello@mithra.example.com>' \
  --plunk-key-file /secure/path/new-plunk.key
```

Reconfigure also creates a pre-mutation backup and rolls back on activation
failure. It preserves household data and the retained master key. When you
change `--proxy`, Mithra removes only its exact previous proxy fragment inside
the same rollback boundary and validates both affected proxy services.

## Restore

Plan a restore, then provide the archive and the current allowlist:

```bash
sudo mithra-installer plan restore \
  --archive /secure/mithra-YYYYMMDDTHHMMSS.mbackup \
  --allowed-emails owner@example.com,partner@example.com

sudo mithra-installer restore \
  --archive /secure/mithra-YYYYMMDDTHHMMSS.mbackup \
  --allowed-emails owner@example.com,partner@example.com
```

Restore authenticates and stages the archive before stopping Mithra, reconciles
the deletion journal, applies the current allowlist, and swaps the generation
only after health checks pass. It clears restored password hashes, sessions,
reset and invitation tokens, OpenAI credentials, pending work, and cached
coaching. Each eligible adult must use **Set or reset your password** again;
the owner reconnects OpenAI. On failure, the previous generation is restored.

## Shell completions and command reference

Public help is authoritative:

```bash
mithra help
mithra-installer help
mithra-installer help restore
```

Generate completion without writing files, requiring privilege, inspecting the
host, or running an installer operation. Redirect it only if you choose to
install it in your shell configuration:

```bash
mithra-installer completion bash
mithra-installer completion zsh
mithra-installer completion fish
```

`mithra version` and `mithra-installer version` print their build version.
`mithra recover-owner` is an offline recovery command. It requires
`--household` and `--email`, plus the current allowlist through
`--allowed-emails` or `ALLOWED_EMAILS`; select the database with `--db` or
`MITHRA_DB`. Stop Mithra before using it.

## Troubleshooting and removal

Start with the read-only facts:

```bash
sudo mithra-installer status
sudo journalctl -u mithra.service
```

Startup logs intentionally show a stable stage code instead of credentials,
content, addresses, or provider responses. For a failed proxied install, check
your DNS, certificate/TLS, proxy ownership, and proxy-native validation; for
app-only, check the chosen loopback port and server-local access. Do not edit
generated Mithra files or another application's proxy files to work around a
failed plan.

To stop using Mithra while retaining recovery data:

```bash
sudo mithra-installer uninstall
```

Uninstall stops and removes Mithra binaries, service/timer units, non-secret
configuration, the Plunk credential, and Mithra's proxy fragment. It preserves
the database, encrypted sources, deletion journal, backups, and master key, so
restore remains possible.

To permanently remove that retained recovery state, first uninstall, ensure
your independent backup decision is final, then explicitly confirm purge:

```bash
sudo mithra-installer purge --confirm-purge
```

Purge removes only Mithra's exact data, backups, and master-key targets. It is
irreversible: encrypted sources and backups cannot be recovered afterward. It
does not use wildcards and cannot target Arivu paths or unrelated services.
