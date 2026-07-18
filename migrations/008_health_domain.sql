CREATE TABLE health_observations (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 128),
    analyte TEXT NOT NULL CHECK(length(analyte) BETWEEN 1 AND 128),
    specimen TEXT NOT NULL DEFAULT '' CHECK(length(specimen) <= 128),
    method TEXT NOT NULL DEFAULT '' CHECK(length(method) <= 128),
    reference_context TEXT NOT NULL DEFAULT '' CHECK(length(reference_context) <= 256),
    comparability_key TEXT NOT NULL CHECK(length(comparability_key) BETWEEN 1 AND 1024),
    observed_on TEXT NOT NULL,
    value_coefficient INTEGER NOT NULL,
    value_scale INTEGER NOT NULL CHECK(value_scale BETWEEN 0 AND 6),
    value_original TEXT NOT NULL CHECK(length(value_original) BETWEEN 1 AND 128),
    unit TEXT NOT NULL CHECK(length(unit) BETWEEN 1 AND 64),
    reference_low_coefficient INTEGER,
    reference_low_scale INTEGER CHECK(reference_low_scale BETWEEN 0 AND 6),
    reference_high_coefficient INTEGER,
    reference_high_scale INTEGER CHECK(reference_high_scale BETWEEN 0 AND 6),
    reference_unit TEXT NOT NULL DEFAULT '' CHECK(length(reference_unit) <= 64),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES health_observations(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((reference_low_coefficient IS NULL AND reference_low_scale IS NULL) OR (reference_low_coefficient IS NOT NULL AND reference_low_scale IS NOT NULL)),
    CHECK((reference_high_coefficient IS NULL AND reference_high_scale IS NULL) OR (reference_high_coefficient IS NOT NULL AND reference_high_scale IS NOT NULL))
);

CREATE TABLE health_appointments (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 128),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    provider TEXT NOT NULL DEFAULT '' CHECK(length(provider) <= 128),
    location TEXT NOT NULL DEFAULT '' CHECK(length(location) <= 256),
    scheduled_on TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('planned', 'completed', 'cancelled')),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES health_appointments(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE health_care_routines (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    subject TEXT NOT NULL CHECK(length(subject) BETWEEN 1 AND 128),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    cadence TEXT NOT NULL CHECK(length(cadence) BETWEEN 1 AND 128),
    next_due_on TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active', 'paused', 'completed')),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES health_care_routines(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX health_observations_scope ON health_observations(household_id, visibility, owner_user_id, active, observed_on);
CREATE INDEX health_observations_series ON health_observations(household_id, visibility, owner_user_id, active, comparability_key, observed_on);
CREATE INDEX health_appointments_scope ON health_appointments(household_id, visibility, owner_user_id, active, scheduled_on, status);
CREATE INDEX health_care_routines_scope ON health_care_routines(household_id, visibility, owner_user_id, active, next_due_on, status);

CREATE TRIGGER health_observations_scope_insert BEFORE INSERT ON health_observations WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid health scope'); END;
CREATE TRIGGER health_appointments_scope_insert BEFORE INSERT ON health_appointments WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid health scope'); END;
CREATE TRIGGER health_care_routines_scope_insert BEFORE INSERT ON health_care_routines WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid health scope'); END;

CREATE TRIGGER health_observations_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON health_observations BEGIN SELECT RAISE(ABORT, 'health scope is immutable'); END;
CREATE TRIGGER health_appointments_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON health_appointments BEGIN SELECT RAISE(ABORT, 'health scope is immutable'); END;
CREATE TRIGGER health_care_routines_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON health_care_routines BEGIN SELECT RAISE(ABORT, 'health scope is immutable'); END;

CREATE TRIGGER health_observations_revision AFTER INSERT ON health_observations BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER health_appointments_revision AFTER INSERT ON health_appointments BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER health_care_routines_revision AFTER INSERT ON health_care_routines BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;

CREATE TRIGGER health_observations_change_revision AFTER UPDATE OF active,version ON health_observations WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER health_appointments_change_revision AFTER UPDATE OF active,version ON health_appointments WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER health_care_routines_change_revision AFTER UPDATE OF active,version ON health_care_routines WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;

CREATE TRIGGER health_observations_search_delete AFTER DELETE ON health_observations BEGIN DELETE FROM search_entries WHERE record_family='health' AND record_id=OLD.id; END;
CREATE TRIGGER health_appointments_search_delete AFTER DELETE ON health_appointments BEGIN DELETE FROM search_entries WHERE record_family='health' AND record_id=OLD.id; END;
CREATE TRIGGER health_care_routines_search_delete AFTER DELETE ON health_care_routines BEGIN DELETE FROM search_entries WHERE record_family='health' AND record_id=OLD.id; END;
