CREATE TABLE evidence_links (
    id TEXT NOT NULL PRIMARY KEY,
    record_family TEXT NOT NULL CHECK(record_family IN ('finance', 'health', 'planning', 'coaching')),
    record_id TEXT NOT NULL CHECK(length(record_id) BETWEEN 1 AND 128),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    locator_kind TEXT NOT NULL CHECK(locator_kind IN ('source', 'page', 'sheet', 'row', 'time')),
    locator_value TEXT NOT NULL CHECK(length(locator_value) BETWEEN 1 AND 512),
    created_at TEXT NOT NULL,
    UNIQUE(record_family, record_id, source_id, locator_kind, locator_value)
);

CREATE INDEX evidence_links_record ON evidence_links(household_id, record_family, record_id);

CREATE TRIGGER evidence_links_scope_insert
BEFORE INSERT ON evidence_links
WHEN NOT EXISTS (
    SELECT 1 FROM household_members m
    JOIN users u ON u.id = m.user_id
    JOIN households h ON h.id = m.household_id
    WHERE m.household_id = NEW.household_id AND m.user_id = NEW.owner_user_id
      AND u.status = 'active' AND h.status = 'active'
) OR NOT EXISTS (
    SELECT 1 FROM sources s
    WHERE s.id = NEW.source_id AND s.household_id = NEW.household_id
      AND s.family = NEW.source_family AND s.source_version = NEW.source_version
      AND s.state = 'live'
      AND (s.visibility = 'shared' OR (s.visibility = 'personal' AND s.owner_user_id = NEW.owner_user_id))
      AND NOT (s.visibility = 'personal' AND NEW.visibility = 'shared')
)
BEGIN
    SELECT RAISE(ABORT, 'invalid evidence scope');
END;

CREATE TRIGGER evidence_links_scope_immutable
BEFORE UPDATE OF record_family, record_id, household_id, owner_user_id, visibility, source_id, source_family, source_version, created_at ON evidence_links
BEGIN
    SELECT RAISE(ABORT, 'evidence scope is immutable');
END;

CREATE TABLE search_entries (
    rowid INTEGER PRIMARY KEY,
    record_family TEXT NOT NULL CHECK(record_family IN ('finance', 'health', 'planning', 'coaching')),
    record_id TEXT NOT NULL CHECK(length(record_id) BETWEEN 1 AND 128),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    content TEXT NOT NULL CHECK(length(content) BETWEEN 1 AND 32768),
    UNIQUE(record_family, record_id)
);

CREATE TRIGGER search_entries_scope_insert
BEFORE INSERT ON search_entries
WHEN NOT EXISTS (
    SELECT 1 FROM household_members m
    JOIN users u ON u.id = m.user_id
    JOIN households h ON h.id = m.household_id
    WHERE m.household_id = NEW.household_id AND m.user_id = NEW.owner_user_id
      AND u.status = 'active' AND h.status = 'active'
)
BEGIN
    SELECT RAISE(ABORT, 'invalid search scope');
END;

CREATE TRIGGER search_entries_scope_immutable
BEFORE UPDATE OF rowid, record_family, record_id, household_id, owner_user_id, visibility ON search_entries
BEGIN
    SELECT RAISE(ABORT, 'search scope is immutable');
END;

CREATE VIRTUAL TABLE search_entries_fts USING fts5(
    content,
    content='search_entries',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER search_entries_fts_insert
AFTER INSERT ON search_entries
BEGIN
    INSERT INTO search_entries_fts(rowid, content) VALUES(NEW.rowid, NEW.content);
END;

CREATE TRIGGER search_entries_fts_delete
AFTER DELETE ON search_entries
BEGIN
    INSERT INTO search_entries_fts(search_entries_fts, rowid, content) VALUES('delete', OLD.rowid, OLD.content);
END;

CREATE TRIGGER search_entries_fts_update
AFTER UPDATE OF content ON search_entries
BEGIN
    INSERT INTO search_entries_fts(search_entries_fts, rowid, content) VALUES('delete', OLD.rowid, OLD.content);
    INSERT INTO search_entries_fts(rowid, content) VALUES(NEW.rowid, NEW.content);
END;
