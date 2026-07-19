---
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
execution: code
title: Mithra Product Guidance, Security, and CLI
date: 2026-07-19
---

# Mithra Product Guidance, Security, and CLI

## Goal Capsule

Build a trustworthy post-launch layer around Mithra: authenticated in-app Help,
contextual guidance, an accessible quick-navigation palette, consistent product
language, complete repository and self-hosting documentation, a coherent installer
CLI, and remediation of every confirmed P0-P2 security defect.

Preserve the Go/SQLite single-binary architecture, embedded dependency-free
frontend, current two-adult privacy model, and the boundary that the installer
manages Mithra but does not prepare a VPS.

## Public Interfaces

- Add authenticated `GET /help`, linked from every authenticated surface.
- Add a non-destructive quick-navigation palette opened with `Ctrl+K` or
  `Command+K`; it navigates only to Brief, Week in Review, Capture, Import,
  Finance, Health, Planning, Settings, or Help.
- Add `mithra help`, `mithra --help`, and `mithra version`.
- Add `mithra-installer help [COMMAND]`, `mithra-installer COMMAND --help`,
  `mithra-installer version`, `mithra-installer plan [OPERATION]`,
  `mithra-installer verify-backup --archive PATH`, and
  `mithra-installer completion bash|zsh|fish`.
- Preserve bare `mithra` as serve and bare `mithra-installer plan` as install
  planning.
- Keep `pdf-parser`, release-artifact flags, and `--root` as hidden internal
  surfaces and validate them strictly.
- Persist proxy mode and app-only canonical origin in runtime configuration.
- Keep status JSON backward compatible while distinguishing systemd activity
  from actual application health.

## Key Decisions

- In-app Help contains concise task guidance; repository documentation remains
  the detailed source for users and operators. No Markdown renderer or content
  framework is added.
- UI labels use the canonical vocabulary: “Family Brief,” “Week in Review,”
  “Only you,” “Shared,” “Capture,” “Import,” “source,” and “evidence.”
- Copy remains calm, factual, privacy-explicit, non-judgmental, and free of
  medical or financial advice.
- The quick-navigation palette uses a modifier shortcut, ignores editable
  controls and composition events, cannot submit or mutate data, and restores
  focus when closed.
- Installer help, dispatch, flag scoping, and completion scripts derive from
  one standard-library command registry. No CLI dependency is added.
- Security work is finding-driven. Confirmed P0-P2 defects block completion;
  speculative P3 hardening does not.
- Detailed vulnerability evidence stays out of public documentation until
  remediation lands. The final repository contains a safe audit summary,
  verified controls, residual risks, and recovery expectations.
- Root-executed release bytes must be authenticated before execution. The
  official installation flow becomes download, verify the signed manifest and
  script digest, then execute. Direct `curl | sudo sh` is not documented.

## Scope Boundaries

- No new production dependency.
- No localization or copy-catalog abstraction.
- No VPS preparation, package installation, DNS management, firewall policy,
  or ownership of unrelated proxy configuration.
- No calendar synchronization, provider execution expansion, medical advice,
  financial advice, mediation, diagnosis, blame, or nagging.
- Release or deployment to the live VPS remains separately authorized.

## Implementation Units

### U1. Application, import, and provider security

**Goal:** Remediate confirmed application-side findings and finish the bounded
application security audit.

**Files:** `internal/imports/review.go`, `internal/imports/*_test.go`,
`internal/providers/plunk.go`, `internal/providers/plunk_test.go`,
`.github/workflows/ci.yml`, `web/static/`.

**Approach:**

- Make import commit, discard, visual abort, and abandoned-cleanup state claims
  transactional so ciphertext cannot be removed after another path publishes
  the source.
- Reject Plunk redirects and verify the fixed final HTTPS endpoint without
  reflecting provider responses.
- Run syntax checks and native tests for every first-party JavaScript file.
- Audit remaining browser, authorization, upload, provider, job, logging, and
  data-lifecycle surfaces against OWASP ASVS 5.0 without reopening proven
  controls.

**Verification:** Concurrent commit and cleanup always leave a complete commit
with ciphertext or a complete discard; Plunk 3xx responses never transmit to a
redirect target; all first-party JavaScript is checked; existing security and
deletion-journal tests remain green.

