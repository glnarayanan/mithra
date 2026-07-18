# OpenAI Build Week submission

Verified against the official [OpenAI Build Week rules](https://openai.devpost.com/rules)
and [FAQ](https://openai.devpost.com/details/faqs) on 2026-07-18. The submission
deadline is 2026-07-21 at 5:00 PM Pacific. Recheck the live pages before the
final submission because Devpost controls the requirements.

## Submission draft

- **Project:** Mithra
- **Track:** Apps for Your Life
- **Tagline:** A calm, private household coach for the facts of family life.
- **Working description:** Mithra gives busy couples one shared place for
  finance, health trends, planning, imports, conversational updates, calendar
  views, a Family Brief, and a private Week in Review. It converts text, voice,
  CSV, XLSX, and PDF inputs into typed, evidence-linked records that users review
  and correct. Deterministic views work without AI; optional OpenAI coaching is
  factual, evidence-bound, private by scope, and never medical advice,
  relationship judgment, spending, booking, or record-changing automation.
  Mithra ships as a low-dependency Go binary with embedded UI, SQLite, encrypted
  sources and backups, invitation-only two-adult households, and an Arivu-safe
  shared-VPS installer.
- **Why it matters:** Couples already have systems—the problem is fragmentation.
  Mithra lowers capture and switching cost without asking a family to surrender
  privacy or hand operational authority to an AI.

## Judging-criteria evidence

| Criterion | Observable evidence |
|---|---|
| Technological implementation | Eleven atomic Go/SQLite units; local-first CSV/XLSX/PDF extraction; strict provider schemas; encrypted source/backup state; privacy and restart acceptance; signed rollback-safe installer. |
| Design | One coherent responsive shell across capture, imports, finance, health, calendar, Family Brief, and Week in Review; explicit empty/error/stale states and evidence links. |
| Potential impact | Replaces fragmented couple workflows while preserving existing files, private overlays, user correction, and factual boundaries. |
| Quality of idea | A superapp whose unifying layer is a permissioned, non-executing household coach—not a bundle of mini-apps or another reminder list. |

## Codex and GPT-5.6 evidence map

Primary Codex build task: `019f7561-247d-7d60-b17e-e046156f8fdf`.

| Evidence | Contribution and result |
|---|---|
| `85e8b48` | Product interrogation settled the couples-only V1, shared/personal privacy, calm coach, no execution, low-dependency Go/SQLite binary, installer, imports, and full superapp scope. |
| `952c2d0`–`dcefeb9` | Codex implementation plus GPT-5.6 architecture/security review established the runtime, allowlisted household access, encryption, providers, and durable jobs. |
| `62afe7b`–`bc7d382` | Finance, factual health, full calendar, conversational capture, local-first imports, Family Brief, and Week in Review landed with focused tests and consolidated GPT-5.6 Sol review fixes. |
| `c483cf4` | GPT-5.6 Sol review drove encrypted backups, deletion-journal reconciliation, exact rollback errors, symlink defenses, Caddy ownership, and Arivu baseline verification. |
| U11 commit | Fixture-only reset, arbitrary-household restart acceptance, judge path, browser QA, deployment receipt, and submission audit. |

GPT-5.6 Sol was used for high-rigor consolidated review and root-cause fixes;
GPT-5.6 Luna assisted implementation and debugging. The product owner made the
defining scope and privacy decisions. GPT-5.6 is meaningful build evidence and
is not represented as an in-product runtime requirement. Mithra's optional
runtime models are `gpt-5.4-mini` and `gpt-4o-mini-transcribe`.

## Required final fields and access

- [ ] Hosted HTTPS URL remains free and available through the end of judging.
- [ ] Private testing instructions contain two judge accounts and the four
  workflows in `demo-script.md`; credentials appear nowhere public.
- [ ] Repository URL is public with an appropriate license, or the private repo
  is shared with `testing@devpost.com` and `build-week-event@openai.com`.
- [ ] Public YouTube video is 3:00 or shorter, has clear audio, demonstrates the
  working product, and specifically explains Codex and GPT-5.6 contributions.
- [ ] Devpost text description and **Apps for Your Life** category are set.
- [ ] The `/feedback` Codex Session ID is generated in the primary build task
  and pasted into Devpost; a thread ID is not substituted for it.
- [ ] README setup/sample-data/testing and Codex/GPT-5.6 sections match the
  deployed commit.
- [ ] No third-party trademarks, unlicensed music, real personal data, or
  credentials appear in public artifacts.
- [ ] Final external check is repeated after service restart and immediately
  before submission; commit and binary SHA-256 values match the local receipt.

## Go/no-go commands

Use `documentation/deployment-receipt.md` as the procedure and save real output
only to ignored `documentation/deployment-receipt.local.md`.

```bash
git status --short
git rev-parse HEAD
shasum -a 256 /usr/local/bin/mithra /usr/local/bin/mithra-installer
sudo mithra-installer status
curl --fail --silent --show-error https://YOUR_HOST/healthz
```

Do not submit until every external checkbox above is complete. Devpost notes
that judges may rely only on the description, images, and video, so each must
stand on its own even though private working access is provided.
