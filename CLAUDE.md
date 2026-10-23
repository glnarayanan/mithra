# Mithra Contributor Notes

Start with [documentation/architecture.md](documentation/architecture.md) and
the applicable plan under `documentation/plans/`.

Mithra is a Go single-binary application with embedded browser assets and
SQLite. Preserve the loopback listener boundary, checksum-locked migrations,
and dependency-free frontend unless a product decision explicitly changes them.

## Design

Read [DESIGN.md](DESIGN.md) before any UI change. Mithra is the blue square in the
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
