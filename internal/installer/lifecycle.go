package installer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/glnarayanan/mithra/internal/auth"
	mithradb "github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/storage"
	"golang.org/x/crypto/hkdf"
)

type backupEntry struct {
	Path, SHA256 string
	Size         int64
}
type backupManifest struct {
	Format, CreatedAt, KeyFingerprint, Generation, MAC string
	Entries                                            []backupEntry
}

type OwnedFile struct {
	Path    string
	Mode    fs.FileMode
	Content []byte
	Remove  bool
}

type StatusReport struct {
	Installed, Database, DataPreserved, BackupTimer, BackupTimerActive, ServiceActive, ServiceHealthy, PDFParserSocket, PDFParserActive, MasterKey, PlunkCredential bool
	Version, Listener, LastBackup, SocketMode                                                                                                                       string
	SocketUID, SocketGID                                                                                                                                            uint32
}

// RestoreOwnership is resolved by the command before it stops Mithra. Tests
// can supply the current uid/gid; a zero value deliberately makes no chown.
type RestoreOwnership struct {
	UID, GID int
	Set      bool
}

// PreparedRestore is an authenticated archive generation retained across the
// service quiesce boundary so activation never reopens caller-controlled bytes.
type PreparedRestore struct{ stage string }

func (p *PreparedRestore) Cleanup() {
	if p != nil && p.stage != "" {
		_ = os.RemoveAll(p.stage)
		p.stage = ""
	}
}

func DatabasePreflight(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return errors.New("SQLite integrity check failed")
	}
	applied, err := mithradb.AppliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	embedded, err := mithradb.EmbeddedMigrations()
	if err != nil || len(applied) > len(embedded) {
		return errors.New("migration history is not the recognized current prefix")
	}
	for index := range applied {
		if applied[index].Version != embedded[index].Version || applied[index].Name != embedded[index].Name || applied[index].Checksum != embedded[index].Checksum {
			return errors.New("migration history checksum mismatch")
		}
	}
	return nil
}

func LatestBackup(directory string) string {
	entries, _ := os.ReadDir(directory)
	latest := ""
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "mithra-") && strings.HasSuffix(entry.Name(), ".mbackup") && entry.Name() > filepath.Base(latest) {
			latest = filepath.Join(directory, entry.Name())
		}
	}
	return latest
}

func VerifyBackupArchive(archive string, masterKey []byte) error {
	stage, err := os.MkdirTemp("", "mithra-backup-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extractArchive(archive, stage, masterKey); err != nil {
		return err
	}
	return verifyExtracted(stage, masterKey)
}

type ReleaseInstall struct {
	ArtifactName, InstallerName, PlunkCredential string
	Artifact, Installer                          []byte
	Manifest, Signature                          []byte
	PublisherKey                                 ed25519.PublicKey
	RequestedVersion, InstalledVersion           string
	Validate                                     func() error
}