### U2. Restore, proxy, and PDF-parser lifecycle

**Goal:** Make restore, rollback, proxy selection, and isolated PDF parsing
operationally safe.

**Files:** `internal/installer/`, `cmd/mithra-installer/`, `deploy/systemd/`.

**Approach:**

- Apply the Mithra service UID/GID and exact modes to staged restore generations
  before activation.
- Preserve committed and superseded import metadata during restore; discard
  only incomplete review, consent, job, session, and provider state.
- Run archive, key, allowlist, journal, migration, and ownership preflight
  before quiescing the service.
- Persist selected proxy mode; infer it for older installations only from
  Mithra-owned artifacts.
- Derive app-only canonical origin from the selected loopback port.
- Strictly validate DNS hostnames before rendering proxy configuration.
- Own, install, enable, report, roll back, and uninstall the isolated PDF-parser
  user, socket, and service.

**Verification:** Invalid restore inputs fail before shutdown; restored state is
writable and retains deletion capability; rollback restores ownership; hostile
hostnames are rejected; parser isolation is enforced; custom-port app-only links
use the correct origin.

### U3. Release trust and lifecycle activation

**Goal:** Authenticate all root-executed release assets and make upgrade and
reconfigure usable.

**Files:** `.github/workflows/release.yml`, `deploy/install.sh`,
`internal/installer/release.go`, `cmd/mithra-installer/main.go`.

**Approach:**

- Render `install.sh` before the canonical manifest and include its digest in
  the signed manifest.
- Publish the pinned public-key fingerprint and a verify-before-execute
  procedure; enable immutable GitHub releases where available.
- Default `install.sh` to install and accept explicit upgrade using the
  downloaded target installer.
- Let reconfigure use the installed installer without release artifacts; keep
  compatible release flags accepted when supplied.
- Load the installed allowlist for upgrade when omitted.
- Verify tag, manifest, candidate installer, candidate app, and installed-version
  transition before staging.
- Add the `base64` prerequisite and inject release version metadata into both
  binaries.

**Verification:** Tampering and tag mismatch fail before root execution or
owned-path mutation; existing install invocation remains valid; upgrade
preserves data, keys, proxy, and allowlist; reconfigure rotates configuration
without unnecessary binary replacement.

### U4. CLI contract and shell completions

**Goal:** Provide predictable, documented, backward-compatible operator
commands.

**Files:** `cmd/mithra-installer/*.go`, `cmd/mithra-installer/*_test.go`,
`cmd/mithra/*.go`, `internal/installer/installer.go`.

**Approach:**

- Use one `flag.FlagSet` per command with shared command metadata.
- Help exits zero on stdout; usage failures exit two on stderr; operational
  failures exit one without usage output.
- Reject unrelated flags before host discovery.
- Support planning every mutating lifecycle operation while keeping bare `plan`
  compatible.
- Add read-only backup verification with a bounded machine-readable receipt and
  no quiesce or mutation.
- Generate deterministic Bash, Zsh, and Fish completions to stdout without
  writes, privilege checks, discovery, secrets, or installer operations.
- Keep `pdf-parser` hidden and reject extra arguments.
- Validate hidden `--root` as an absolute, clean, non-symlink path.

**Verification:** Help and failures use correct streams and exit classes;
legacy commands parse where meaningful; completions contain public commands and
scoped flags and are syntax-valid when their shell exists; status distinguishes
active-unhealthy from healthy; backup verification never stops the service or
exposes keys.

### U5. In-app Help and product-language review

**Goal:** Explain Mithra inside the product and make user-facing language
consistent.

**Files:** `internal/app/app.go`, `internal/app/help_handlers.go`,
`internal/app/*_handlers.go`, `web/embed.go`, `web/templates/`.

**Approach:**

- Add authenticated `/help` for first use, privacy, Capture, Import, Finance,
  Health, Planning, Family Brief, Week in Review, OpenAI boundaries, source
  deletion, and recovery expectations.
- Add contextual Help links at visual-PDF transfer, health corrections, calendar
  export, AI connection, and shared/private coaching boundaries.
