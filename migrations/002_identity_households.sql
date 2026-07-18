CREATE TABLE users (
    id TEXT NOT NULL PRIMARY KEY,
    email TEXT NOT NULL COLLATE NOCASE UNIQUE,
    status TEXT NOT NULL CHECK(status IN ('pending', 'active', 'disabled')),
    password_hash TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    disabled_at TEXT
);

CREATE TABLE households (
    id TEXT NOT NULL PRIMARY KEY,
    status TEXT NOT NULL CHECK(status IN ('active', 'closed')),
    owner_user_id TEXT REFERENCES users(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE household_members (
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL UNIQUE REFERENCES users(id),
    role TEXT NOT NULL CHECK(role IN ('owner', 'adult')),
    created_at TEXT NOT NULL,
    PRIMARY KEY(household_id, user_id)
);

CREATE UNIQUE INDEX household_single_owner ON household_members(household_id) WHERE role = 'owner';

CREATE TRIGGER household_member_limit_insert
BEFORE INSERT ON household_members
WHEN (SELECT COUNT(*) FROM household_members WHERE household_id = NEW.household_id) >= 2
BEGIN
    SELECT RAISE(ABORT, 'household adult limit reached');
END;

CREATE TRIGGER household_member_limit_update
BEFORE UPDATE OF household_id ON household_members
WHEN (SELECT COUNT(*) FROM household_members WHERE household_id = NEW.household_id AND user_id <> OLD.user_id) >= 2
BEGIN
    SELECT RAISE(ABORT, 'household adult limit reached');
END;

CREATE TABLE password_reset_tokens (
    token_hash TEXT NOT NULL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX password_reset_tokens_user ON password_reset_tokens(user_id);

CREATE TABLE browser_sessions (
    token_hash TEXT NOT NULL PRIMARY KEY,
    csrf_hash TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    created_at TEXT NOT NULL,
    rotated_from_hash TEXT
);
CREATE INDEX browser_sessions_user ON browser_sessions(user_id);

CREATE TABLE invitations (
    token_hash TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    inviter_user_id TEXT NOT NULL REFERENCES users(id),
    invited_email TEXT NOT NULL COLLATE NOCASE,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX invitations_email ON invitations(invited_email);
CREATE INDEX invitations_household ON invitations(household_id);

CREATE TABLE auth_throttles (
    throttle_key TEXT NOT NULL PRIMARY KEY,
    window_started_at TEXT NOT NULL,
    attempts INTEGER NOT NULL CHECK(attempts >= 0),
    updated_at TEXT NOT NULL
);
