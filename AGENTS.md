# Agent Notes

Keep this entrypoint lean. Read [the architecture guide](documentation/architecture.md)
and the relevant implementation plan before changing runtime behaviour.

- The shipped web UI is embedded from `web/` and remains first-party CSS and
  small JavaScript; do not add npm dependencies for it.
- Use `GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension` for Go checks.
- Keep schema changes as numbered, checksum-locked files in `migrations/`.
