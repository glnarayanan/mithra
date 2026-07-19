# Security audit summary

**Validation date:** 2026-07-19

This is a safe post-remediation summary. It records reviewed boundaries and
operational expectations without publishing exploit paths, payloads, secrets,
host details, or sensitive test evidence.

The consolidated final review found no unresolved P0-P2 security defects.

## Reviewed surfaces

- admission, password reset, invitations, sessions, CSRF, browser headers, and
  household authorization;
- source encryption, imports, document review, visual-PDF consent, deletion
  journal, backups, restore, and recovery ownership;
- OpenAI and Plunk request boundaries, credential delivery, logs, jobs, and
  generated coaching;
- runtime listeners, Unix-socket permissions, PDF-parser isolation, installer
  release verification, lifecycle operations, owned-path constraints, and
  reverse-proxy modes;
- first-party browser JavaScript, authenticated Help, quick navigation, and
  mobile record layouts.

## Remediated findings and verified controls

The review remediated confirmed P1/P2 concerns in import/deletion lifecycle
atomicity, provider redirect handling, restore ownership and preflight,
unfinished-import cleanup during restore, release-artifact authentication,
lifecycle CLI parsing, and browser privacy and responsive behavior. The
following controls were verified by focused tests and the repository's
runtime/installer checks:

- invitation-only, two-adult admission; scoped authorization; bounded Argon2id
  password work; short-lived hashed reset, invitation, session, and CSRF data;
- encrypted source, OpenAI-setting, and backup material derived from a retained
  master key; no plaintext credential values in normal command arguments,
  logs, or manifests, and no master or Plunk credential in backups;
- source-linked, review-gated imports; bounded local parsing; isolated
  no-network PDF parsing; explicit one-time visual-PDF transfer consent;
- authenticated deletion intent and recovery replay so deletion cannot be
  silently undone by a crash or older generation;
- optional OpenAI requests with minimized action-specific input, `store: false`,
  strict schemas, separate shared/private contexts, and no autonomous changes;
- signed release manifest, pinned publisher-key verification, digest checks
  before execution, atomic lifecycle activation, and exact Mithra-owned path
  boundaries that preserve unrelated services;
- health/status reporting that distinguishes a running service from a healthy
  application, plus encrypted backup verification that does not stop the
  service or expose key material.

## Residual risks and operator responsibilities

No application can eliminate the risks of a compromised host, stolen recovery
key, compromised email inbox, weak operator access controls, unavailable third
party services, or an operator's DNS/TLS/proxy mistake. OpenAI use necessarily
sends the narrowly scoped material for a user-requested action to OpenAI;
households that do not want that transfer should leave it disconnected.

Operators must keep the server patched, protect SSH and privileged access,
operate DNS/TLS/firewall/proxy infrastructure, retain the master key and tested
backups separately, and investigate health failures. Mithra deliberately does
not install packages, change global firewall policy, manage DNS, or own
unrelated proxy configuration.

## Recovery expectations

Regularly copy encrypted archives and the retained master key to separate
protected storage, then run `mithra-installer verify-backup --archive PATH`.
Practice restoration in a disposable environment. Restore authenticates and
stages data before activation, preserves deletion intent, and intentionally
clears access tokens, password hashes, OpenAI credentials, pending work, and
cached coaching; allowlisted adults re-bootstrap passwords and the owner
reconnects OpenAI afterward.

`uninstall` preserves data and recovery material. `purge --confirm-purge` is
irreversible and removes Mithra's retained data, backups, and master key. See
[self-hosting](self-hosting.md) and [backup and restore](backup-restore.md).
