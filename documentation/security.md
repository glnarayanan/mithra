# Identity and browser security

## Admission and household lifecycle

Mithra has no signup route. At startup, the operator allowlist is synchronized
into pending, active, or disabled users. A reset request always receives the
same acknowledgement, while only an eligible allowlisted user receives mail.
The first activated adult owns a one-person household. A second adult joins
only with an allowlisted, email-bound, one-use invitation; database constraints
cap membership at two.

Removing any user revokes their sessions, reset tokens, invitations, and
undelivered access. Removing an owner closes the household and revokes every
member session, so an ownerless household exposes no data. The explicit
`mithra recover-owner` operator operation may promote an active or pending
allowlisted adult already in that closed household, or attach an unbound adult.
It rejects disabled or foreign-household candidates. A pending candidate stays
pending after reassignment and must complete normal password bootstrap before
any session can be created.

## Passwords, tokens, and sessions

Passwords are bounded and hashed with Argon2id. A process-wide semaphore caps
concurrent password work, and SQLite throttles run before expensive hashing.
Reset, invitation, session, and CSRF values are generated from `crypto/rand`;
only SHA-256 hashes are stored.

Reset and invitation GET requests do not consume a token. They place it in a
short-lived HttpOnly SameSite cookie and redirect with `no-referrer` to a
query-free URL before rendering. Password setup consumes either a reset or a
first-use invitation; an invitation alone can set only an account with no
existing password. Membership, password activation, token consumption, prior
session revocation, and fresh session creation commit atomically. Production
cookies use the `__Host-` prefix, Secure, HttpOnly, Path `/`, and SameSite.
Authenticated requests recheck current user, membership, and household state
in SQLite.

## Request boundary

Every mutation requires a session-bound synchronizer token plus a canonical
Origin or Referer. Cross-site Fetch Metadata is rejected. Forms, query values,
request bodies, response bodies, headers, and password work are bounded.
Authenticated and bootstrap responses use `no-store`. `same-origin` referrers
preserve CSRF evidence for form posts while preventing any referrer from being
sent off-site; bearer-token GETs redirect to a clean URL before rendering.

The canonical origin is configuration, never `Host` or a forwarding header.
HTTPS is required outside literal loopback development. TCP mode ignores
`X-Forwarded-For`. Permissioned Unix-socket mode accepts exactly one valid IP
from it and hashes that IP into a throttle key; invalid or multi-hop values use
one shared proxy key. Forwarding data never determines an actor, household,
link, or authorization decision.

## Data authorization

One policy package derives actor, household, and role from the current server
session. Personal is the default visibility. Shared records are readable and
editable by both adults; personal records only by their owner. Every mutation
checks the resource household and expected version, so cross-household and stale
edits fail without revealing foreign existence.

## Email credential and logs

The Resend key is read from an absolute, regular, service-user-owned credential
file with no group or other permissions. The file is bounded and checked after
opening to prevent symlink or replacement substitution. The key is never an
argument or ordinary environment value. The sender identity is non-secret
configuration, and the client uses a fixed HTTPS endpoint with bounded time and
response size.

Runtime logs are allowlisted to request IDs and stable error codes. They omit
emails, token URLs, queries, filenames, credential paths and values, provider
bodies, and household content. Startup errors intentionally emit only
`error_code=startup_failed`.
