CREATE TABLE sources (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    family TEXT NOT NULL CHECK(family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    state TEXT NOT NULL CHECK(state IN ('live', 'deleted')),
    storage_key TEXT NOT NULL UNIQUE CHECK(length(storage_key) = 43 AND storage_key NOT GLOB '*[^A-Za-z0-9_-]*'),
    plaintext_size INTEGER NOT NULL CHECK(plaintext_size > 0 AND plaintext_size <= 16777216),
    plaintext_digest TEXT NOT NULL CHECK(length(plaintext_digest) = 64 AND plaintext_digest NOT GLOB '*[^a-f0-9]*'),
    locator_kind TEXT NOT NULL CHECK(locator_kind IN ('source', 'page', 'sheet', 'row', 'time')),
    locator_value TEXT NOT NULL CHECK(length(locator_value) BETWEEN 1 AND 512),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(id, household_id, owner_user_id, visibility, family, source_version)
);

CREATE INDEX sources_scope ON sources(household_id, visibility, owner_user_id, state);

CREATE TRIGGER sources_scope_insert
BEFORE INSERT ON sources
WHEN NOT EXISTS (
    SELECT 1
    FROM household_members m
    JOIN users u ON u.id = m.user_id
    JOIN households h ON h.id = m.household_id
    WHERE m.household_id = NEW.household_id
      AND m.user_id = NEW.owner_user_id
      AND u.status = 'active'
      AND h.status = 'active'
)
BEGIN
    SELECT RAISE(ABORT, 'invalid source scope');
END;

CREATE TRIGGER sources_scope_update
BEFORE UPDATE OF household_id, owner_user_id, visibility ON sources
WHEN NOT EXISTS (
    SELECT 1
    FROM household_members m
    JOIN users u ON u.id = m.user_id
    JOIN households h ON h.id = m.household_id
    WHERE m.household_id = NEW.household_id
      AND m.user_id = NEW.owner_user_id
      AND u.status = 'active'
      AND h.status = 'active'
)
BEGIN
    SELECT RAISE(ABORT, 'invalid source scope');
END;

CREATE TRIGGER sources_identity_immutable
BEFORE UPDATE OF id, household_id, owner_user_id, family, source_version, storage_key, plaintext_size, plaintext_digest, locator_kind, locator_value, created_at ON sources
BEGIN
    SELECT RAISE(ABORT, 'source identity is immutable');
END;
