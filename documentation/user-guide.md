# Using Mithra

Mithra is a place to keep household facts in view. It keeps finance, health,
and planning records linked to their **source** and **evidence** so you can
check what a view is based on. It is not a clinician, financial adviser,
mediator, reminder service, or automation tool.

## First use

Mithra has no public signup. An operator allowlists the adults who may use the
household. Use **Set or reset your password** from the sign-in page to activate
your allowlisted account. The first active adult becomes the household owner.
The owner can invite the second allowlisted adult from **Settings**; the invite
is email-bound and one-use.

Use **Help** in the application for short, contextual guidance. Press
<kbd>Ctrl</kbd>+<kbd>K</kbd> or <kbd>Command</kbd>+<kbd>K</kbd> for quick
navigation, or press <kbd>?</kbd> to see the shortcut guide. Quick navigation
only opens destinations; it cannot change records.

Outside form fields and open dialogs, use <kbd>Shift</kbd>+<kbd>C</kbd> for
**Capture**, <kbd>Shift</kbd>+<kbd>I</kbd> for **Import**, or the page shortcuts
listed in the in-app guide. These shortcuts only navigate; they never submit or
change a record.

## Shared and Only you

Choose a visibility every time you use **Capture** or **Import**:

- **Shared** records are visible to both active adults and can inform the
  household's Family Brief.
- **Only you** records are visible only to their owner. They never enter the
  other adult's views or shared wording.

Changing visibility is not a substitute for checking a record before you save
it. Records remain source-linked, and edits are saved as accountable revisions.

## Capture and Import

Use **Capture** for an update in your own words. A complete text update is
added after processing and can be undone for ten minutes. If a material date,
amount, unit, owner, or status is missing, Mithra asks one question rather than
inventing it. Voice captures wait for you to review the transcript and proposed
record before anything is added.

Use **Import** for one CSV, Excel, or PDF file at a time. Mithra reads it
locally first and proposes source-linked finance, health, or planning records.
Correct every required value, date, unit, and source location before importing.
Warnings ask for judgment; required corrections prevent an import. See
[document imports](imports.md) for format limits, review, deletion, and the
isolated PDF-parser boundary.

For a PDF without recoverable text, Mithra asks for one explicit visual-reading
confirmation. Continuing sends that one encrypted source to OpenAI to read the
visible pages; canceling sends no file and deletes the staged source.

Deleting a source also removes its records and unfinished actions. It is not an
archive action: Mithra will not make the deleted source available again.
Recovery preserves that deletion so an older backup cannot silently revive it.

## Finance, Health, and Planning

**Finance** shows recorded totals, recent changes, and upcoming payments. It
does not offer financial advice. **Health trends** keeps reported values,
appointments, and routines together; it does not diagnose or recommend
treatment. When results cannot be compared safely, Mithra keeps them separate
until you enter a corrected value and unit.

**Planning** keeps goals, milestones, constraints, and household dates
connected. Confirm the household timezone in **Settings** before exporting
timed events. Downloading an `.ics` file or opening a Google Calendar draft is
a one-time export: later changes in Mithra do not update another calendar.

## Family Brief and Week in Review

The **Family Brief** is the calm shared view. **Week in Review** is a factual
look at dates, conflicts, inconsistencies, and up to three evidence-backed
priorities. Their **Only you** sections are prepared separately. Neither view
is a score, verdict, diagnosis, or relationship judgment.

Opening a view does not call OpenAI. When available, an explicit refresh can
improve wording from the records that are visible to that audience. Mithra
keeps a record-based summary available when OpenAI is disconnected or cannot
answer. See [coaching](coaching.md) for the evidence and notification boundary.

## OpenAI and privacy

OpenAI is optional. The household owner can connect, replace, or disconnect an
OpenAI key in **Settings**. The saved key is encrypted and never displayed.
Without it, saved finance, health, and planning records still work. Capture,
Import organisation, and AI refresh actions remain unavailable.

For an action you request, Mithra sends only the material needed for that
action, separates shared and private contexts, and requests `store: false`.
OpenAI cannot sign in, see the saved key, change records, send invitations, or
act without confirmation. Read [security](security.md) for the full identity,
encryption, and provider contract.

## When something needs recovery

Ask the operator to preserve the master key and a verified encrypted backup.
Restoring a backup deliberately clears passwords, sessions, invitations,
OpenAI credentials, pending work, and cached coaching before current allowlist
members start again with **Set or reset your password**. The owner reconnects
OpenAI afterward. See [backup and restore](backup-restore.md).
