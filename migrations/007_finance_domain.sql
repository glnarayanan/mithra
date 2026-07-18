CREATE TABLE finance_income (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    category TEXT NOT NULL CHECK(length(category) BETWEEN 1 AND 128),
    received_on TEXT NOT NULL,
    amount_coefficient INTEGER,
    amount_scale INTEGER,
    amount_original TEXT NOT NULL CHECK(length(amount_original) <= 128),
    incomplete_reason TEXT NOT NULL DEFAULT '' CHECK(length(incomplete_reason) <= 256),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES finance_income(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((amount_coefficient IS NULL AND amount_scale IS NULL AND incomplete_reason <> '') OR (amount_coefficient IS NOT NULL AND amount_scale BETWEEN 0 AND 6)),
    UNIQUE(id, household_id, owner_user_id, visibility)
);

CREATE TABLE finance_spending (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    category TEXT NOT NULL CHECK(length(category) BETWEEN 1 AND 128),
    spent_on TEXT NOT NULL,
    amount_coefficient INTEGER,
    amount_scale INTEGER,
    amount_original TEXT NOT NULL CHECK(length(amount_original) <= 128),
    incomplete_reason TEXT NOT NULL DEFAULT '' CHECK(length(incomplete_reason) <= 256),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES finance_spending(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((amount_coefficient IS NULL AND amount_scale IS NULL AND incomplete_reason <> '') OR (amount_coefficient IS NOT NULL AND amount_scale BETWEEN 0 AND 6)),
    UNIQUE(id, household_id, owner_user_id, visibility)
);

CREATE TABLE finance_assets (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    category TEXT NOT NULL CHECK(length(category) BETWEEN 1 AND 128),
    observed_on TEXT NOT NULL,
    amount_coefficient INTEGER,
    amount_scale INTEGER,
    amount_original TEXT NOT NULL CHECK(length(amount_original) <= 128),
    incomplete_reason TEXT NOT NULL DEFAULT '' CHECK(length(incomplete_reason) <= 256),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES finance_assets(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((amount_coefficient IS NULL AND amount_scale IS NULL AND incomplete_reason <> '') OR (amount_coefficient IS NOT NULL AND amount_scale BETWEEN 0 AND 6)),
    UNIQUE(id, household_id, owner_user_id, visibility)
);

CREATE TABLE finance_liabilities (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    category TEXT NOT NULL CHECK(length(category) BETWEEN 1 AND 128),
    observed_on TEXT NOT NULL,
    amount_coefficient INTEGER,
    amount_scale INTEGER,
    amount_original TEXT NOT NULL CHECK(length(amount_original) <= 128),
    incomplete_reason TEXT NOT NULL DEFAULT '' CHECK(length(incomplete_reason) <= 256),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES finance_liabilities(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((amount_coefficient IS NULL AND amount_scale IS NULL AND incomplete_reason <> '') OR (amount_coefficient IS NOT NULL AND amount_scale BETWEEN 0 AND 6)),
    UNIQUE(id, household_id, owner_user_id, visibility)
);

CREATE TABLE finance_budgets (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    category TEXT NOT NULL CHECK(length(category) BETWEEN 1 AND 128),
    starts_on TEXT NOT NULL,
    ends_on TEXT NOT NULL,
    amount_coefficient INTEGER,
    amount_scale INTEGER,
    amount_original TEXT NOT NULL CHECK(length(amount_original) <= 128),
    incomplete_reason TEXT NOT NULL DEFAULT '' CHECK(length(incomplete_reason) <= 256),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES finance_budgets(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((amount_coefficient IS NULL AND amount_scale IS NULL AND incomplete_reason <> '') OR (amount_coefficient IS NOT NULL AND amount_scale BETWEEN 0 AND 6)),
    UNIQUE(id, household_id, owner_user_id, visibility)
);

CREATE TABLE finance_obligations (
    id TEXT NOT NULL PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal', 'shared')),
    source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    source_family TEXT NOT NULL CHECK(source_family IN ('text', 'voice', 'csv', 'xlsx', 'pdf')),
    source_version INTEGER NOT NULL CHECK(source_version > 0),
    label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 256),
    category TEXT NOT NULL CHECK(length(category) BETWEEN 1 AND 128),
    due_on TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending', 'paid', 'cancelled')),
    amount_coefficient INTEGER,
    amount_scale INTEGER,
    amount_original TEXT NOT NULL CHECK(length(amount_original) <= 128),
    incomplete_reason TEXT NOT NULL DEFAULT '' CHECK(length(incomplete_reason) <= 256),
    generated_by TEXT NOT NULL CHECK(generated_by IN ('application', 'ai', 'user')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128),
    prompt_version TEXT NOT NULL DEFAULT '' CHECK(length(prompt_version) <= 64),
    schema_version TEXT NOT NULL CHECK(length(schema_version) BETWEEN 1 AND 64),
    data_revision INTEGER NOT NULL CHECK(data_revision >= 0),
    supersedes_id TEXT REFERENCES finance_obligations(id),
    active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK((amount_coefficient IS NULL AND amount_scale IS NULL AND incomplete_reason <> '') OR (amount_coefficient IS NOT NULL AND amount_scale BETWEEN 0 AND 6)),
    UNIQUE(id, household_id, owner_user_id, visibility)
);

CREATE INDEX finance_income_scope ON finance_income(household_id, visibility, owner_user_id, active, received_on);
CREATE INDEX finance_spending_scope ON finance_spending(household_id, visibility, owner_user_id, active, spent_on);
CREATE INDEX finance_assets_scope ON finance_assets(household_id, visibility, owner_user_id, active, observed_on);
CREATE INDEX finance_liabilities_scope ON finance_liabilities(household_id, visibility, owner_user_id, active, observed_on);
CREATE INDEX finance_budgets_scope ON finance_budgets(household_id, visibility, owner_user_id, active, starts_on);
CREATE INDEX finance_obligations_scope ON finance_obligations(household_id, visibility, owner_user_id, active, due_on, status);

CREATE TRIGGER finance_income_scope_insert BEFORE INSERT ON finance_income WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid finance scope'); END;
CREATE TRIGGER finance_spending_scope_insert BEFORE INSERT ON finance_spending WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid finance scope'); END;
CREATE TRIGGER finance_assets_scope_insert BEFORE INSERT ON finance_assets WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid finance scope'); END;
CREATE TRIGGER finance_liabilities_scope_insert BEFORE INSERT ON finance_liabilities WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid finance scope'); END;
CREATE TRIGGER finance_budgets_scope_insert BEFORE INSERT ON finance_budgets WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid finance scope'); END;
CREATE TRIGGER finance_obligations_scope_insert BEFORE INSERT ON finance_obligations WHEN NOT EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active') OR NOT EXISTS (SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.family=NEW.source_family AND s.source_version=NEW.source_version AND s.state='live' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=NEW.owner_user_id AND NEW.visibility='personal'))) BEGIN SELECT RAISE(ABORT, 'invalid finance scope'); END;

CREATE TRIGGER finance_income_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON finance_income BEGIN SELECT RAISE(ABORT, 'finance scope is immutable'); END;
CREATE TRIGGER finance_spending_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON finance_spending BEGIN SELECT RAISE(ABORT, 'finance scope is immutable'); END;
CREATE TRIGGER finance_assets_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON finance_assets BEGIN SELECT RAISE(ABORT, 'finance scope is immutable'); END;
CREATE TRIGGER finance_liabilities_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON finance_liabilities BEGIN SELECT RAISE(ABORT, 'finance scope is immutable'); END;
CREATE TRIGGER finance_budgets_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON finance_budgets BEGIN SELECT RAISE(ABORT, 'finance scope is immutable'); END;
CREATE TRIGGER finance_obligations_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,source_family,source_version,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,created_at ON finance_obligations BEGIN SELECT RAISE(ABORT, 'finance scope is immutable'); END;

CREATE TRIGGER finance_income_revision AFTER INSERT ON finance_income BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_spending_revision AFTER INSERT ON finance_spending BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_assets_revision AFTER INSERT ON finance_assets BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_liabilities_revision AFTER INSERT ON finance_liabilities BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_budgets_revision AFTER INSERT ON finance_budgets BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_obligations_revision AFTER INSERT ON finance_obligations BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;

CREATE TRIGGER finance_income_change_revision AFTER UPDATE OF active, version ON finance_income WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_spending_change_revision AFTER UPDATE OF active, version ON finance_spending WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_assets_change_revision AFTER UPDATE OF active, version ON finance_assets WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_liabilities_change_revision AFTER UPDATE OF active, version ON finance_liabilities WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_budgets_change_revision AFTER UPDATE OF active, version ON finance_budgets WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;
CREATE TRIGGER finance_obligations_change_revision AFTER UPDATE OF active, version ON finance_obligations WHEN OLD.active<>NEW.active OR OLD.version<>NEW.version BEGIN UPDATE household_revisions SET shared_revision=shared_revision+1 WHERE household_id=NEW.household_id AND NEW.visibility='shared'; UPDATE user_revisions SET personal_revision=personal_revision+1 WHERE user_id=NEW.owner_user_id AND NEW.visibility='personal'; END;

CREATE TRIGGER finance_income_search_delete AFTER DELETE ON finance_income BEGIN DELETE FROM search_entries WHERE record_family='finance' AND record_id=OLD.id; END;
CREATE TRIGGER finance_spending_search_delete AFTER DELETE ON finance_spending BEGIN DELETE FROM search_entries WHERE record_family='finance' AND record_id=OLD.id; END;
CREATE TRIGGER finance_assets_search_delete AFTER DELETE ON finance_assets BEGIN DELETE FROM search_entries WHERE record_family='finance' AND record_id=OLD.id; END;
CREATE TRIGGER finance_liabilities_search_delete AFTER DELETE ON finance_liabilities BEGIN DELETE FROM search_entries WHERE record_family='finance' AND record_id=OLD.id; END;
CREATE TRIGGER finance_budgets_search_delete AFTER DELETE ON finance_budgets BEGIN DELETE FROM search_entries WHERE record_family='finance' AND record_id=OLD.id; END;
CREATE TRIGGER finance_obligations_search_delete AFTER DELETE ON finance_obligations BEGIN DELETE FROM search_entries WHERE record_family='finance' AND record_id=OLD.id; END;
