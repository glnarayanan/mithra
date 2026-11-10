# Moving from TinyTots and HealthTrends to Mithra

TinyTots and HealthTrends are now part of Mithra. This page records what moved
where, how to bring data across, what was not carried over, and the steps to
retire the old apps. The old repositories stay private and are archived, not
maintained.

## Where each feature lives now

| Old app | Feature | In Mithra |
| --- | --- | --- |
| TinyTots | Today screen, feeds, diapers, spit-ups | Family, then the child's page, Baby log |
| TinyTots | Weights and milestones | Baby log, Weight and Milestones |
| TinyTots | Time since last feed, today's counts | Baby log hero, and a panel on Home |
| TinyTots | Offline outbox | Baby log queues entries while offline |
| TinyTots | Night theme | Mithra night theme, everywhere |
| HealthTrends | Lab values, trend charts, range status | Health, grouped cards with sparklines |
| HealthTrends | Per-person view and switcher | Health person switch, and each person's Family page |
| HealthTrends | Manual entry with a biomarker catalog | Health, Add a measurement (66 tests) |
| HealthTrends | Habits and check-ins | Health and person pages, Habits |
| HealthTrends | Medicines and appointment notes | Health and person pages |
| HealthTrends | PDF lab report upload and review | Import, reviewed before anything is saved |

## Bringing data across

Only TinyTots weights are being moved. HealthTrends has no data to move.

1. In TinyTots, open the child's export and download the CSV.
2. In Mithra, add the child under **Family** with their date of birth.
3. On the child's page, open **Import weights from TinyTots** and choose the
   file. Only weight rows are added; the TinyTots id of each row is kept, so
   a second import of the same file adds nothing.
4. Check the Weight list against TinyTots before retiring it.

## Not carried over

- **Web Push reminders and Telegram logging** from TinyTots. Both need
  credentials (VAPID keys, a bot token) and a product decision before they
  are rebuilt in Mithra.
- **Feed and diaper history import.** The importer takes weights only. The
  same CSV layout could carry the other rows later.
- **HealthTrends AI chat, report compare and profile merge.** Mithra's AI
  summaries on Home cover the overview; the rest can follow if missed.
- **Spit-up linked to a feed** (TinyTots `linked_feed`). Spit-ups are logged
  on their own.

## Retiring the old apps

These steps cannot be undone and are for the owner to run.

1. Confirm the weights in Mithra match TinyTots.
2. Point `tinytots.mithrahq.com` at a permanent redirect (301) to
   `https://app.mithrahq.com/family`. The installed PWA will open the redirect
   and can then be removed from the home screen. If HealthTrends had its own
   host, redirect it to `https://app.mithrahq.com/health`.
3. Stop the old containers in Dokploy and keep one final backup of each
   SQLite file (`tinytots.sqlite`, `healthtrends.db`) offline.
4. Archive both GitHub repositories. They stay private.
5. Update mithrahq.com so TinyTots and HealthTrends read as parts of Mithra
   rather than separate apps.
