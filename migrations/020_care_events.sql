CREATE TABLE care_events (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    person_id TEXT NOT NULL REFERENCES household_people(id) ON DELETE CASCADE,
    actor_user_id TEXT NOT NULL REFERENCES users(id),
    client_id TEXT NOT NULL CHECK(length(client_id) BETWEEN 1 AND 128),
    source TEXT NOT NULL CHECK(source IN ('web', 'import')),
    type TEXT NOT NULL CHECK(type IN ('feed', 'diaper', 'spitup', 'weight', 'milestone')),
    occurred_at TEXT NOT NULL,
    ended_at TEXT NOT NULL DEFAULT '',
    details_json TEXT NOT NULL CHECK(length(details_json) <= 4000),
    deleted_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX care_events_client ON care_events(household_id, client_id);
CREATE INDEX care_events_person_time ON care_events(person_id, occurred_at DESC);