// InstallRelease verifies publisher trust before creating or replacing any
// owned path, then commits all staged files behind one rollback boundary.
func InstallRelease(plan Plan, release ReleaseInstall) error {
	if plan.Options.Operation != Install && plan.Options.Operation != Upgrade && plan.Options.Operation != Reconfigure {
		return errors.New("release activation requires an install, upgrade, or reconfigure plan")
	}
	var releaseManifest ReleaseManifest
	if plan.Options.Operation != Reconfigure {
		var err error
		releaseManifest, err = VerifyRelease(release.Manifest, release.Signature, release.PublisherKey, release.ArtifactName, release.Artifact)
		if err != nil {
			return err
		}
		if _, err := VerifyRelease(release.Manifest, release.Signature, release.PublisherKey, release.InstallerName, release.Installer); err != nil {
			return err
		}
		if err := VerifyReleaseVersion(releaseManifest, release.RequestedVersion, release.InstalledVersion); err != nil {
			return err
		}
	}
	paths := OwnedPaths(plan.Options.Root, plan.Proxy)
	owned := ownedRuntime(paths)
	filtered := owned[:0]
	for _, path := range owned {
		if path != "" && !listContainsPath(filtered, path) {
			filtered = append(filtered, path)
		}
	}
	owned = filtered
	sort.Strings(owned)
	manifest, _ := json.Marshal(struct {
		Version int      `json:"version"`
		Paths   []string `json:"paths"`
	}{1, owned})
	files := []OwnedFile{{Path: paths.OwnedManifest, Mode: 0o600, Content: manifest}}
	switch plan.Options.Operation {
	case Install:
		files = append(files,
			OwnedFile{Path: paths.Binary, Mode: 0o755, Content: release.Artifact}, OwnedFile{Path: paths.Installer, Mode: 0o755, Content: release.Installer},
			OwnedFile{Path: paths.Version, Mode: 0o644, Content: []byte(releaseManifest.Version + "\n")},
			OwnedFile{Path: paths.Config, Mode: 0o640, Content: []byte(RuntimeConfig(plan))}, OwnedFile{Path: paths.PlunkKey, Mode: 0o600, Content: []byte(strings.TrimSpace(release.PlunkCredential))},
			OwnedFile{Path: paths.Service, Mode: 0o644, Content: []byte(ServiceUnit())}, OwnedFile{Path: paths.BackupService, Mode: 0o644, Content: []byte(BackupServiceUnit())}, OwnedFile{Path: paths.BackupTimer, Mode: 0o644, Content: []byte(BackupTimerUnit())}, OwnedFile{Path: paths.PDFParserService, Mode: 0o644, Content: []byte(PDFParserServiceUnit())}, OwnedFile{Path: paths.PDFParserSocket, Mode: 0o644, Content: []byte(PDFParserSocketUnit())})
	case Upgrade:
		files = append(files, OwnedFile{Path: paths.Binary, Mode: 0o755, Content: release.Artifact}, OwnedFile{Path: paths.Installer, Mode: 0o755, Content: release.Installer}, OwnedFile{Path: paths.Version, Mode: 0o644, Content: []byte(releaseManifest.Version + "\n")}, OwnedFile{Path: paths.Service, Mode: 0o644, Content: []byte(ServiceUnit())}, OwnedFile{Path: paths.PDFParserService, Mode: 0o644, Content: []byte(PDFParserServiceUnit())}, OwnedFile{Path: paths.PDFParserSocket, Mode: 0o644, Content: []byte(PDFParserSocketUnit())})
	case Reconfigure:
		files = append(files, OwnedFile{Path: paths.Config, Mode: 0o640, Content: []byte(RuntimeConfig(plan))}, OwnedFile{Path: paths.PlunkKey, Mode: 0o600, Content: []byte(strings.TrimSpace(release.PlunkCredential))}, OwnedFile{Path: paths.Service, Mode: 0o644, Content: []byte(ServiceUnit())})
	}
	if paths.Proxy != "" && (plan.Options.Operation == Install || plan.Options.Operation == Reconfigure) {
		proxyConfig := ProxyConfig(plan)
		if proxyConfig == "" {
			return errors.New("proxy configuration domain is invalid")
		}
		files = append(files, OwnedFile{Path: paths.Proxy, Mode: 0o644, Content: []byte(proxyConfig)})
	}
	for _, path := range plan.Retired {
		files = append(files, OwnedFile{Path: path, Remove: true})
	}
	if plan.Options.Operation == Install {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		encoded := base64.RawURLEncoding.EncodeToString(key)
		clear(key)
		files = append(files, OwnedFile{Path: paths.MasterKey, Mode: 0o600, Content: []byte(encoded)})
	}
	if len(plan.Options.AllowedEmails) == 0 || ((plan.Options.Operation == Install || plan.Options.Operation == Reconfigure) && !strings.HasPrefix(strings.TrimSpace(release.PlunkCredential), "sk_")) {
		return errors.New("current allowlist and operation-required Plunk credential are required")
	}
	return ApplyOwnedFiles(plan, files, release.Validate)
}

// ApplyOwnedFiles is the atomic file boundary used after release verification,
// migration rehearsal, and proxy validation have succeeded.
func ApplyOwnedFiles(plan Plan, files []OwnedFile, validate func() error) error {
	allowed := map[string]bool{}
	for _, path := range plan.Mutations {
		allowed[path] = true
	}
	type original struct {
		path   string
		value  []byte
		mode   fs.FileMode
		exists bool
	}
	var originals []original
	hadOriginal := false
	rollback := func() error {
		var failures []error
		for index := len(originals) - 1; index >= 0; index-- {
			old := originals[index]
			if old.exists {
				if err := atomicWrite(old.path, old.value, old.mode, plan.Options.Root); err != nil {
					failures = append(failures, err)
				}
			} else {
				if err := os.Remove(old.path); err != nil && !errors.Is(err, os.ErrNotExist) {
					failures = append(failures, err)
				}
			}
		}
		return errors.Join(failures...)
	}
	for _, file := range files {
		if !allowed[file.Path] || hasArivuSegment(file.Path) || (!file.Remove && file.Mode.Perm()&0o022 != 0) {
			return errors.Join(fmt.Errorf("refusing unmanaged or unsafe file %s", file.Path), rollback())
		}
		entry := original{path: file.Path}
		info, statErr := os.Lstat(file.Path)
		if statErr == nil {
			if !info.Mode().IsRegular() {
				return errors.Join(errors.New("owned file target is not a regular file"), rollback())
			}
			old, readErr := os.ReadFile(file.Path)
			if readErr != nil {
				return errors.Join(readErr, rollback())
			}
			entry.value, entry.mode, entry.exists = old, info.Mode().Perm(), true
			hadOriginal = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(statErr, rollback())
		}
		originals = append(originals, entry)
		var writeErr error
		if file.Remove {
			writeErr = os.Remove(file.Path)
			if errors.Is(writeErr, os.ErrNotExist) {
				writeErr = nil
			}
		} else {
			writeErr = atomicWrite(file.Path, file.Content, file.Mode, plan.Options.Root)
		}
		if writeErr != nil {
			return errors.Join(writeErr, rollback())
		}
	}
	if validate != nil {
		if err := validate(); err != nil {
			rollbackErr := rollback()
			if rollbackErr != nil {
				return fmt.Errorf("activation failed and previous files could not be restored: %w", errors.Join(err, rollbackErr))
			}
			if hadOriginal {
				if recoveryErr := validate(); recoveryErr != nil {
					return fmt.Errorf("activation failed and previous health could not be restored: %w", errors.Join(err, recoveryErr))
				}
			}
			return fmt.Errorf("activation validation failed; previous files restored: %w", err)
		}
	}
	return nil
}

