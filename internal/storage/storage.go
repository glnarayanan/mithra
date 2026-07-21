// Package storage owns immutable encrypted source files and their SQLite rows.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/secrets"
)

const maxCiphertextBytes = secrets.MaxPlaintextBytes + 128

var (
	ErrInvalidInput = errors.New("invalid source input")
	ErrNotFound     = errors.New("source is unavailable")
	ErrIntegrity    = errors.New("source integrity check failed")
	ErrStorage      = errors.New("source storage failed")
)

type Metadata struct {
	Family       string
	Version      int64
	Visibility   policy.Visibility
	LocatorKind  string
	LocatorValue string
}

type Source struct {
	ID              string
	HouseholdID     string
	OwnerID         string
	Visibility      policy.Visibility
	Family          string
	Version         int64
	State           string
	StorageKey      string
	PlaintextSize   int64
	PlaintextDigest string
	LocatorKind     string
	LocatorValue    string
	CreatedAt       time.Time
}

type hooks struct {
	beforeRename func() error
	afterRename  func() error
	beforeCommit func() error
}

type Service struct {
	db    *sql.DB
	root  string
	box   *secrets.Box
	now   func() time.Time
	hooks hooks
}

func New(db *sql.DB, root string, masterKey []byte) (*Service, error) {
	return newForOwner(db, root, masterKey, os.Geteuid(), os.Getegid())
}

// NewForOwner opens a stopped service's source tree while a root-owned
// maintenance command retains process control.
func NewForOwner(db *sql.DB, root string, masterKey []byte, uid, gid int) (*Service, error) {
	if uid < 0 || gid < 0 {
		return nil, ErrInvalidInput
	}
	return newForOwner(db, root, masterKey, uid, gid)
}

func newForOwner(db *sql.DB, root string, masterKey []byte, uid, gid int) (*Service, error) {
	if db == nil {
		return nil, ErrInvalidInput
	}
	root, err := prepareRoot(root, uid, gid)
	if err != nil {
		return nil, err
	}
	box, err := secrets.New(masterKey, secrets.Sources)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return &Service{db: db, root: root, box: box, now: time.Now}, nil
}

