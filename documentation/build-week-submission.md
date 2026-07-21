# OpenAI Build Week submission

Mithra is an **Apps for Your Life** entry. It is a family operating system for
busy couples: one place for finance, health records, household plans, upcoming
obligations, shared facts, and private adult records.

The public entry points are [mithrahq.com](https://mithrahq.com) and the
[Mithra repository](https://github.com/glnarayanan/mithra). Confirm the hosted
health check and the published release before submitting; this document does
not stand in for a live deployment check.

## Devpost copy

Use [submission/copy/devpost.md](../submission/copy/devpost.md) for the
description, build notes, challenges, and next steps. It describes the shipped
behaviour without promising medical advice, financial advice, relationship
mediation, background calendar sync, spending, booking, or automatic record
changes.

## What judges should see

1. **Week in Review** starts with a shared, deterministic weekly status.
2. It ranks no more than three priorities from valid shared records and gives a
   grounded next step for each.
3. It groups a clearly matched planning item and finance obligation once rather
   than repeating the same event.
4. Its observation uses recorded amounts, dates, budgets, and comparisons.
   Optional AI wording may add context, but it cannot replace those facts.
5. Each adult's **Only you** section stays separate. A private health or
   finance issue cannot change shared status, shared copy, or shared empty
   states.
6. Health comparisons name the measurement, retain the recorded unit, and do
   not make a medical claim. Incompatible readings show a correction notice
   instead.

## Judge access

The hosted app is invitation-only. Reset the marked synthetic household and
set the two synthetic judge passwords with the installed operator command:

```bash
sudo mithra-installer reset-demo \
  --owner-email judge-owner@example.com \
  --partner-email judge-partner@example.com \
  --owner-password-file /root/mithra-demo-owner.password \
  --partner-password-file /root/mithra-demo-partner.password
```

Both addresses must be allowlisted. The two password files must be private,
regular files owned by root. Reset changes only the marked synthetic household,
takes a verified encrypted backup first, and revokes its old sessions and reset
links. Paste the generated credentials only into the private Devpost testing
field. Use [the private template](../submission/copy/judge-instructions.template.md);
do not commit real passwords, reset links, API keys, or session values.

## Public assets

- [Video render](../submission/video/mithra-build-week-demo.mp4): 160
  seconds, 1920 × 1080, H.264/AAC, spoken narration, and burned-in captions.
  It uses no music, stock media, credentials, or personal data. See
  [render notes](../submission/copy/video.md).
- [Six screenshot assets](../submission/copy/screenshots.md): Owner and Partner
  desktop reviews, Owner mobile review, status/priorities, observation, and
  Owner private health.

## Codex and GPT-5.6

Codex inspected the existing Go and SQLite application, traced the shared
Family Brief and Week paths, then added Week-specific finance, planning, and
health handling. It also added tests, privacy checks, browser verification,
and the Remotion video. GPT-5.6 supported product critique, UX choices,
prioritisation, prompt work, focused implementation, and review. The builder
made the final scope, privacy, safety, and product decisions.

GPT-5.6 helped build Mithra; it is not presented as a required runtime model.
The app uses optional model providers only for bounded, evidence-linked
wording. Deterministic facts remain visible if a provider is unavailable.

## Submit checklist

- [ ] Select **Apps for Your Life**.
- [ ] Paste the current public repository and hosted URL.
- [ ] Upload `submission/video/mithra-build-week-demo.mp4` to YouTube and paste
  its public or unlisted URL.
- [ ] Upload the six screenshots in the manifest.
- [ ] Paste the private judge template after setting fresh synthetic passwords.
- [ ] Add the primary-build `/feedback` session ID, not a thread ID.
- [ ] Read the hosted app in clean Owner and Partner browser profiles.
- [ ] Check that no password, reset URL, API key, personal data, or local URL
  appears in public text, images, or video.
- [ ] Confirm the saved Devpost fields and final **Submitted** state.

For the longer walkthrough, use [the demo script](demo-script.md). For the
deployment check, use [the deployment receipt procedure](deployment-receipt.md)
and keep host-specific output in its ignored local receipt.
