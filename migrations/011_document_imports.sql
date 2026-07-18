CREATE TABLE document_imports (
    id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^a-f0-9]*'),
    household_id TEXT NOT NULL REFERENCES households(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    visibility TEXT NOT NULL CHECK(visibility IN ('personal','shared')),
    source_id TEXT NOT NULL UNIQUE REFERENCES sources(id),
    file_name TEXT NOT NULL CHECK(length(file_name) BETWEEN 1 AND 255),
    document_kind TEXT NOT NULL CHECK(document_kind IN ('csv','xlsx','pdf')),
    source_digest TEXT NOT NULL CHECK(length(source_digest)=64 AND source_digest NOT GLOB '*[^a-f0-9]*'),
    state TEXT NOT NULL CHECK(state IN ('review','awaiting_visual_consent','visual_processing','committed','superseded','discarded','deleted')),
    proposal_json TEXT NOT NULL DEFAULT '' CHECK(length(proposal_json) <= 524288),
    expected_shared_revision INTEGER NOT NULL CHECK(expected_shared_revision >= 0),
    expected_personal_revision INTEGER NOT NULL CHECK(expected_personal_revision >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    consent_token_hash TEXT CHECK(consent_token_hash IS NULL OR (length(consent_token_hash)=64 AND consent_token_hash NOT GLOB '*[^a-f0-9]*')),
    consent_expires_at TEXT,
    deletion_token_hash TEXT CHECK(deletion_token_hash IS NULL OR (length(deletion_token_hash)=64 AND deletion_token_hash NOT GLOB '*[^a-f0-9]*')),
    deletion_expires_at TEXT,
    supersedes_import_id TEXT REFERENCES document_imports(id),
    committed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
    ,CHECK((state='awaiting_visual_consent' AND consent_token_hash IS NOT NULL AND consent_expires_at IS NOT NULL) OR state<>'awaiting_visual_consent')
);

CREATE INDEX document_imports_scope ON document_imports(household_id,owner_user_id,visibility,state,created_at);
CREATE UNIQUE INDEX document_imports_exact_live ON document_imports(household_id,owner_user_id,visibility,source_digest) WHERE state IN ('review','awaiting_visual_consent','committed','superseded');
CREATE UNIQUE INDEX document_imports_one_successor ON document_imports(supersedes_import_id) WHERE supersedes_import_id IS NOT NULL AND state NOT IN ('discarded','deleted');

CREATE TRIGGER document_imports_scope_insert BEFORE INSERT ON document_imports
WHEN NOT EXISTS (
    SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id
    WHERE m.household_id=NEW.household_id AND m.user_id=NEW.owner_user_id AND u.status='active' AND h.status='active'
) OR NOT EXISTS (
    SELECT 1 FROM sources s WHERE s.id=NEW.source_id AND s.household_id=NEW.household_id AND s.owner_user_id=NEW.owner_user_id
      AND s.visibility=NEW.visibility AND s.family=NEW.document_kind AND s.plaintext_digest=NEW.source_digest AND s.state='live'
)
BEGIN SELECT RAISE(ABORT,'invalid import scope'); END;

CREATE TRIGGER document_imports_scope_immutable BEFORE UPDATE OF id,household_id,owner_user_id,visibility,source_id,document_kind,source_digest,supersedes_import_id,created_at ON document_imports
BEGIN SELECT RAISE(ABORT,'import scope is immutable'); END;