func (s *Service) Store(ctx context.Context, scope policy.ActorScope, plaintext []byte, metadata Metadata) (Source, error) {
	metadata.Visibility = policy.PersonalDefault(metadata.Visibility)
	if !scope.Valid() || len(plaintext) == 0 || len(plaintext) > secrets.MaxPlaintextBytes || !validMetadata(metadata) {
		return Source{}, ErrInvalidInput
	}
	id, err := randomKey()
	if err != nil {
		return Source{}, ErrStorage
	}
	storageKey, err := randomKey()
	if err != nil {
		return Source{}, ErrStorage
	}
	source := Source{
		ID: id, HouseholdID: scope.HouseholdID, OwnerID: scope.ActorID,
		Visibility: metadata.Visibility, Family: metadata.Family, Version: metadata.Version,
		State: "live", StorageKey: storageKey, PlaintextSize: int64(len(plaintext)),
		LocatorKind: metadata.LocatorKind, LocatorValue: metadata.LocatorValue,
		CreatedAt: s.now().UTC(),
	}
	digest := sha256.Sum256(plaintext)
	source.PlaintextDigest = fmt.Sprintf("%x", digest[:])
	ciphertext, err := s.box.Seal(plaintext, sourceContext(source))
	if err != nil {
		return Source{}, ErrStorage
	}
	stagePath := filepath.Join(s.root, ".stage-"+storageKey)
	finalPath := filepath.Join(s.root, storageKey+".enc")
	if err := writeSynced(stagePath, ciphertext); err != nil {
		return Source{}, ErrStorage
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.Remove(stagePath)
		}
	}()
	if s.hooks.beforeRename != nil {
		if err := s.hooks.beforeRename(); err != nil {
			return Source{}, ErrStorage
		}
	}
	if err := os.Rename(stagePath, finalPath); err != nil {
		return Source{}, ErrStorage
	}
	removeStage = false
	if err := syncDirectory(s.root); err != nil {
		return Source{}, ErrStorage
	}
	if s.hooks.afterRename != nil {
		if err := s.hooks.afterRename(); err != nil {
			return Source{}, ErrStorage
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Source{}, ErrStorage
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources(id,household_id,owner_user_id,visibility,family,source_version,state,storage_key,plaintext_size,plaintext_digest,locator_kind,locator_value,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		source.ID, source.HouseholdID, source.OwnerID, source.Visibility, source.Family, source.Version, source.State, source.StorageKey, source.PlaintextSize, source.PlaintextDigest, source.LocatorKind, source.LocatorValue, source.CreatedAt.Format(time.RFC3339Nano), source.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return Source{}, ErrStorage
	}
	if s.hooks.beforeCommit != nil {
		if err := s.hooks.beforeCommit(); err != nil {
			return Source{}, ErrStorage
		}
	}
	if err := tx.Commit(); err != nil {
		return Source{}, ErrStorage
	}
	return source, nil
}

func (s *Service) Read(ctx context.Context, scope policy.ActorScope, sourceID string) ([]byte, Source, error) {
	if !scope.Valid() || strings.TrimSpace(sourceID) == "" {
		return nil, Source{}, ErrNotFound
	}
	source, err := s.lookup(ctx, scope, sourceID)
	if err != nil {
		return nil, Source{}, ErrNotFound
	}
	ciphertext, err := readRegularFile(filepath.Join(s.root, source.StorageKey+".enc"))
	if err != nil {
		return nil, Source{}, ErrIntegrity
	}
	plaintext, err := s.box.Open(ciphertext, sourceContext(source))
	if err != nil {
		return nil, Source{}, ErrIntegrity
	}
	digest := sha256.Sum256(plaintext)
	if int64(len(plaintext)) != source.PlaintextSize || fmt.Sprintf("%x", digest[:]) != source.PlaintextDigest {
		clear(plaintext)
		return nil, Source{}, ErrIntegrity
	}
	return plaintext, source, nil
}

// Info returns authorized source metadata without decrypting its content.
func (s *Service) Info(ctx context.Context, scope policy.ActorScope, sourceID string) (Source, error) {
	if !scope.Valid() || strings.TrimSpace(sourceID) == "" {
		return Source{}, ErrNotFound
	}
	source, err := s.lookup(ctx, scope, sourceID)
	if err != nil {
		return Source{}, ErrNotFound
	}
	return source, nil
}

// Delete makes a source unreadable before removing its encrypted ciphertext.
// Reconcile removes a ciphertext left behind by an interrupted deletion.
func (s *Service) Delete(ctx context.Context, scope policy.ActorScope, sourceID string) error {
	if !scope.Valid() || strings.TrimSpace(sourceID) == "" {
		return ErrNotFound
	}
	source, err := s.lookup(ctx, scope, sourceID)
	if err != nil || source.OwnerID != scope.ActorID {
		return ErrNotFound
	}
	return s.deleteSource(ctx, source)
}

// DeleteExpiredVoice removes only an exact owner/household voice source. It is
// used by the capture janitor after membership may have expired, never by HTTP.
func (s *Service) DeleteExpiredVoice(ctx context.Context, householdID, ownerID, sourceID string) error {
	if strings.TrimSpace(householdID) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(sourceID) == "" {
		return ErrNotFound
	}
	var source Source
	err := s.db.QueryRowContext(ctx, `SELECT id,household_id,owner_user_id,visibility,family,source_version,state,storage_key FROM sources WHERE id=? AND household_id=? AND owner_user_id=? AND family='voice' AND state='live'`, sourceID, householdID, ownerID).Scan(&source.ID, &source.HouseholdID, &source.OwnerID, &source.Visibility, &source.Family, &source.Version, &source.State, &source.StorageKey)
	if err != nil || !validStorageKey(source.StorageKey) {
		return ErrNotFound
	}
	return s.deleteSource(ctx, source)
}

// DeleteOwnedImport removes one exact CSV/XLSX/PDF source for lifecycle
// cleanup after the actor may have lost membership. It is never an HTTP
// authorization primitive; callers must first resolve an owned import row.
func (s *Service) DeleteOwnedImport(ctx context.Context, householdID, ownerID, sourceID string) error {
	if strings.TrimSpace(householdID) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(sourceID) == "" {
		return ErrNotFound
	}
	var source Source
	err := s.db.QueryRowContext(ctx, `SELECT id,household_id,owner_user_id,visibility,family,source_version,state,storage_key FROM sources WHERE id=? AND household_id=? AND owner_user_id=? AND family IN ('csv','xlsx','pdf') AND state='live'`, sourceID, householdID, ownerID).Scan(&source.ID, &source.HouseholdID, &source.OwnerID, &source.Visibility, &source.Family, &source.Version, &source.State, &source.StorageKey)
	if err != nil || !validStorageKey(source.StorageKey) {
		return ErrNotFound
	}
	return s.deleteSource(ctx, source)
}

func (s *Service) deleteSource(ctx context.Context, source Source) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err := s.TombstoneInTx(ctx, tx, source.HouseholdID, source.OwnerID, source.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrStorage
	}
	return s.RemoveCiphertext(source)
}

// TombstoneInTx makes one exact owner source unreadable inside a caller-owned
// transaction. Cross-domain deletion uses it as the final atomic boundary.
func (s *Service) TombstoneInTx(ctx context.Context, tx *sql.Tx, householdID, ownerID, sourceID string) (Source, error) {
	if tx == nil || householdID == "" || ownerID == "" || sourceID == "" {
		return Source{}, ErrNotFound
	}
	var source Source
	var visibility, created string
	err := tx.QueryRowContext(ctx, `SELECT id,household_id,owner_user_id,visibility,family,source_version,state,storage_key,plaintext_size,plaintext_digest,locator_kind,locator_value,created_at FROM sources WHERE id=? AND household_id=? AND owner_user_id=? AND state='live'`, sourceID, householdID, ownerID).Scan(&source.ID, &source.HouseholdID, &source.OwnerID, &visibility, &source.Family, &source.Version, &source.State, &source.StorageKey, &source.PlaintextSize, &source.PlaintextDigest, &source.LocatorKind, &source.LocatorValue, &created)
	if err != nil || !validStorageKey(source.StorageKey) {
		return Source{}, ErrNotFound
	}
	source.Visibility = policy.Visibility(visibility)
	source.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	result, err := tx.ExecContext(ctx, `UPDATE sources SET state='deleted',updated_at=? WHERE id=? AND state='live'`, s.now().UTC().Format(time.RFC3339Nano), source.ID)
	if err != nil {
		return Source{}, ErrStorage
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Source{}, ErrNotFound
	}
	return source, nil
}

