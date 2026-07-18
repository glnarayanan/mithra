CREATE TABLE demo_households (
    household_id TEXT NOT NULL PRIMARY KEY REFERENCES households(id) ON DELETE CASCADE,
    fixture_version TEXT NOT NULL CHECK(length(fixture_version) BETWEEN 1 AND 64),
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    partner_user_id TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    CHECK(owner_user_id <> partner_user_id)
);
