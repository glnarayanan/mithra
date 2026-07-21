# Mithra judge path and video script

## Private judge preparation

1. Add two synthetic judge email addresses to the installed `ALLOWED_EMAILS`
   value.
2. Create two private, root-owned password files. Use only synthetic passwords
   of 12–128 bytes.
3. Reset the marked fixture and set those passwords:

   ```bash
   sudo mithra-installer reset-demo \
     --owner-email judge-owner@example.com \
     --partner-email judge-partner@example.com \
     --owner-password-file /root/mithra-demo-owner.password \
     --partner-password-file /root/mithra-demo-partner.password
   ```

4. Open the hosted app in two clean browser profiles. Sign in as each adult and
   confirm that shared material matches while **Only you** differs.
5. Give the hosted URL, two synthetic accounts, and this short walkthrough only
   in Devpost's private testing field. Use
   [the template](../submission/copy/judge-instructions.template.md).

`reset-demo` makes and verifies an encrypted backup before it changes the
fixture. It rejects ordinary households, revokes old sessions and reset links
for the two demo accounts, and does not change unrelated households. Never put
passwords, reset URLs, API keys, session values, or host paths in the
repository, screenshots, narration, or public Devpost text.

## Four judge workflows

### 1. Week in Review

Open **Week in Review** as the Owner. Show the weekly status, three priorities,
Mithra's factual observation, progress, and upcoming dates. Point out that the
insurance planning item and payment are one grouped event when their titles,
dates, and scope match. Open **Details** for an evidence link. The review works
from deterministic facts even when no model provider is available.

### 2. Private health safety

In the Owner profile, show **Only you**. A named glucose comparison keeps its
recorded unit and says that it is a record comparison, not medical advice. Show
the single unit-correction notice. In the Partner profile, confirm that this
private health issue does not change shared status, shared wording, or shared
empty states.

### 3. Finance and planning

Open **Finance** to show recorded totals, budget headroom, and source details.
Open **Planning** to show dated household items, a timed event, and the
month/week/agenda views. A calendar export opens a reviewed Google Calendar
draft or downloads an ICS file; Mithra has no calendar OAuth or background
sync.

### 4. Bring existing records

From **Import**, upload a CSV, XLSX, or text-bearing PDF under the file limit.
Review the proposed records, correct them, and commit. The source stays linked
to the records. For a text update, use quick capture and confirm the proposed
record before it is saved. Voice and visual-PDF reading require OpenAI; the
rest of the application does not depend on it.

## Public video

The final video render is
[`submission/video/mithra-build-week-demo.mp4`](../submission/video/mithra-build-week-demo.mp4).
It runs for 160 seconds and includes clear narration, captions, and the final
clean Owner and Partner captures.

1. Start with the problem: couples split money, health, plans, and household
   work across separate tools.
2. Show the Family Brief, Finance, Health, Planning, and then spend most of the
   video on Week in Review.
3. Show the weekly status, three priorities, grouped insurance event, grocery
   observation, progress, upcoming items, and the private health area.
4. Compare Owner and Partner views without showing login details.
5. State that Codex traced and changed the application, tests, browser checks,
   and video; GPT-5.6 supported critique, UX choices, prioritisation, prompt
   work, implementation, and review; the builder made the final decisions.
6. End on Mithra, the hosted demo, and the public repository.

Do not show developer tools, passwords, secrets, personal data, local URLs, or
unlicensed media. Upload the final file as public or unlisted and paste its URL
into Devpost.
