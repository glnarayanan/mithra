# Mithra — Devpost copy

## Tagline

A private family operating system that turns household facts into a calm weekly
plan.

## Description

Busy couples keep money, health records, household plans, and upcoming
obligations across spreadsheets, apps, and messages. Mithra brings those facts
into one shared home while letting each adult keep selected records **Only
you**.

People can capture an update in their own words or import CSV, XLSX, and PDF
files. Mithra turns them into typed, evidence-linked records that people review
and correct. Finance shows exact recorded totals and trends. Health keeps
measurements and reports together without diagnosis. Planning holds dates,
timed events, and one-time calendar exports.

The core experience is Week in Review. It starts with a clear household status,
ranks no more than three priorities, groups only clearly related planning and
finance records, shows what changed and what is next, and gives a factual
observation from valid data. A private health unit mismatch appears as a
correction notice, not a health conclusion. Private records never change a
partner's shared status, wording, or empty states.

Mithra uses deterministic calculations for dates, totals, budgets,
comparisons, validation, and privacy. An optional model provider can improve
the wording of evidence-linked coaching, but it cannot invent facts, change a
record, spend money, make a booking, provide medical or financial advice, or
mediate a relationship.

Mithra is a low-dependency Go application with an embedded UI, SQLite,
encrypted sources and backups, and invitation-only two-adult households. It can
run on a household's own server.

## What we built

- A shared Family Brief for current household facts.
- Finance, health, planning, capture, import, and source review.
- Week in Review with deterministic status, three priorities, grouped events,
  grounded finance observations, progress, and upcoming items.
- Separate **Only you** views for each adult.
- Evidence links, private source storage, encrypted backups, and a
  self-hosting installer.
- Optional model-provider coaching with strict evidence and safety checks.

## How we built it

Mithra uses Go, SQLite, embedded HTML/CSS/JavaScript, and a single-binary
deployment model. It uses typed finance, health, and planning records instead
of database search text for user-facing review content. The review calculates
facts, eligibility, privacy, validation, and ranking before optional AI wording
runs.

Codex inspected the existing repository, traced the Family Brief and Week
paths, added Week-specific record handling, tests, privacy checks, browser
verification, and the Remotion demo. GPT-5.6 supported product critique, UX
choices, prioritisation, prompt work, focused implementation, and review. The
builder made the final scope, privacy, safety, and product decisions.

## Challenges

The hard part was not making a record list. It was giving a couple a useful
weekly readout without turning old imports into changes, treating a past
transaction as unfinished work, repeating one event across sections, or leaking
private health or finance information. Mithra now uses typed records, strict
section assignment, conservative event matching, and separate shared and
personal contexts.

## What is next

We will learn from real household use before adding broader family roles or
more domains. The next work will focus on better import correction, stronger
trend views, and carefully bounded coaching while keeping people in control.

## Links

- Hosted app: <https://mithrahq.com>
- Source code: <https://github.com/glnarayanan/mithra>
- Demo video: upload `submission/video/mithra-build-week-demo.mp4` to YouTube
  and paste the public or unlisted link here.