func CreateBackup(ctx context.Context, paths Paths, masterKey []byte, now time.Time) (string, error) {
	if len(masterKey) != 32 || paths.Database == "" || paths.Backups == "" {
		return "", errors.New("backup requires the retained 32-byte master key and owned paths")
	}
	if err := os.MkdirAll(paths.Backups, 0o700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(paths.Backups, ".backup-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := snapshotSQLite(ctx, paths.Database, filepath.Join(stage, "mithra.sqlite3")); err != nil {
		return "", err
	}
	if err := copyTree(paths.Sources, filepath.Join(stage, "sources")); err != nil {
		return "", err
	}
	if err := copyRegular(paths.Journal, filepath.Join(stage, "deletion.journal"), 0o600); err != nil {
		return "", err
	}
	manifest, err := makeBackupManifest(stage, masterKey, now)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), encoded, 0o600); err != nil {
		return "", err
	}
	name := "mithra-" + now.UTC().Format("20060102T150405.000000000Z") + ".mbackup"
	partial := filepath.Join(paths.Backups, "."+name+".partial")
	if err := writeArchive(stage, partial, masterKey); err != nil {
		return "", err
	}
	final := filepath.Join(paths.Backups, name)
	if err := os.Rename(partial, final); err != nil {
		return "", err
	}
	if err := syncDir(paths.Backups); err != nil {
		return "", err
	}
	if err := rotateBackups(paths.Backups, 7); err != nil {
		return "", err
	}
	return final, nil
}

func RehearseMigrations(ctx context.Context, databasePath string) error {
	stage, err := os.MkdirTemp(filepath.Dir(databasePath), ".migration-rehearsal-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	candidate := filepath.Join(stage, "mithra.sqlite3")
	if err := snapshotSQLite(ctx, databasePath, candidate); err != nil {
		return err
	}
	db, err := mithradb.Open(ctx, candidate)
	if err != nil {
		return fmt.Errorf("migration rehearsal failed: %w", err)
	}
	defer db.Close()
	return mithradb.CheckReady(ctx, db)
}

// PreflightRestore authenticates and opens every recovery input before the
// caller quiesces the running service. It only creates a temporary directory.
func PreflightRestore(ctx context.Context, paths Paths, archive string, masterKey []byte, allowedEmails []string, owner RestoreOwnership) error {
	prepared, err := PrepareRestore(ctx, paths, archive, masterKey, allowedEmails, owner)
	if prepared != nil {
		prepared.Cleanup()
	}
	return err
}

// PrepareRestore authenticates and opens every recovery input before the
// caller quiesces the running service. The returned generation is private to
// the installer and must be consumed or cleaned up.
func PrepareRestore(ctx context.Context, paths Paths, archive string, masterKey []byte, allowedEmails []string, owner RestoreOwnership) (*PreparedRestore, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("restore requires the retained key and current non-empty allowlist")
	}
	if err := validateAllowlist(allowedEmails); err != nil {
		return nil, err
	}
	if err := validateRestoreOwnership(paths, owner); err != nil {
		return nil, err
	}
	parent := filepath.Dir(paths.Data)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".mithra-restore-")
	if err != nil {
		return nil, err
	}
	prepared := &PreparedRestore{stage: stage}
	fail := func(err error) (*PreparedRestore, error) {
		prepared.Cleanup()
		return nil, err
	}
	if err := extractArchive(archive, stage, masterKey); err != nil {
		return fail(err)
	}
	if err := verifyExtracted(stage, masterKey); err != nil {
		return fail(err)
	}
	if err := DatabasePreflight(ctx, filepath.Join(stage, "mithra.sqlite3")); err != nil {
		return fail(fmt.Errorf("restored database preflight: %w", err))
	}
	archivedJournal, err := imports.NewDeletionJournal(filepath.Join(stage, "deletion.journal"), masterKey)
	if err != nil {
		return fail(fmt.Errorf("open archived deletion journal: %w", err))
	}
	if _, err := archivedJournal.ReadAll(); err != nil {
		return fail(fmt.Errorf("verify archived deletion journal: %w", err))
	}
	if exists(paths.Journal) {
		journal, err := imports.NewDeletionJournal(paths.Journal, masterKey)
		if err != nil {
			return fail(fmt.Errorf("open current deletion journal: %w", err))
		}
		if _, err := journal.ReadAll(); err != nil {
			return fail(fmt.Errorf("verify current deletion journal: %w", err))
		}
	}
	return prepared, nil
}

