CREATE TABLE household_people (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    display_name TEXT NOT NULL CHECK(length(display_name) BETWEEN 1 AND 80),
    relationship TEXT NOT NULL CHECK(relationship IN ('child', 'parent', 'adult', 'other')),
    birth_date TEXT NOT NULL DEFAULT '' CHECK(birth_date = '' OR (length(birth_date) = 10 AND birth_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]')),
    archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX household_people_household ON household_people(household_id, archived, display_name);
