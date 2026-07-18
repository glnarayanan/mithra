# Deployment receipt procedure

This file defines the data-free release receipt. Do not paste live hostnames,
emails, IP addresses, credentials, reset URLs, cookies, API keys, database
paths, or household data into Git. Save actual output to the ignored
`documentation/deployment-receipt.local.md`.

Record:

- UTC timestamp and operator;
- Git commit and clean/expected branch;
- application and installer SHA-256 values;
- schema/migration readiness result;
- installer status booleans, version, listener kind, socket mode/UID/GID, and
  backup/timer presence (never credential contents);
- signed release manifest verification result;
- predeploy backup verify-only result and forced rollback rehearsal result;
- Arivu immutable checksum/service/health baseline result when present;
- external HTTPS health result before and after one service restart;
- two fresh judge logins and the four workflows in `demo-script.md`;
- one separately imported household's post-restart result;
- desktop/tablet/mobile browser result and secret-scan result;
- final `GO` or `NO-GO`, with exact non-secret failure codes.

Any checksum mismatch, unavailable health endpoint, failed privacy boundary,
unverified backup, stale deployed commit, or missing judge workflow is `NO-GO`.
