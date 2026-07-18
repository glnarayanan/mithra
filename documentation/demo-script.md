# Mithra judge path and video script

## Private judge preparation

1. Put both private judge addresses in the installed `ALLOWED_EMAILS` value.
2. Run `sudo mithra-installer reset-demo --owner-email … --partner-email …`.
3. Open **Set or reset your password** in two fresh browser profiles. Deliver
   addresses, passwords, and the hosted URL only through Devpost's private
   testing instructions.
4. Keep a separate arbitrary household/account for the final general-purpose
   import check. Never place credentials, reset URLs, API keys, or session
   values in this repository, screenshots, narration, or the deployment receipt.

The reset receipt reports only the fixture version, record counts, and encrypted
backup path. It is safe to save locally but should not be copied into a public
submission if it reveals host paths.

## Four repeatable workflows

### 1. Finance import and exact changes

From **Bring in data**, upload a new CSV or XLSX containing income, spending,
an asset, and a pending obligation. Show the local extraction review, correct
one number if desired, import, then open **Money**. Point out number-only totals,
the category change, incomplete-state handling, the upcoming obligation, and
the source link. No currency selection or conversion is claimed in V1.

### 2. Health trend and unit mismatch

Upload a text-bearing health PDF. Review the mapped values, units, dates, and
reference range, then open **Health**. Show that different units remain separate
and the mismatch is explained. Enter the correct value and unit through the
correction form. State clearly: Mithra summarizes reported facts and trends; it
does not diagnose or provide medical advice.

### 3. Planning capture and calendar

Use **Capture** to enter a short plan by text (or demonstrate microphone denial
first, then voice). Confirm the proposed event, open **Plans**, and switch among
month, week, and agenda. Open the event's Google Calendar draft and download its
ICS file. Mithra neither uses calendar OAuth nor performs background sync.

### 4. Shared and private coaching

With both accounts in separate profiles, compare the shared **Family Brief**.
Then open **Week in Review** for each adult and show that the shared facts agree
while each **Only you** overlay contains only that adult's personal records.
Open an evidence link. Refresh coaching once to demonstrate calm AI wording;
the deterministic facts remain available if OpenAI is unavailable.

## Under-three-minute narrated video

- **0:00-0:20 — Problem and product.** Busy couples coordinate finance, health,
  home and plans across sheets, chats and apps. Mithra is one calm, factual
  household overview, not a taskmaster, mediator, medical adviser, or operator.
- **0:20-0:55 — Bring existing data.** Upload finance data, show review and exact
  totals. Briefly show the health PDF trend and mismatch correction.
- **0:55-1:25 — Natural capture.** Capture one plan, confirm it, and show the
  month/week/agenda views plus Google draft or ICS.
- **1:25-1:55 — Privacy-aware coaching.** Compare the shared Family Brief and
  two private Week in Review overlays, including one evidence link.
- **1:55-2:30 — What is real.** Mention the Go single binary, embedded UI,
  SQLite, encrypted sources/backups, allowlisted two-adult households, and the
  normal arbitrary-import acceptance path. State that AI never invents facts
  and page loads do not trigger model calls.
- **2:30-2:55 — Codex and GPT-5.6.** Explain that Codex carried the project from
  product interrogation through eleven atomic units, tests and browser QA;
  GPT-5.6 Sol reviewed architecture/security and GPT-5.6 Luna assisted build and
  debugging. Clarify that GPT-5.6 was a meaningful build tool, not a claimed
  Mithra runtime dependency.

Record at desktop width with readable zoom and real audio. Keep the final public
YouTube video at or below 2:55 so no required claim falls beyond Devpost's
three-minute judging boundary.
