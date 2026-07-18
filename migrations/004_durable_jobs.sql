CREATE TABLE household_revisions (
    household_id TEXT NOT NULL PRIMARY KEY REFERENCES households(id) ON DELETE CASCADE,
    shared_revision INTEGER NOT NULL DEFAULT 0 CHECK(shared_revision >= 0)
);

CREATE TABLE user_revisions (
    user_id TEXT NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    personal_revision INTEGER NOT NULL DEFAULT 0 CHECK(personal_revision >= 0)
);

INSERT INTO household_revisions(household_id)
SELECT id FROM households;

INSERT INTO user_revisions(user_id, household_id)
SELECT user_id, household_id FROM household_members;

CREATE TRIGGER household_revision_create
AFTER INSERT ON households
BEGIN
    INSERT INTO household_revisions(household_id) VALUES(NEW.id);
END;

CREATE TRIGGER user_revision_membership_insert
AFTER INSERT ON household_members
BEGIN
    INSERT INTO user_revisions(user_id, household_id, personal_revision)
    VALUES(NEW.user_id, NEW.household_id, 1)
    ON CONFLICT(user_id) DO UPDATE SET
        household_id = excluded.household_id,
        personal_revision = user_revisions.personal_revision + 1;
    UPDATE household_revisions SET shared_revision = shared_revision + 1
    WHERE household_id = NEW.household_id;
END;

CREATE TRIGGER user_revision_membership_delete
AFTER DELETE ON household_members
BEGIN
    UPDATE user_revisions SET personal_revision = personal_revision + 1
    WHERE user_id = OLD.user_id;
    UPDATE household_revisions SET shared_revision = shared_revision + 1
    WHERE household_id = OLD.household_id;
END;

CREATE TRIGGER revisions_user_status
AFTER UPDATE OF status ON users
WHEN OLD.status <> NEW.status
BEGIN
    UPDATE user_revisions SET personal_revision = personal_revision + 1
    WHERE user_id = NEW.id;
    UPDATE household_revisions SET shared_revision = shared_revision + 1
    WHERE household_id IN (SELECT household_id FROM household_members WHERE user_id = NEW.id);
END;

CREATE TRIGGER revisions_source_insert
AFTER INSERT ON sources
BEGIN
    UPDATE household_revisions SET shared_revision = shared_revision + 1
    WHERE household_id = NEW.household_id AND NEW.visibility = 'shared';
    UPDATE user_revisions SET personal_revision = personal_revision + 1
    WHERE user_id = NEW.owner_user_id AND NEW.visibility = 'personal';
END;

CREATE TRIGGER revisions_source_change
AFTER UPDATE OF visibility, state, updated_at ON sources
WHEN OLD.visibility <> NEW.visibility OR OLD.state <> NEW.state OR OLD.updated_at <> NEW.updated_at
BEGIN
    UPDATE household_revisions SET shared_revision = shared_revision + 1
    WHERE household_id = NEW.household_id;
    UPDATE user_revisions SET personal_revision = personal_revision + 1
    WHERE user_id = NEW.owner_user_id;
END;

CREATE TABLE jobs (
    id TEXT NOT NULL PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN ('extract', 'transcribe', 'capture', 'import', 'finance', 'health', 'planning', 'coaching', 'email')),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT REFERENCES sources(id) ON DELETE CASCADE,
    subject_id TEXT NOT NULL CHECK(length(subject_id) BETWEEN 1 AND 128),
    idempotency_hash TEXT NOT NULL CHECK(length(idempotency_hash) = 64 AND idempotency_hash NOT GLOB '*[^a-f0-9]*'),
    expected_shared_revision INTEGER NOT NULL CHECK(expected_shared_revision >= 0),
    expected_personal_revision INTEGER NOT NULL CHECK(expected_personal_revision >= 0),
    state TEXT NOT NULL CHECK(state IN ('queued', 'leased', 'succeeded', 'failed', 'cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    max_attempts INTEGER NOT NULL CHECK(max_attempts BETWEEN 1 AND 10),
    available_at TEXT NOT NULL,
    leased_until TEXT,
    lease_generation INTEGER NOT NULL DEFAULT 0 CHECK(lease_generation >= 0),
    lease_token_hash TEXT CHECK(lease_token_hash IS NULL OR length(lease_token_hash) = 64),
    last_error_code TEXT CHECK(last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 48),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(household_id, owner_user_id, kind, idempotency_hash)
);

CREATE INDEX jobs_claim ON jobs(state, available_at, leased_until, created_at);

CREATE TRIGGER jobs_scope_insert
BEFORE INSERT ON jobs
WHEN NOT EXISTS (
    SELECT 1
    FROM household_members m
    JOIN users u ON u.id = m.user_id
    JOIN households h ON h.id = m.household_id
    WHERE m.household_id = NEW.household_id
      AND m.user_id = NEW.owner_user_id
      AND u.status = 'active'
      AND h.status = 'active'
) OR (
    NEW.source_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM sources s
        WHERE s.id = NEW.source_id
          AND s.household_id = NEW.household_id
          AND s.state = 'live'
          AND (s.visibility = 'shared' OR (s.visibility = 'personal' AND s.owner_user_id = NEW.owner_user_id))
          AND NOT (s.visibility = 'personal' AND NEW.visibility = 'shared')
    )
)
BEGIN
    SELECT RAISE(ABORT, 'invalid job scope');
END;
