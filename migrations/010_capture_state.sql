CREATE TABLE captures (
    id TEXT NOT NULL PRIMARY KEY CHECK(length(id) = 32 AND id NOT GLOB '*[^a-f0-9]*'),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal','shared')),
    source_id TEXT REFERENCES sources(id) ON DELETE SET NULL,
    raw_audio_source_id TEXT REFERENCES sources(id) ON DELETE SET NULL,
    source_kind TEXT NOT NULL CHECK(source_kind IN ('text','transcript','audio')),
    summary TEXT NOT NULL DEFAULT '' CHECK(length(summary) <= 512),
    state TEXT NOT NULL CHECK(state IN ('processing','awaiting_confirmation','clarification','confirmed','undone','rejected','cancelled')),
    clarification_field TEXT NOT NULL DEFAULT '' CHECK(clarification_field IN ('','owner','date','unit','status')),
    clarification_question TEXT NOT NULL DEFAULT '' CHECK(length(clarification_question) <= 256),
    proposal_json TEXT NOT NULL DEFAULT '' CHECK(length(proposal_json) <= 8192),
    record_family TEXT NOT NULL DEFAULT '' CHECK(record_family IN ('','finance','health','planning')),
    record_table TEXT NOT NULL DEFAULT '',
    record_id TEXT NOT NULL DEFAULT '',
    record_version INTEGER NOT NULL DEFAULT 0 CHECK(record_version >= 0),
    undo_revision INTEGER NOT NULL DEFAULT 0 CHECK(undo_revision >= 0),
    undo_until TEXT,
    audio_state TEXT NOT NULL CHECK(audio_state IN ('none','retryable','terminal','cancelled','cleaned')),
    audio_attempts INTEGER NOT NULL DEFAULT 0 CHECK(audio_attempts BETWEEN 0 AND 3),
    cleanup_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((state='clarification' AND clarification_field<>'' AND clarification_question<>'' AND record_id='') OR state<>'clarification'),
    CHECK((record_id='' AND record_family='' AND record_table='' AND record_version=0) OR (record_id<>'' AND record_family<>'' AND record_table<>'' AND record_version>0)),
    CHECK((audio_state='none' AND raw_audio_source_id IS NULL AND cleanup_at IS NULL) OR audio_state<>'none')
);

CREATE INDEX captures_cleanup ON captures(audio_state, cleanup_at);
CREATE INDEX captures_scope ON captures(household_id, visibility, owner_user_id, created_at);

CREATE TRIGGER captures_scope_insert BEFORE INSERT ON captures
WHEN NOT EXISTS (
    SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id
    WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active'
) OR (NEW.source_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.state='live'
      AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))
)) OR (NEW.raw_audio_source_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sources s WHERE s.id=NEW.raw_audio_source_id AND s.household_id=NEW.household_id AND s.family='voice' AND s.state='live'
      AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))
))
BEGIN SELECT RAISE(ABORT,'invalid capture scope'); END;

CREATE TRIGGER captures_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,created_at ON captures
BEGIN SELECT RAISE(ABORT,'capture scope is immutable'); END;

CREATE TRIGGER captures_source_scope_update BEFORE UPDATE OF source_id,raw_audio_source_id ON captures
WHEN (NEW.source_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.state='live'
      AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))
)) OR (NEW.raw_audio_source_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sources s WHERE s.id=NEW.raw_audio_source_id AND s.household_id=NEW.household_id AND s.family='voice' AND s.state='live'
      AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))
))
BEGIN SELECT RAISE(ABORT,'invalid capture source scope'); END;
