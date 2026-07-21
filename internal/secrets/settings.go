package secrets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/providers"
)

var (
	ErrSettingsDenied     = errors.New("provider settings are unavailable")
	ErrSettingsCredential = errors.New("provider settings are invalid")
)

type ProviderConfig struct{ ProviderID, Model, BaseURL, APIKey string }
type ProviderDetails struct{ ProviderID, Model, BaseURL string }
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

// ProviderDetails returns only values that Settings and page capability checks
// may use. It never decrypts the saved key.
func (s *SettingsStore) ProviderDetails(ctx context.Context, scope policy.ActorScope) (ProviderDetails, error) {
	if !scope.Valid() {
		return ProviderDetails{}, ErrSettingsDenied
	}
	var stored ProviderDetails
	err := s.db.QueryRowContext(ctx, `SELECT p.provider_id,p.provider_model,p.provider_base_url FROM household_openai_settings p JOIN household_members m ON m.household_id=p.household_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE p.household_id=? AND u.status='active' AND h.status='active'`, scope.ActorID, scope.HouseholdID).Scan(&stored.ProviderID, &stored.Model, &stored.BaseURL)
	if err != nil {
		return ProviderDetails{}, ErrSettingsDenied
	}
	provider, ok := providers.ModelProviderByID(stored.ProviderID)
	if !ok {
		return ProviderDetails{}, ErrSettingsDenied
	}
	key := "configured"
	if provider.KeyOptional {
		key = ""
	}
	normalized, _, err := providers.NormalizeModelConfig(providers.ModelConfig{ProviderID: stored.ProviderID, Model: stored.Model, BaseURL: stored.BaseURL, APIKey: key})
	if err != nil {
		return ProviderDetails{}, ErrSettingsDenied
	}
	return ProviderDetails{ProviderID: normalized.ProviderID, Model: normalized.Model, BaseURL: normalized.BaseURL}, nil
}

// ProviderConfig returns a decrypted key to server code only. HTTP views never
// receive this value.
func (s *SettingsStore) ProviderConfig(ctx context.Context, scope policy.ActorScope) (ProviderConfig, error) {
	if !scope.Valid() {
		return ProviderConfig{}, ErrSettingsDenied
	}
	var stored ProviderConfig
	var ciphertext []byte
	err := s.db.QueryRowContext(ctx, `SELECT p.provider_id,p.provider_model,p.provider_base_url,p.encrypted_api_key FROM household_openai_settings p JOIN household_members m ON m.household_id=p.household_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE p.household_id=? AND u.status='active' AND h.status='active'`, scope.ActorID, scope.HouseholdID).Scan(&stored.ProviderID, &stored.Model, &stored.BaseURL, &ciphertext)
	if err != nil {
		return ProviderConfig{}, ErrSettingsDenied
	}
	plaintext, err := s.box.Open(ciphertext, settingContext(scope.HouseholdID))
	if err != nil {
		return ProviderConfig{}, ErrSettingsDenied
	}
	if len(plaintext) == 1 && plaintext[0] == 0 {
		stored.APIKey = ""
	} else {
		stored.APIKey = string(plaintext)
	}
	clear(plaintext)
	normalized, _, err := providers.NormalizeModelConfig(providers.ModelConfig{ProviderID: stored.ProviderID, Model: stored.Model, BaseURL: stored.BaseURL, APIKey: stored.APIKey})
	if err != nil {
		return ProviderConfig{}, ErrSettingsDenied
	}
	return ProviderConfig{ProviderID: normalized.ProviderID, Model: normalized.Model, BaseURL: normalized.BaseURL, APIKey: normalized.APIKey}, nil
}

