# Coaching and restrained notifications

Mithra's Family Brief and Week in Review are read-only coaching views. Page
loads build deterministic, actor-scoped facts and signals from active,
source-linked finance, health, and planning records. They never call a model
provider.

## Week in Review

Week in Review has its own typed processing model. It does not reuse the Family
Brief sections. The shared review uses shared, valid records only and can show:

- A deterministic status: **On track**, **Mostly on track**, or **Needs
  attention**.
- No more than three ranked priorities.
- A deterministic finance observation, progress, and upcoming items.
- Optional cached, evidence-linked AI wording.

The status and priorities use overdue actionable records, near dates, pending
obligations, validation blockers, and valid finance signals. Completed spending
and historical health measurements are not treated as overdue work. Imported
old records do not count as a new real-world change. A future record belongs in
upcoming items, not what changed. Completed, corrected, cancelled, reopened,
and newly overdue records may appear once as a meaningful change.

Mithra combines a planning item and a finance obligation only when they share a
privacy scope, their normalised titles strongly match, and their dates are no
more than three days apart. It combines no ambiguous match. A grouped event
keeps the underlying evidence links and appears once in the review.

Health comparisons use compatible, valid readings only. They name the
measurement and retain its recorded unit. An incompatible or invalid reading
creates a data-quality notice; it cannot support a comparison, shared status,
or coaching claim. Mithra does not interpret health data as medical advice.

## Privacy and generated wording

Shared construction cannot read a personal row. The **Only you** context is
owner-scoped and renders as a separate landmark. A private health or finance
issue cannot affect shared status, shared wording, shared empty states, or
shared section visibility. Each adult sees only their own personal context.

An explicit refresh sends shared and personal contexts in separate requests and
publishes wording only after membership, revisions, evidence, and source
visibility are checked again. Shared cache identity excludes personal revisions
and private facts. Cached wording is marked stale after ordinary revision
changes. A successful refresh keeps up to twelve actor-scoped history entries
for each mode and visibility. If cited evidence is no longer visible, or
membership or source privacy changes, Mithra deletes the current and saved
wording instead of serving it.

Every generated item must cite an opaque evidence ID from its exact prompt
context. Exact amounts, percentages, and comparisons come from supplied
deterministic signals. Mithra rejects unknown evidence, invented numbers,
unsupported comparisons, causal claims, medical or financial advice, diagnosis,
scores, blame, mediation, commands, nagging, and reset framing. If one item
fails those checks, Mithra drops it and keeps any other valid item. If none
pass, the refresh fails without replacing the saved view. Deterministic Week in
Review content remains available, and priorities never exceed three.

## Notifications

An inconsistency may create one in-app nudge and one generic email that only
links to Mithra. Follow-up is off by default and can send at most once after
explicit opt-in. Corrected, inaccessible, acknowledged, stale, or completed
records leave the awaiting-update state and cannot keep emailing. A Week in
Review correction notice suppresses the matching duplicate follow-up from that
page; it does not delete the stored nudge.