func RestoreBackup(ctx context.Context, paths Paths, archive string, masterKey []byte, allowedEmails []string, health func() error) error {
	return RestoreBackupWithOwnership(ctx, paths, archive, masterKey, allowedEmails, RestoreOwnership{}, health)
}

func RestoreBackupWithOwnership(ctx context.Context, paths Paths, archive string, masterKey []byte, allowedEmails []string, owner RestoreOwnership, health func() error) error {
	prepared, err := PrepareRestore(ctx, paths, archive, masterKey, allowedEmails, owner)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()
	return RestorePrepared(ctx, paths, prepared, masterKey, allowedEmails, owner, health)
}

// RestorePrepared activates a generation returned by PrepareRestore without
// rereading its original archive.
func RestorePrepared(ctx context.Context, paths Paths, prepared *PreparedRestore, masterKey []byte, allowedEmails []string, owner RestoreOwnership, health func() error) error {
	if prepared == nil || prepared.stage == "" || len(masterKey) != 32 || len(allowedEmails) == 0 {
		return errors.New("restore requires a prepared authenticated generation and current allowlist")
	}
	if err := validateRestoreOwnership(paths, owner); err != nil {
		return err
	}
	parent := filepath.Dir(paths.Data)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	stage := prepared.stage
	intents, err := mergeDeletionJournals(filepath.Join(stage, "deletion.journal"), paths.Journal, masterKey)
	if err != nil {
		return err
	}
	if err := sanitizeRestore(ctx, stage, intents, allowedEmails, masterKey); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stage, "manifest.json")); err != nil {
		return err
	}
	if err := applyRestoreOwnership(stage, owner); err != nil {
		return err
	}
	previous := paths.Data + ".previous-" + time.Now().UTC().Format("20060102150405.000000000")
	hadPrevious := exists(paths.Data)
	if exists(previous) || exists(paths.Data+".failed-restore") {
		return errors.New("restore recovery collision requires operator inspection")
	}
	if hadPrevious {
		if err := os.Rename(paths.Data, previous); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, paths.Data); err != nil {
		if hadPrevious {
			if rollbackErr := os.Rename(previous, paths.Data); rollbackErr != nil {
				return fmt.Errorf("activate restore: %w; restore previous generation: %v", err, rollbackErr)
			}
		}
		return err
	}
	prepared.stage = ""
	if health != nil {
		if err := health(); err != nil {
			failed := paths.Data + ".failed-restore"
			if renameErr := os.Rename(paths.Data, failed); renameErr != nil {
				return fmt.Errorf("restore health failed and failed generation could not be retained: %w", errors.Join(err, renameErr))
			}
			if hadPrevious {
				if renameErr := os.Rename(previous, paths.Data); renameErr != nil {
					return fmt.Errorf("restore health failed and previous generation could not be reactivated; both generations retained: %w", errors.Join(err, renameErr))
				}
				if recoveryErr := health(); recoveryErr != nil {
					return fmt.Errorf("restore health failed and previous generation health did not recover; failed generation retained: %w", errors.Join(err, recoveryErr))
				}
				if err := os.RemoveAll(failed); err != nil {
					return err
				}
				return fmt.Errorf("restore health failed; previous generation restored: %w", err)
			}
			return fmt.Errorf("restore health failed; failed generation retained for inspection: %w", err)
		}
	}
	if hadPrevious {
		_ = os.RemoveAll(previous)
	}
	return syncDir(parent)
}

// RestoreGeneration is rollback-only: it restores the exact authenticated
// pre-mutation generation without the access-clearing semantics of a user
// requested recovery restore.
func RestoreGeneration(paths Paths, archive string, masterKey []byte, health func() error) error {
	return RestoreGenerationWithOwnership(paths, archive, masterKey, RestoreOwnership{}, health)
}

