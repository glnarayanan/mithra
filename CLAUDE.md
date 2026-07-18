# Mithra Contributor Notes

Start with [documentation/architecture.md](documentation/architecture.md) and
the applicable plan under `documentation/plans/`.

Mithra is a Go single-binary application with embedded browser assets and
SQLite. Preserve the loopback listener boundary, checksum-locked migrations,
and dependency-free frontend unless a product decision explicitly changes them.
