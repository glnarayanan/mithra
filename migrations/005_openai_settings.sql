CREATE TABLE household_openai_settings (
    household_id TEXT NOT NULL PRIMARY KEY REFERENCES households(id) ON DELETE CASCADE,
    encrypted_api_key BLOB NOT NULL CHECK(length(encrypted_api_key) > 32),
    key_fingerprint TEXT NOT NULL CHECK(length(key_fingerprint) = 16 AND key_fingerprint NOT GLOB '*[^a-f0-9]*'),
    updated_by_user_id TEXT NOT NULL REFERENCES users(id),
    version INTEGER NOT NULL CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TRIGGER openai_settings_owner_insert
BEFORE INSERT ON household_openai_settings
WHEN NOT EXISTS (
    SELECT 1
    FROM households h
    JOIN household_members m ON m.household_id = h.id AND m.user_id = NEW.updated_by_user_id
    JOIN users u ON u.id = m.user_id
    WHERE h.id = NEW.household_id
      AND h.status = 'active'
      AND h.owner_user_id = NEW.updated_by_user_id
      AND m.role = 'owner'
      AND u.status = 'active'
)
BEGIN
    SELECT RAISE(ABORT, 'only the active owner may store provider settings');
END;

CREATE TRIGGER openai_settings_owner_update
BEFORE UPDATE ON household_openai_settings
WHEN NOT EXISTS (
    SELECT 1
    FROM households h
    JOIN household_members m ON m.household_id = h.id AND m.user_id = NEW.updated_by_user_id
    JOIN users u ON u.id = m.user_id
    WHERE h.id = NEW.household_id
      AND h.status = 'active'
      AND h.owner_user_id = NEW.updated_by_user_id
      AND m.role = 'owner'
      AND u.status = 'active'
)
BEGIN
    SELECT RAISE(ABORT, 'only the active owner may store provider settings');
END;