func RestoreGenerationWithOwnership(paths Paths, archive string, masterKey []byte, owner RestoreOwnership, health func() error) error {
	if len(masterKey) != 32 {
		return errors.New("rollback requires the retained key")
	}
	if err := validateRestoreOwnership(paths, owner); err != nil {
		return err
	}
	parent := filepath.Dir(paths.Data)
	stage, err := os.MkdirTemp(parent, ".mithra-rollback-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extractArchive(archive, stage, masterKey); err != nil {
		return err
	}
	if err := verifyExtracted(stage, masterKey); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stage, "manifest.json")); err != nil {
		return err
	}
	if err := applyRestoreOwnership(stage, owner); err != nil {
		return err
	}
	failed := paths.Data + ".failed-generation"
	if exists(failed) {
		return errors.New("rollback collision requires operator inspection")
	}
	if exists(paths.Data) {
		if err := os.Rename(paths.Data, failed); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, paths.Data); err != nil {
		if exists(failed) {
			if rollbackErr := os.Rename(failed, paths.Data); rollbackErr != nil {
				return fmt.Errorf("activate rollback generation: %w; restore current generation: %v", err, rollbackErr)
			}
		}
		return err
	}
	if health != nil {
		if err := health(); err != nil {
			candidate := paths.Data + ".failed-rollback"
			if exists(candidate) {
				return errors.New("rollback health failed and recovery collision requires operator inspection")
			}
			if renameErr := os.Rename(paths.Data, candidate); renameErr != nil {
				return fmt.Errorf("rollback health failed and candidate could not be retained: %w", errors.Join(err, renameErr))
			}
			if exists(failed) {
				if renameErr := os.Rename(failed, paths.Data); renameErr != nil {
					return fmt.Errorf("rollback health failed and current generation could not be restored; both generations retained: %w", errors.Join(err, renameErr))
				}
				if recoveryErr := health(); recoveryErr != nil {
					return fmt.Errorf("rollback failed and current generation health did not recover: %w", errors.Join(err, recoveryErr))
				}
			}
			return fmt.Errorf("rollback health failed; current generation restored and failed candidate retained: %w", err)
		}
	}
	if exists(failed) {
		if err := os.RemoveAll(failed); err != nil {
			return err
		}
	}
	return syncDir(parent)
}

func validateRestoreOwnership(paths Paths, owner RestoreOwnership) error {
	if !owner.Set {
		return nil
	}
	if owner.UID < 0 || owner.GID < 0 {
		return errors.New("service ownership is invalid")
	}
	parent := filepath.Dir(paths.Data)
	if filepath.Base(paths.Data) != "mithra" || filepath.Clean(paths.Data) != filepath.Join(parent, "mithra") {
		return errors.New("restore data path is not Mithra-owned")
	}
	if info, err := os.Lstat(parent); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("restore data parent is unavailable or unsafe")
	}
	return nil
}

func applyRestoreOwnership(stage string, owner RestoreOwnership) error {
	if !owner.Set {
		return nil
	}
	base := filepath.Base(stage)
	if !strings.HasPrefix(base, ".mithra-restore-") && !strings.HasPrefix(base, ".mithra-rollback-") {
		return errors.New("refusing ownership change outside Mithra restore staging")
	}
	return applyOwnershipTree(stage, owner)
}

// ApplyDataOwnership returns a stopped live generation to the configured
// service identity before the installer restarts Mithra.
func ApplyDataOwnership(paths Paths, owner RestoreOwnership) error {
	if err := validateRestoreOwnership(paths, owner); err != nil {
		return err
	}
	if !owner.Set {
		return nil
	}
	return applyOwnershipTree(paths.Data, owner)
}

func applyOwnershipTree(root string, owner RestoreOwnership) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("restore staging contains an unsafe path")
		}
		mode := fs.FileMode(0o600)
		if info.IsDir() {
			mode = 0o750
		}
		if err := os.Chown(path, owner.UID, owner.GID); err != nil {
			return fmt.Errorf("own restored generation: %w", err)
		}
		return os.Chmod(path, mode)
	})
}

