package app

import (
	"context"
	"errors"
	"time"

	"github.com/glnarayanan/mithra/internal/installer"
)

// Backup writes one verified, encrypted .mbackup generation into directory
// while the application keeps serving. Ciphertext creation, removal and
// deletion-journal appends wait behind the maintenance gate for the few
// seconds the SQLite snapshot and file copies take.
func (a *App) Backup(ctx context.Context, directory string, now time.Time) (string, error) {
	if a.closed.Load() || a.backupPaths.Database == "" || directory == "" {
		return "", errors.New("backup is unavailable")
	}
	paths := a.backupPaths
	paths.Backups = directory
	a.maintenance.Lock()
	archive, err := installer.CreateBackup(ctx, paths, a.masterKey, now)
	a.maintenance.Unlock()
	if err != nil {
		return "", err
	}
	if err := installer.VerifyBackupArchive(archive, a.masterKey); err != nil {
		return "", err
	}
	return archive, nil
}
