CREATE TABLE health_medicines (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 128),
    name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 128),
    dosage TEXT NOT NULL DEFAULT '' CHECK(length(dosage) <= 128),
    started_on TEXT NOT NULL DEFAULT '' CHECK(started_on = '' OR length(started_on) = 10),
    ended_on TEXT NOT NULL DEFAULT '' CHECK(ended_on = '' OR length(ended_on) = 10),
    note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 512),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE health_notes (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 128),
    recorded_on TEXT NOT NULL CHECK(length(recorded_on) = 10),
    content TEXT NOT NULL CHECK(length(content) BETWEEN 1 AND 4000),
    created_at TEXT NOT NULL
);

CREATE TABLE health_habits (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 128),
    name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 128),
    kind TEXT NOT NULL CHECK(kind IN ('check', 'number')),
    unit TEXT NOT NULL DEFAULT '' CHECK(length(unit) <= 32),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE health_habit_logs (
    habit_id TEXT NOT NULL REFERENCES health_habits(id) ON DELETE CASCADE,
    logged_on TEXT NOT NULL CHECK(length(logged_on) = 10),
    value_text TEXT NOT NULL CHECK(length(value_text) BETWEEN 1 AND 64),
    created_at TEXT NOT NULL,
    PRIMARY KEY (habit_id, logged_on)
);

CREATE INDEX health_medicines_subject ON health_medicines(household_id, subject);
CREATE INDEX health_notes_subject ON health_notes(household_id, subject, recorded_on);
CREATE INDEX health_habits_subject ON health_habits(household_id, subject, active);