func RemoveRuntime(plan Plan) error {
	if plan.Options.Operation != Uninstall {
		return errors.New("runtime removal requires an uninstall plan")
	}
	for _, path := range plan.Mutations {
		if path == "" {
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-file uninstall target %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func PurgeRecovery(plan Plan) error {
	if plan.Options.Operation != Purge || !plan.Options.ConfirmPurge {
		return errors.New("purge is not confirmed")
	}
	for _, target := range plan.PurgeTarget {
		if target == "" || hasArivuSegment(target) {
			return errors.New("invalid purge target")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return nil
}

func InspectStatus(paths Paths, listener, version string) StatusReport {
	report := StatusReport{Installed: exists(paths.Binary), Database: exists(paths.Database), DataPreserved: exists(paths.Data), BackupTimer: exists(paths.BackupTimer), PDFParserSocket: exists(paths.PDFParserSocket), MasterKey: exists(paths.MasterKey), PlunkCredential: exists(paths.PlunkKey), Listener: listener, Version: version}
	if info, err := os.Lstat(paths.Socket); err == nil && info.Mode()&os.ModeSocket != 0 {
		report.SocketMode = info.Mode().Perm().String()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			report.SocketUID, report.SocketGID = stat.Uid, stat.Gid
		}
	}
	entries, _ := os.ReadDir(paths.Backups)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".mbackup") && entry.Name() > report.LastBackup {
			report.LastBackup = entry.Name()
		}
	}
	return report
}

func makeBackupManifest(stage string, key []byte, now time.Time) (backupManifest, error) {
	entries, err := digestTree(stage)
	if err != nil {
		return backupManifest{}, err
	}
	keyDigest := sha256.Sum256(key)
	var generationInput strings.Builder
	for _, entry := range entries {
		generationInput.WriteString(entry.Path + "\x00" + entry.SHA256 + "\x00")
	}
	generation := sha256.Sum256([]byte(generationInput.String()))
	manifest := backupManifest{Format: "mithra-backup-v1", CreatedAt: now.UTC().Format(time.RFC3339Nano), KeyFingerprint: hex.EncodeToString(keyDigest[:]), Generation: hex.EncodeToString(generation[:]), Entries: entries}
	mac, err := manifestMAC(manifest, key)
	if err != nil {
		return backupManifest{}, err
	}
	manifest.MAC = mac
	return manifest, nil
}

func manifestMAC(manifest backupManifest, key []byte) (string, error) {
	manifest.MAC = ""
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	derived := sha256.Sum256(append(append([]byte(nil), key...), []byte("mithra-backup-manifest-v1")...))
	digest := hmac.New(sha256.New, derived[:])
	_, _ = digest.Write(encoded)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyExtracted(stage string, key []byte) error {
	databaseInfo, databaseErr := os.Lstat(filepath.Join(stage, "mithra.sqlite3"))
	journalInfo, journalErr := os.Lstat(filepath.Join(stage, "deletion.journal"))
	sourcesInfo, sourcesErr := os.Lstat(filepath.Join(stage, "sources"))
	if databaseErr != nil || journalErr != nil || sourcesErr != nil || !databaseInfo.Mode().IsRegular() || !journalInfo.Mode().IsRegular() || !sourcesInfo.IsDir() || sourcesInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup omits required database, source directory, or deletion journal")
	}
	raw, err := os.ReadFile(filepath.Join(stage, "manifest.json"))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("backup manifest is unavailable")
	}
	var manifest backupManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Format != "mithra-backup-v1" {
		return errors.New("backup manifest is invalid")
	}
	keyDigest := sha256.Sum256(key)
	expectedMAC, err := manifestMAC(manifest, key)
	if err != nil || manifest.KeyFingerprint != hex.EncodeToString(keyDigest[:]) || !hmac.Equal([]byte(expectedMAC), []byte(manifest.MAC)) {
		return errors.New("backup key or authentication is invalid")
	}
	current, err := digestTree(stage)
	if err != nil {
		return err
	}
	filtered := current[:0]
	for _, entry := range current {
		if entry.Path != "manifest.json" {
			filtered = append(filtered, entry)
		}
	}
	if !sameEntries(manifest.Entries, filtered) {
		return errors.New("backup content is missing, truncated, or tampered")
	}
	return nil
}

func mergeDeletionJournals(archivedPath, currentPath string, key []byte) ([]imports.DeletionIntent, error) {
	archived, err := imports.NewDeletionJournal(archivedPath, key)
	if err != nil {
		return nil, fmt.Errorf("open archived deletion journal: %w", err)
	}
	intents, err := archived.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("verify archived deletion journal: %w", err)
	}
	byID := make(map[string]imports.DeletionIntent, len(intents))
	for _, intent := range intents {
		byID[intent.ID] = intent
	}
	if exists(currentPath) {
		current, err := imports.NewDeletionJournal(currentPath, key)
		if err != nil {
			return nil, fmt.Errorf("open current deletion journal: %w", err)
		}
		currentIntents, err := current.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("verify current deletion journal: %w", err)
		}
		for _, intent := range currentIntents {
			if prior, ok := byID[intent.ID]; ok && prior != intent {
				return nil, fmt.Errorf("deletion journal intent %s conflicts across generations", intent.ID)
			}
			byID[intent.ID] = intent
		}
	}
	intents = intents[:0]
	for _, intent := range byID {
		intents = append(intents, intent)
	}
	sort.Slice(intents, func(i, j int) bool {
		if intents[i].CreatedAt.Equal(intents[j].CreatedAt) {
			return intents[i].ID < intents[j].ID
		}
		return intents[i].CreatedAt.Before(intents[j].CreatedAt)
	})
	if err := os.Remove(archivedPath); err != nil {
		return nil, err
	}
	merged, err := imports.NewDeletionJournal(archivedPath, key)
	if err != nil {
		return nil, err
	}
	for _, intent := range intents {
		if err := merged.Append(intent); err != nil {
			return nil, err
		}
	}
	return intents, nil
}