// RemoveCiphertext is idempotent physical cleanup after a committed tombstone.
func (s *Service) RemoveCiphertext(source Source) error {
	if !validStorageKey(source.StorageKey) {
		return ErrNotFound
	}
	if err := os.Remove(filepath.Join(s.root, source.StorageKey+".enc")); err != nil && !os.IsNotExist(err) {
		return ErrStorage
	}
	if err := syncDirectory(s.root); err != nil {
		return ErrStorage
	}
	return nil
}

// Reconcile removes incomplete/unreferenced Mithra ciphertext and refuses to
// serve when a live row has no regular ciphertext file.
func (s *Service) Reconcile(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT storage_key FROM sources WHERE state='live'`)
	if err != nil {
		return ErrStorage
	}
	referenced := map[string]struct{}{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil || !validStorageKey(key) {
			rows.Close()
			return ErrIntegrity
		}
		referenced[key] = struct{}{}
	}
	if err := rows.Close(); err != nil || rows.Err() != nil {
		return ErrStorage
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return ErrStorage
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, ".stage-") && validStorageKey(strings.TrimPrefix(name, ".stage-")):
			if err := os.Remove(filepath.Join(s.root, name)); err != nil {
				return ErrStorage
			}
		case strings.HasSuffix(name, ".enc") && validStorageKey(strings.TrimSuffix(name, ".enc")):
			key := strings.TrimSuffix(name, ".enc")
			if _, ok := referenced[key]; !ok {
				if err := os.Remove(filepath.Join(s.root, name)); err != nil {
					return ErrStorage
				}
			}
		}
	}
	for key := range referenced {
		info, err := os.Lstat(filepath.Join(s.root, key+".enc"))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return ErrIntegrity
		}
	}
	if err := syncDirectory(s.root); err != nil {
		return ErrStorage
	}
	return nil
}

func (s *Service) lookup(ctx context.Context, scope policy.ActorScope, sourceID string) (Source, error) {
	var source Source
	var visibility, created string
	err := s.db.QueryRowContext(ctx, `SELECT s.id,s.household_id,s.owner_user_id,s.visibility,s.family,s.source_version,s.state,s.storage_key,s.plaintext_size,s.plaintext_digest,s.locator_kind,s.locator_value,s.created_at FROM sources s JOIN household_members m ON m.household_id=s.household_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE s.id=? AND s.household_id=? AND s.state='live' AND u.status='active' AND h.status='active' AND (s.visibility='shared' OR (s.visibility='personal' AND s.owner_user_id=?))`, scope.ActorID, sourceID, scope.HouseholdID, scope.ActorID).Scan(
		&source.ID, &source.HouseholdID, &source.OwnerID, &visibility, &source.Family, &source.Version, &source.State, &source.StorageKey, &source.PlaintextSize, &source.PlaintextDigest, &source.LocatorKind, &source.LocatorValue, &created)
	if err != nil || !validStorageKey(source.StorageKey) {
		return Source{}, ErrNotFound
	}
	source.Visibility = policy.Visibility(visibility)
	source.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Source{}, ErrIntegrity
	}
	return source, nil
}

func prepareRoot(root string, uid, gid int) (string, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", ErrInvalidInput
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", ErrStorage
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrInvalidInput
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
		return "", ErrInvalidInput
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", ErrStorage
	}
	return root, nil
}

func writeSynced(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readRegularFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() <= 0 || before.Size() > maxCiphertextBytes {
		return nil, ErrIntegrity
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrIntegrity
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, ErrIntegrity
	}
	content, err := io.ReadAll(io.LimitReader(file, maxCiphertextBytes+1))
	if err != nil || len(content) > maxCiphertextBytes {
		return nil, ErrIntegrity
	}
	return content, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validMetadata(metadata Metadata) bool {
	if metadata.Version < 1 || len(metadata.LocatorValue) < 1 || len(metadata.LocatorValue) > 512 {
		return false
	}
	switch metadata.Family {
	case "text", "voice", "csv", "xlsx", "pdf":
	default:
		return false
	}
	switch metadata.LocatorKind {
	case "source", "page", "sheet", "row", "time":
		return true
	default:
		return false
	}
}

func sourceContext(source Source) []byte {
	return []byte(strings.Join([]string{"source-v1", source.ID, source.HouseholdID, source.OwnerID, source.Family, fmt.Sprint(source.Version)}, "\x00"))
}

func randomKey() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validStorageKey(key string) bool {
	if len(key) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(key)
	return err == nil && !strings.ContainsAny(key, "/\\.")
}