// ReplaceProvider validates the final complete configuration before opening a
// transaction. A failed change leaves the working connection untouched.
func (s *SettingsStore) ReplaceProvider(ctx context.Context, scope policy.ActorScope, candidate ProviderConfig, validate func(context.Context, ProviderConfig) error) error {
	if !scope.Valid() || scope.Role != "owner" || validate == nil {
		return ErrSettingsCredential
	}
	prior, priorErr := s.ProviderConfig(ctx, scope)
	if candidate.APIKey == "" && priorErr == nil && strings.EqualFold(strings.TrimSpace(candidate.ProviderID), prior.ProviderID) {
		candidate.APIKey = prior.APIKey
	}
	normalized, _, err := providers.NormalizeModelConfig(providers.ModelConfig{ProviderID: candidate.ProviderID, Model: candidate.Model, BaseURL: candidate.BaseURL, APIKey: candidate.APIKey})
	if err != nil {
		return ErrSettingsCredential
	}
	final := ProviderConfig{ProviderID: normalized.ProviderID, Model: normalized.Model, BaseURL: normalized.BaseURL, APIKey: normalized.APIKey}
	if err := validate(ctx, final); err != nil {
		return ErrSettingsCredential
	}
	plain := []byte(final.APIKey)
	if len(plain) == 0 {
		plain = []byte{0}
	}
	ciphertext, err := s.box.Seal(plain, settingContext(scope.HouseholdID))
	clear(plain)
	if err != nil {
		return ErrSettingsCredential
	}
	fingerprint := envelopeFingerprint(ciphertext)
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrSettingsDenied
	}
	defer tx.Rollback()
	if !activeOwner(ctx, tx, scope) {
		return ErrSettingsDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO household_openai_settings(household_id,encrypted_api_key,key_fingerprint,provider_id,provider_model,provider_base_url,updated_by_user_id,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,?,?) ON CONFLICT(household_id) DO UPDATE SET encrypted_api_key=excluded.encrypted_api_key,key_fingerprint=excluded.key_fingerprint,provider_id=excluded.provider_id,provider_model=excluded.provider_model,provider_base_url=excluded.provider_base_url,updated_by_user_id=excluded.updated_by_user_id,version=household_openai_settings.version+1,updated_at=excluded.updated_at`, scope.HouseholdID, ciphertext, fingerprint, final.ProviderID, final.Model, final.BaseURL, scope.ActorID, now, now)
	if err != nil {
		return ErrSettingsDenied
	}
	return tx.Commit()
}

func envelopeFingerprint(ciphertext []byte) string {
	digest := sha256.Sum256(ciphertext)
	return hex.EncodeToString(digest[:8])
}

func (s *SettingsStore) RemoveProvider(ctx context.Context, scope policy.ActorScope) error {
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

// Legacy methods keep the prior OpenAI-only server calls compatible.
func (s *SettingsStore) ReplaceOpenAI(ctx context.Context, scope policy.ActorScope, key string, validate func(context.Context, string) error) error {
	return s.ReplaceProvider(ctx, scope, ProviderConfig{ProviderID: providers.ProviderOpenAI, APIKey: key}, func(ctx context.Context, config ProviderConfig) error { return validate(ctx, config.APIKey) })
}
func (s *SettingsStore) RemoveOpenAI(ctx context.Context, scope policy.ActorScope) error {
	return s.RemoveProvider(ctx, scope)
}
func (s *SettingsStore) OpenAIKey(ctx context.Context, scope policy.ActorScope) (string, error) {
	config, err := s.ProviderConfig(ctx, scope)
	if err != nil || config.ProviderID != providers.ProviderOpenAI {
		return "", ErrSettingsDenied
	}
	return config.APIKey, nil
}

func activeOwner(ctx context.Context, tx *sql.Tx, scope policy.ActorScope) bool {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM households h JOIN household_members m ON m.household_id=h.id AND m.user_id=? JOIN users u ON u.id=m.user_id WHERE h.id=? AND h.owner_user_id=? AND h.status='active' AND m.role='owner' AND u.status='active')`, scope.ActorID, scope.HouseholdID, scope.ActorID).Scan(&exists)
	return err == nil && exists == 1
}

func settingContext(householdID string) []byte {
	return []byte("openai-api-key\x00" + strings.TrimSpace(householdID))
}
