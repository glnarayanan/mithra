CREATE TABLE coaching_history (
    id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^a-f0-9]*'),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT REFERENCES users(id),
    mode TEXT NOT NULL CHECK(mode IN ('brief','week')),
    visibility TEXT NOT NULL CHECK(visibility IN ('shared','personal')),
    content_json TEXT NOT NULL CHECK(length(content_json) BETWEEN 2 AND 262144),
    evidence_json TEXT NOT NULL CHECK(length(evidence_json) BETWEEN 2 AND 131072),
    model TEXT NOT NULL CHECK(length(model) BETWEEN 1 AND 64),
    prompt_version TEXT NOT NULL CHECK(length(prompt_version) BETWEEN 1 AND 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    generated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK((visibility='shared' AND owner_user_id IS NULL) OR (visibility='personal' AND owner_user_id IS NOT NULL))
);

CREATE INDEX coaching_history_scope ON coaching_history(household_id,IFNULL(owner_user_id,''),mode,visibility,generated_at DESC);

CREATE TRIGGER coaching_history_membership_delete AFTER DELETE ON household_members
BEGIN
    DELETE FROM coaching_history WHERE household_id=OLD.household_id AND (owner_user_id=OLD.user_id OR visibility='shared');
END;

CREATE TRIGGER coaching_history_user_disable AFTER UPDATE OF status ON users WHEN NEW.status<>'active'
BEGIN
    DELETE FROM coaching_history WHERE owner_user_id=NEW.id OR household_id IN (SELECT household_id FROM household_members WHERE user_id=NEW.id);
END;

CREATE TRIGGER coaching_history_source_privacy AFTER UPDATE OF visibility,state ON sources
WHEN OLD.visibility<>NEW.visibility OR OLD.state<>NEW.state
BEGIN
    DELETE FROM coaching_history WHERE household_id=NEW.household_id;
END;