func sanitizeRestore(ctx context.Context, stage string, intents []imports.DeletionIntent, allowlist []string, masterKey []byte) error {
	dbPath := filepath.Join(stage, "mithra.sqlite3")
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(dbPath)+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return errors.New("restored SQLite database failed integrity check")
	}
	sources, err := storage.New(db, filepath.Join(stage, "sources"), masterKey)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		if err := imports.ReplayDeletionIntent(ctx, db, sources, intent); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	type sourceScope struct{ householdID, ownerID, sourceID string }
	var incompleteSources []sourceScope
	rows, err := tx.QueryContext(ctx, `SELECT i.household_id,i.owner_user_id,i.source_id FROM document_imports i JOIN sources s ON s.id=i.source_id WHERE i.state IN ('review','awaiting_visual_consent','visual_processing') AND s.state='live'`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for rows.Next() {
		var householdID, ownerID, sourceID string
		if err := rows.Scan(&householdID, &ownerID, &sourceID); err != nil {
			rows.Close()
			tx.Rollback()
			return err
		}
		incompleteSources = append(incompleteSources, sourceScope{householdID, ownerID, sourceID})
	}
	if err := errors.Join(rows.Close(), rows.Err()); err != nil {
		tx.Rollback()
		return err
	}
	var discardedSources []storage.Source
	for _, scope := range incompleteSources {
		source, err := sources.TombstoneInTx(ctx, tx, scope.householdID, scope.ownerID, scope.sourceID)
		if err != nil {
			tx.Rollback()
			return err
		}
		discardedSources = append(discardedSources, source)
	}
	for _, statement := range []string{
		`UPDATE users SET password_hash='',status='pending'`,
		`DELETE FROM household_openai_settings`, `DELETE FROM browser_sessions`, `DELETE FROM password_reset_tokens`, `DELETE FROM invitations`, `DELETE FROM auth_throttles`,
		`DELETE FROM jobs`, `DELETE FROM coaching_cache`, `DELETE FROM coaching_nudges`,
		`UPDATE document_imports SET state='discarded',proposal_json='',consent_token_hash=NULL,consent_expires_at=NULL,deletion_token_hash=NULL,deletion_expires_at=NULL,version=version+1,updated_at=? WHERE state IN ('review','awaiting_visual_consent','visual_processing')`,
		`UPDATE document_imports SET deletion_token_hash=NULL,deletion_expires_at=NULL,updated_at=? WHERE state IN ('committed','superseded','deleted')`,
	} {
		if _, err := tx.ExecContext(ctx, statement, restoreStatementArgs(statement)...); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, source := range discardedSources {
		if err := sources.RemoveCiphertext(source); err != nil {
			return err
		}
	}
	if err := auth.New(db, auth.Config{}).SynchronizeAllowlist(ctx, allowlist); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func restoreStatementArgs(statement string) []any {
	if strings.HasPrefix(statement, "UPDATE document_imports") {
		return []any{time.Now().UTC().Format(time.RFC3339Nano)}
	}
	return nil
}

func snapshotSQLite(ctx context.Context, source, target string) error {
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(source)+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return errors.New("SQLite integrity check failed")
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(FULL)"); err != nil {
		return err
	}
	escaped := strings.ReplaceAll(filepath.ToSlash(target), "'", "''")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return err
	}
	return os.Chmod(target, 0o600)
}

func digestTree(root string) ([]backupEntry, error) {
	var entries []backupEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("backup contains a symbolic link")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		digest := sha256.New()
		size, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		entries = append(entries, backupEntry{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest.Sum(nil)), Size: size})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}

func sameEntries(a, b []backupEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func writeArchive(root, target string, key []byte) error {
	plain, err := os.CreateTemp(filepath.Dir(target), ".mithra-plain-archive-")
	if err != nil {
		return err
	}
	plainPath := plain.Name()
	if err := plain.Chmod(0o600); err != nil {
		plain.Close()
		os.Remove(plainPath)
		return err
	}
	file := plain
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil || (!info.Mode().IsRegular() && !entry.IsDir()) {
			return errors.New("archive source must contain regular files only")
		}
		relative, _ := filepath.Rel(root, path)
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if entry.IsDir() {
			header.Mode = 0o700
		} else {
			header.Mode = 0o600
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	err = errors.Join(err, tarWriter.Close(), gzipWriter.Close(), file.Sync(), file.Close())
	if err != nil {
		_ = os.Remove(plainPath)
		return err
	}
	defer os.Remove(plainPath)
	return encryptArchive(plainPath, target, key)
}

const (
	backupEncryptionMagic = "MITHRAE1"
	backupChunkSize       = 1 << 20
)

func backupCipher(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("backup encryption requires the retained 32-byte master key")
	}
	derived := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, key, nil, []byte("mithra-backup-archive-v1")), derived); err != nil {
		return nil, err
	}
	defer clear(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(prefix []byte, sequence uint64) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint64(nonce[4:], sequence)
	return nonce
}

