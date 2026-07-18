# Document imports

Mithra accepts exactly one CSV, XLSX, or PDF up to 10 MB. The browser performs a quick extension and size check; the server independently checks the declared media type, container signature, archive expansion, row, cell, page, text, response, and time limits.

CSV uses the Go standard library. XLSX uses the pinned Excelize module after ZIP preflight. PDF bytes are sent over bounded Unix-socket IPC to `mithra pdf-parser`, the same binary running as a separate systemd identity with no network, credentials, database, source-directory, or inherited application descriptor access. The parser receives bytes, never a plaintext path.

For ordinary files, Mithra sends OpenAI only minimized locally extracted text with row, cell, or page locators and `store: false`. A PDF with no recoverable text stops before provider transfer. The page explains the one-file visual fallback and issues a ten-minute, single-use confirmation bound to the current actor, household, source digest and version, visibility, membership, and source state. Cancellation deletes the encrypted staging source without transmitting it.

## Review and commit

AI proposes typed finance, health, and planning records. Missing or invalid numbers, dates, units, record types, or evidence are blockers; possible duplicates and inconsistencies remain warnings. User edits are persisted as user-generated values. Final import rechecks membership, source identity, visibility, source and review versions, and both data revisions, then creates all domain records and marks the import committed in one SQLite transaction. No page load invokes OpenAI.

Exact duplicate suppression is scoped to household, actor, visibility, and digest. Filenames never establish a version relationship. A changed file stays independent unless the user opens **Import new version** on a prior import; the final transaction then retires only that prior import's active records as it publishes the reviewed successor.

## Deletion

Deletion first shows source visibility plus dependent active-record and pending-job counts. A short-lived confirmation appends and fsyncs an authenticated, encrypted intent to `deletion.journal` before SQLite tombstones the source, every source-linked live record, evidence, search entry, and pending job in one transaction. Ciphertext removal follows as idempotent physical cleanup. Startup replays every journal intent before serving, so a crash or older database generation cannot make a deleted source live again.

The journal is recovery state: preserve it with the database, encrypted sources, backups, and master key. It contains no plaintext household identifiers or source paths.
