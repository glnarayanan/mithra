# Backup and restore

Mithra backups capture one SQLite, encrypted-source, and deletion-journal
generation. The service is quiesced by the lifecycle operation before the
final snapshot. SQLite passes `quick_check` and a full WAL checkpoint, then a
native SQLite snapshot is created. Every archived file has an exact size and
SHA-256 digest. A manifest records the retained-key fingerprint and complete
generation digest and is authenticated with a key derived from the master key.
The key and Plunk credential are never archived. The timer retains the seven
most recent successfully committed archives.

```bash
sudo mithra-installer backup
sudo mithra-installer status
```

Copy successful archives and the master-key credential to separate protected
storage. A recovery drill must use a disposable host or `--root` sandbox and
must verify that eligible users can complete the normal password bootstrap.

## Restore boundary

Restore requires an archive, the matching retained key, a readable deletion
journal, and the current allowlist:

```bash
sudo mithra-installer restore \
  --archive /secure/mithra-20260718T120000.000000000Z.mbackup \
  --allowed-emails owner@example.com,partner@example.com
```

The archive is bounded and extracted into a private sibling staging directory;
absolute paths, traversal, links, duplicate files, truncation, unknown key
fingerprints, manifest tampering, digest mismatch, corrupt SQLite, or an
invalid journal fail before live data changes. Deletion intents are replayed
against the staged database and ciphertext directory, so an archive older than
a deletion cannot resurrect that source or its cascade-derived records.

Before activation, restore clears all password hashes, OpenAI credentials,
browser sessions, reset and invitation tokens, throttles, jobs and leases,
pending document work, coaching caches, nudges, and pending email work. The
current allowlist is reconciled and eligible accounts remain pending until they
use **Set or reset your password**. The household owner must re-enter the
OpenAI key in Settings.

Only after these checks does the installer atomically swap the staged data
generation into `/var/lib/mithra`. A failed application or proxy health check
restores the previous complete generation. No down-migration is run.
