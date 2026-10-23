# Agent Notes

Keep this entrypoint lean. Read [the architecture guide](documentation/architecture.md)
and the relevant implementation plan before changing runtime behaviour.

- The shipped web UI is embedded from `web/` and remains first-party CSS and
  small JavaScript; do not add npm dependencies for it.
- Use `GOFLAGS=-tags=sqlite_fts5,sqlite_omit_load_extension` for Go checks.
- Keep schema changes as numbered, checksum-locked files in `migrations/`.

## Design

Read `DESIGN.md` before any UI change when you have it (it is kept local
and git-ignored, like `PRODUCT.md`); the rules below are the summary. Mithra is the blue square in the
MithraHQ shared system (derived from the Lexington Vanta theme), shared with
mithrahq.com, Mithra, HealthTrends and TinyTots.

- Paper shell: a light top bar with underlined text tabs on desktop, and big
  rounded colour panels that carry the key numbers. Lists are hairline lists.
- Sentence case. No eyebrow labels above headings, no uppercase or monospace
  costume, no side-stripe borders, no gradient text or glass.
- Bricolage Grotesque for display, Inter for text, both self-hosted. No CDNs.
- Keep each app's shape and colour: Mithra blue square, HealthTrends coral
  arch, TinyTots mustard circle.
- Mithra has no night theme yet. Copy states facts from records and never gives advice.
- Check changed screens at phone and desktop widths, in every theme the app
  ships, before committing.
