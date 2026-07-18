package imports

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

type DeletionImpact struct {
	ImportID, FileName, SourceID, Token string
	Visibility                          policy.Visibility
	Records, Jobs                       int
	ExpiresAt                           time.Time
}

var ErrCleanupPending = errors.New("source is tombstoned; ciphertext cleanup is pending")

func (s *Service) PrepareDeletion(ctx context.Context, actor policy.ActorScope, importID string) (DeletionImpact, error) {
	if s == nil || s.journal == nil || !actor.Valid() {
		return DeletionImpact{}, policy.ErrUnauthorized
	}
	var impact DeletionImpact
	var visibility string
	err := s.db.QueryRowContext(ctx, `SELECT i.id,i.file_name,i.source_id,i.visibility FROM document_imports i JOIN household_members m ON m.household_id=i.household_id AND m.user_id=i.owner_user_id JOIN users u ON u.id=m.user_id AND u.status='active' JOIN households h ON h.id=m.household_id AND h.status='active' WHERE i.id=? AND i.household_id=? AND i.owner_user_id=? AND i.state IN ('committed','superseded')`, importID, actor.HouseholdID, actor.ActorID).Scan(&impact.ImportID, &impact.FileName, &impact.SourceID, &visibility)
	if err != nil {
		return DeletionImpact{}, policy.ErrUnauthorized
	}
	impact.Visibility = policy.Visibility(visibility)
	for _, table := range deletionTables {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE source_id=? AND active=1", impact.SourceID).Scan(&count); err != nil {
			return DeletionImpact{}, err
		}
		impact.Records += count
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE source_id=? AND state IN ('queued','leased')`, impact.SourceID).Scan(&impact.Jobs)
	token, hash, err := consentToken()
	if err != nil {
		return DeletionImpact{}, err
	}
	impact.Token = token
	impact.ExpiresAt = s.now().UTC().Add(10 * time.Minute)
	result, err := s.db.ExecContext(ctx, `UPDATE document_imports SET deletion_token_hash=?,deletion_expires_at=?,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state IN ('committed','superseded')`, hash, impact.ExpiresAt.Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), importID, actor.HouseholdID, actor.ActorID)
	if err != nil {
		return DeletionImpact{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return DeletionImpact{}, ErrStale
	}
	return impact, nil
}

func (s *Service) ConfirmDeletion(ctx context.Context, actor policy.ActorScope, importID, token string) error {
	if s == nil || s.journal == nil || !actor.Valid() {
		return policy.ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	var intent DeletionIntent
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT lower(hex(randomblob(16))),i.household_id,i.owner_user_id,i.source_id,i.source_digest,? FROM document_imports i JOIN household_members m ON m.household_id=i.household_id AND m.user_id=i.owner_user_id JOIN users u ON u.id=m.user_id AND u.status='active' JOIN households h ON h.id=m.household_id AND h.status='active' WHERE i.id=? AND i.household_id=? AND i.owner_user_id=? AND i.state IN ('committed','superseded') AND i.deletion_token_hash=? AND i.deletion_expires_at>?`, s.now().UTC().Format(time.RFC3339Nano), importID, actor.HouseholdID, actor.ActorID, hex.EncodeToString(hash[:]), s.now().UTC().Format(time.RFC3339Nano)).Scan(&intent.ID, &intent.HouseholdID, &intent.OwnerID, &intent.SourceID, &intent.Digest, &expires)
	if err != nil {
		return ErrStale
	}
	intent.CreatedAt, _ = time.Parse(time.RFC3339Nano, expires)
	if err := s.journal.Append(intent); err != nil {
		return err
	}
	return s.applyDeletionIntent(ctx, intent)
}

func (s *Service) ReconcileDeletions(ctx context.Context) error {
	if s == nil || s.journal == nil {
		return ErrInvalidInput
	}
	intents, err := s.journal.ReadAll()
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if err := s.applyDeletionIntent(ctx, intent); err != nil && !errors.Is(err, storage.ErrNotFound) && !errors.Is(err, ErrCleanupPending) {
			return err
		}
	}
	return nil
}

var deletionTables = []string{"finance_income", "finance_spending", "finance_assets", "finance_liabilities", "finance_budgets", "finance_obligations", "health_observations", "health_appointments", "health_care_routines", "planning_goals", "planning_plans", "planning_milestones", "planning_events"}

func deactivateSourceRecords(ctx context.Context, tx *sql.Tx, sourceID string, now time.Time) error {
	stamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_entries WHERE (record_family,record_id) IN (SELECT record_family,record_id FROM evidence_links WHERE source_id=?)`, sourceID); err != nil {
		return err
	}
	for _, table := range deletionTables {
		if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET active=0,version=version+1,updated_at=? WHERE source_id=? AND active=1", stamp, sourceID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyDeletionIntent(ctx context.Context, intent DeletionIntent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, digest string
	err = tx.QueryRowContext(ctx, `SELECT state,plaintext_digest FROM sources WHERE id=? AND household_id=? AND owner_user_id=?`, intent.SourceID, intent.HouseholdID, intent.OwnerID).Scan(&state, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if digest != intent.Digest {
		return ErrIntegrityIntent
	}
	if state == "deleted" {
		_ = tx.Rollback()
		return s.removeDeletedCiphertext(ctx, intent)
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if err = deactivateSourceRecords(ctx, tx, intent.SourceID, s.now().UTC()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM evidence_links WHERE source_id=?`, intent.SourceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',lease_token_hash=NULL,leased_until=NULL,lease_generation=lease_generation+1,updated_at=? WHERE source_id=? AND state IN ('queued','leased')`, stamp, intent.SourceID); err != nil {
		return err
	}
	source, err := s.sources.TombstoneInTx(ctx, tx, intent.HouseholdID, intent.OwnerID, intent.SourceID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE document_imports SET state='deleted',proposal_json='',deletion_token_hash=NULL,deletion_expires_at=NULL,version=version+1,updated_at=? WHERE source_id=?`, stamp, intent.SourceID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.sources.RemoveCiphertext(source); err != nil {
		return ErrCleanupPending
	}
	return nil
}

func (s *Service) removeDeletedCiphertext(ctx context.Context, intent DeletionIntent) error {
	var source storage.Source
	var visibility, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,household_id,owner_user_id,visibility,family,source_version,state,storage_key,plaintext_size,plaintext_digest,locator_kind,locator_value,created_at FROM sources WHERE id=? AND household_id=? AND owner_user_id=? AND state='deleted'`, intent.SourceID, intent.HouseholdID, intent.OwnerID).Scan(&source.ID, &source.HouseholdID, &source.OwnerID, &visibility, &source.Family, &source.Version, &source.State, &source.StorageKey, &source.PlaintextSize, &source.PlaintextDigest, &source.LocatorKind, &source.LocatorValue, &created)
	if err != nil {
		return storage.ErrNotFound
	}
	source.Visibility = policy.Visibility(visibility)
	source.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err := s.sources.RemoveCiphertext(source); err != nil {
		return ErrCleanupPending
	}
	return nil
}

var ErrIntegrityIntent = errors.New("deletion intent does not match source")