func chunkAAD(sequence uint64) []byte {
	aad := make([]byte, len(backupEncryptionMagic)+8)
	copy(aad, backupEncryptionMagic)
	binary.BigEndian.PutUint64(aad[len(backupEncryptionMagic):], sequence)
	return aad
}

func encryptArchive(source, target string, key []byte) error {
	aead, err := backupCipher(key)
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = output.Close()
		if !complete {
			_ = os.Remove(target)
		}
	}()
	prefix := make([]byte, 4)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	if _, err := output.Write(append([]byte(backupEncryptionMagic), prefix...)); err != nil {
		return err
	}
	buffer := make([]byte, backupChunkSize)
	for sequence := uint64(0); ; sequence++ {
		count, readErr := io.ReadFull(input, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		if count > 0 {
			sealed := aead.Seal(nil, chunkNonce(prefix, sequence), buffer[:count], chunkAAD(sequence))
			if err := binary.Write(output, binary.BigEndian, uint32(len(sealed))); err != nil {
				return err
			}
			if _, err := output.Write(sealed); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if err := binary.Write(output, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	if err := errors.Join(output.Sync(), output.Close()); err != nil {
		return err
	}
	complete = true
	return syncDir(filepath.Dir(target))
}

func decryptArchive(source, target string, key []byte) error {
	aead, err := backupCipher(key)
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	header := make([]byte, len(backupEncryptionMagic)+4)
	if _, err := io.ReadFull(input, header); err != nil || string(header[:len(backupEncryptionMagic)]) != backupEncryptionMagic {
		return errors.New("backup encryption header is invalid")
	}
	prefix := header[len(backupEncryptionMagic):]
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = output.Close()
		if !complete {
			_ = os.Remove(target)
		}
	}()
	for sequence := uint64(0); ; sequence++ {
		var length uint32
		if err := binary.Read(input, binary.BigEndian, &length); err != nil {
			return errors.New("backup ciphertext is truncated")
		}
		if length == 0 {
			var trailing [1]byte
			if count, err := input.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
				return errors.New("backup ciphertext has trailing content")
			}
			break
		}
		if length < uint32(aead.Overhead()) || length > uint32(backupChunkSize+aead.Overhead()) {
			return errors.New("backup ciphertext chunk is invalid")
		}
		sealed := make([]byte, length)
		if _, err := io.ReadFull(input, sealed); err != nil {
			return errors.New("backup ciphertext is truncated")
		}
		plain, err := aead.Open(nil, chunkNonce(prefix, sequence), sealed, chunkAAD(sequence))
		if err != nil {
			return errors.New("backup ciphertext authentication failed")
		}
		if _, err := output.Write(plain); err != nil {
			clear(plain)
			return err
		}
		clear(plain)
	}
	if err := errors.Join(output.Sync(), output.Close()); err != nil {
		return err
	}
	complete = true
	return nil
}

func extractArchive(archive, target string, key []byte) error {
	plain, err := os.CreateTemp(filepath.Dir(target), ".mithra-decrypted-archive-")
	if err != nil {
		return err
	}
	plainPath := plain.Name()
	if err := plain.Close(); err != nil {
		os.Remove(plainPath)
		return err
	}
	defer os.Remove(plainPath)
	if err := decryptArchive(archive, plainPath, key); err != nil {
		return err
	}
	file, err := os.Open(plainPath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(io.LimitReader(file, 2<<30))
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	count, total := 0, int64(0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		count++
		total += header.Size
		clean := filepath.Clean(header.Name)
		if count > 100000 || total > 1<<30 || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) || clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return errors.New("backup archive contains an unsafe entry")
		}
		path := filepath.Join(target, clean)
		if !strings.HasPrefix(path, filepath.Clean(target)+string(filepath.Separator)) {
			return errors.New("backup archive contains an unsafe entry")
		}
		if header.Typeflag == tar.TypeDir {
			if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(out, reader, header.Size)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
	}
	return nil
}

func copyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source directory is unavailable or unsafe")
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, _ := filepath.Rel(source, path)
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("source directory contains a symbolic link")
		}
		return copyRegular(path, destination, 0o600)
	})
}

func copyRegular(source, target string, mode fs.FileMode) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("backup source is unavailable or unsafe")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Sync(), output.Close())
}

func safeOwnedTarget(path, root string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return errors.New("owned paths must be absolute")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("owned target escapes the configured root")
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("owned target has an unsafe parent %s", current)
		}
	}
	return nil
}

func atomicWrite(path string, content []byte, mode fs.FileMode, root string) error {
	if err := safeOwnedTarget(path, root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := safeOwnedTarget(path, root); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mithra-stage-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode.Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := errors.Join(temporary.Sync(), temporary.Close()); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func rotateBackups(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "mithra-") && strings.HasSuffix(entry.Name(), ".mbackup") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		if err := os.Remove(filepath.Join(directory, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
