package secrets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
)

var (
	ErrSettingsDenied     = errors.New("provider settings are unavailable")
	ErrSettingsCredential = errors.New("provider credential is invalid")
)

type SettingsStore struct {
	db  *sql.DB
	box *Box
	now func() time.Time
}

func NewSettingsStore(db *sql.DB, masterKey []byte) (*SettingsStore, error) {
	if db == nil {
		return nil, ErrSettingsDenied
	}
	box, err := New(masterKey, Settings)
	if err != nil {
		return nil, ErrSettingsDenied
	}
	return &SettingsStore{db: db, box: box, now: time.Now}, nil
}

func (s *SettingsStore) Configured(ctx context.Context, scope policy.ActorScope) (bool, error) {
	if !scope.Valid() {
		return false, ErrSettingsDenied
	}
	var configured int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM household_openai_settings p JOIN household_members m ON m.household_id=p.household_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE p.household_id=? AND u.status='active' AND h.status='active')`, scope.ActorID, scope.HouseholdID).Scan(&configured)
	if err != nil {
		return false, ErrSettingsDenied
	}
	return configured == 1, nil
}

// ReplaceOpenAI validates before encrypting or opening a transaction, so a
// failed replacement cannot overwrite a working credential.
func (s *SettingsStore) ReplaceOpenAI(ctx context.Context, scope policy.ActorScope, apiKey string, validate func(context.Context, string) error) error {
	if !scope.Valid() || scope.Role != "owner" || validate == nil || strings.TrimSpace(apiKey) != apiKey || len(apiKey) < 16 || len(apiKey) > 1024 {
		return ErrSettingsCredential
	}
	if err := validate(ctx, apiKey); err != nil {
		return ErrSettingsCredential
	}
	ciphertext, err := s.box.Seal([]byte(apiKey), settingContext(scope.HouseholdID))
	if err != nil {
		return ErrSettingsCredential
	}
	digest := sha256.Sum256([]byte(apiKey))
	fingerprint := fmt.Sprintf("%x", digest[:8])
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrSettingsDenied
	}
	defer tx.Rollback()
	if !activeOwner(ctx, tx, scope) {
		return ErrSettingsDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO household_openai_settings(household_id,encrypted_api_key,key_fingerprint,updated_by_user_id,version,created_at,updated_at) VALUES(?,?,?,?,1,?,?) ON CONFLICT(household_id) DO UPDATE SET encrypted_api_key=excluded.encrypted_api_key,key_fingerprint=excluded.key_fingerprint,updated_by_user_id=excluded.updated_by_user_id,version=household_openai_settings.version+1,updated_at=excluded.updated_at`, scope.HouseholdID, ciphertext, fingerprint, scope.ActorID, now, now)
	if err != nil {
		return ErrSettingsDenied
	}
	return tx.Commit()
}

func (s *SettingsStore) RemoveOpenAI(ctx context.Context, scope policy.ActorScope) error {
	if !scope.Valid() || scope.Role != "owner" {
		return ErrSettingsDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrSettingsDenied
	}
	defer tx.Rollback()
	if !activeOwner(ctx, tx, scope) {
		return ErrSettingsDenied
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM household_openai_settings WHERE household_id=?`, scope.HouseholdID); err != nil {
		return ErrSettingsDenied
	}
	return tx.Commit()
}

// OpenAIKey is for the server-side provider dispatcher only. No HTTP view or
// settings response exposes its return value.
func (s *SettingsStore) OpenAIKey(ctx context.Context, scope policy.ActorScope) (string, error) {
	if !scope.Valid() {
		return "", ErrSettingsDenied
	}
	var ciphertext []byte
	err := s.db.QueryRowContext(ctx, `SELECT p.encrypted_api_key FROM household_openai_settings p JOIN household_members m ON m.household_id=p.household_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE p.household_id=? AND u.status='active' AND h.status='active'`, scope.ActorID, scope.HouseholdID).Scan(&ciphertext)
	if err != nil {
		return "", ErrSettingsDenied
	}
	plaintext, err := s.box.Open(ciphertext, settingContext(scope.HouseholdID))
	if err != nil {
		return "", ErrSettingsDenied
	}
	key := string(plaintext)
	clear(plaintext)
	if strings.TrimSpace(key) != key || len(key) < 16 || len(key) > 1024 {
		return "", ErrSettingsDenied
	}
	return key, nil
}

func activeOwner(ctx context.Context, tx *sql.Tx, scope policy.ActorScope) bool {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM households h JOIN household_members m ON m.household_id=h.id AND m.user_id=? JOIN users u ON u.id=m.user_id WHERE h.id=? AND h.owner_user_id=? AND h.status='active' AND m.role='owner' AND u.status='active')`, scope.ActorID, scope.HouseholdID, scope.ActorID).Scan(&exists)
	return err == nil && exists == 1
}

func settingContext(householdID string) []byte {
	return []byte("openai-api-key\x00" + householdID)
}
