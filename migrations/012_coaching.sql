CREATE TABLE coaching_cache (
    id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^a-f0-9]*'),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT REFERENCES users(id),
    mode TEXT NOT NULL CHECK(mode IN ('brief','week')),
    visibility TEXT NOT NULL CHECK(visibility IN ('shared','personal')),
    content_json TEXT NOT NULL CHECK(length(content_json) BETWEEN 2 AND 262144),
    evidence_json TEXT NOT NULL CHECK(length(evidence_json) BETWEEN 2 AND 131072),
    shared_revision INTEGER NOT NULL CHECK(shared_revision>=0),
    personal_revision INTEGER NOT NULL CHECK(personal_revision>=0),
    source_fingerprint TEXT NOT NULL CHECK(length(source_fingerprint)=64 AND source_fingerprint NOT GLOB '*[^a-f0-9]*'),
    model TEXT NOT NULL CHECK(length(model) BETWEEN 1 AND 64),
    prompt_version TEXT NOT NULL CHECK(length(prompt_version) BETWEEN 1 AND 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    generated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((visibility='shared' AND owner_user_id IS NULL) OR (visibility='personal' AND owner_user_id IS NOT NULL))
);

CREATE UNIQUE INDEX coaching_cache_scope ON coaching_cache(household_id,IFNULL(owner_user_id,''),mode,visibility);

CREATE TABLE coaching_nudges (
    id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^a-f0-9]*'),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    record_family TEXT NOT NULL CHECK(record_family IN ('finance','health','planning')),
    record_id TEXT NOT NULL,
    source_id TEXT NOT NULL REFERENCES sources(id),
    state TEXT NOT NULL CHECK(state IN ('awaiting-update','acknowledged','stale','completed')),
    follow_up_enabled INTEGER NOT NULL DEFAULT 0 CHECK(follow_up_enabled IN (0,1)),
    initial_email_sent_at TEXT,
    follow_up_email_sent_at TEXT,
    acknowledged_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX coaching_nudge_once ON coaching_nudges(household_id,owner_user_id,record_family,record_id);

CREATE TRIGGER coaching_cache_membership_delete AFTER DELETE ON household_members
BEGIN
    DELETE FROM coaching_cache WHERE household_id=OLD.household_id AND (owner_user_id=OLD.user_id OR visibility='shared');
    UPDATE coaching_nudges SET state='stale',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE household_id=OLD.household_id AND owner_user_id=OLD.user_id AND state='awaiting-update';
END;

CREATE TRIGGER coaching_cache_user_disable AFTER UPDATE OF status ON users WHEN NEW.status<>'active'
BEGIN
    DELETE FROM coaching_cache WHERE owner_user_id=NEW.id OR household_id IN (SELECT household_id FROM household_members WHERE user_id=NEW.id);
    UPDATE coaching_nudges SET state='stale',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE owner_user_id=NEW.id AND state='awaiting-update';
END;

CREATE TRIGGER coaching_cache_source_privacy AFTER UPDATE OF visibility,state ON sources
WHEN OLD.visibility<>NEW.visibility OR OLD.state<>NEW.state
BEGIN
    DELETE FROM coaching_cache WHERE household_id=NEW.household_id;
    UPDATE coaching_nudges SET state='stale',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE source_id=NEW.id AND state='awaiting-update';
END;