- Review navigation, headings, labels, actions, state messages, email, nudges,
  and coaching prompts in their owning files.
- Do not add localization or copy-catalog abstractions.

**Verification:** Unauthenticated Help redirects; every authenticated shell
links Help; vocabulary and privacy promises agree; dynamic values remain escaped;
copy never implies execution, diagnosis, advice, blame, mediation, certainty, or
nagging.

### U6. Accessible quick navigation

**Goal:** Add `Ctrl/Command+K` navigation without weakening ordinary keyboard
use.

**Files:** `web/static/app.js`, `web/static/app.test.mjs`,
`web/static/styles.css`, `web/templates/`, `internal/app/app_test.go`.

**Approach:**

- Load the global script only on authenticated pages.
- Build a native modal navigation palette with a visible trigger, filtering,
  arrow selection, Enter navigation, Escape close, and focus restoration.
- Expose the binding through `aria-keyshortcuts` and Help.
- Ignore shortcuts in editable controls, during composition, on repeat, and
  while another modal is active.
- Include navigation destinations only; no mutating actions.

**Verification:** Keyboard-only navigation works and restores focus; editable
typing is unaffected; palette actions cannot mutate; desktop/mobile focus and
scrolling work; no-JavaScript users retain ordinary navigation and Help.

### U7. Repository documentation and public audit record

**Goal:** Make the repository usable by household members and self-hosting
operators.

**Files:** `README.md`, `documentation/user-guide.md`,
`documentation/self-hosting.md`, `documentation/security-audit.md`, and relevant
existing `documentation/*.md`.

**Approach:**

- Add a user guide that matches in-app vocabulary and expands workflow/privacy
  explanations.
- Add an end-to-end self-hosting guide for prerequisites, DNS/TLS responsibility,
  credentials, app-only/proxy modes, verified install, first login, OpenAI,
  partner invitation, health/status, backup verification, upgrade, reconfigure,
  restore, completions, troubleshooting, uninstall, and purge.
- Link existing architecture, operations, security, import, coaching, and backup
  documents instead of duplicating contracts.
- Correct stale architecture references and CLI examples.
- Publish a safe post-remediation audit summary with reviewed surfaces, fixed
  findings, verified controls, accepted residual risks, and validation date.

**Verification:** Every documented command and flag matches CLI help; the guide
does not imply VPS preparation or unrelated infrastructure ownership; lifecycle
and recovery consequences are explicit; README stays a lean entrypoint.

## Verification Contract

- Run repository formatting, `go mod verify`, `go vet`, uncached tagged Go tests,
  race tests, both binary builds, shell syntax checks, all JavaScript syntax and
  tests, diff checks, and the pinned `govulncheck`.
- Add bounded fuzz coverage for hostname/config rendering, release manifests,
  backup/archive parsing, and import lifecycle transitions when it materially
  exercises untrusted input.
- Run `systemd-analyze verify` for generated units; inspect
  `systemd-analyze security --offline` and accept no unexplained privilege or
  writable-path regression.
- Run one consolidated Sol High security review against OWASP ASVS 5.0 with no
  unresolved P0-P2 findings.
- Run authenticated real-browser desktop and mobile QA for Help, contextual
  links, palette focus, typing suppression, responsive layout, and console errors.
- Exercise install, status, backup verification, upgrade, reconfigure, restore
  failure/rollback, uninstall, and parser activation in a disposable
  systemd/proxy fixture while proving Arivu remains unchanged.
- Run Ponytail Audit and CE Code Review after the security, CLI, and
  UX/documentation implementation groups.

## Definition of Done

- All confirmed P1/P2 findings have regression tests and are remediated.
- Help, guides, CLI help, completions, and visible copy agree.
- Product, privacy, AI, and low-dependency boundaries remain intact.
- Existing installer invocations remain compatible or have an explicit safe
  compatibility path.
- No unverified downloaded asset is executed as root by the documented flow.
- No new production dependency is introduced.
- Obsolete copy, stale documentation, temporary audit artifacts, and abandoned
  implementation paths are removed.
- Release or deployment to the live VPS remains a separately authorized action.
