package database_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/glnarayanan/mithra/internal/database"
	_ "github.com/mattn/go-sqlite3"
)

func TestOpenForOwnerUsesConfiguredServiceIdentity(t *testing.T) {
	directory := t.TempDir()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("platform does not expose file ownership")
	}
	path := filepath.Join(directory, "mithra.sqlite3")
	db, err := database.OpenForOwner(context.Background(), path, int(stat.Uid), int(stat.Gid))
	if err != nil {
		t.Fatalf("open for configured owner: %v", err)
	}
	_ = db.Close()
	if _, err := database.OpenForOwner(context.Background(), path, int(stat.Uid)+1, int(stat.Gid)); err == nil {
		t.Fatal("open accepted a directory owned by a different identity")
	}
}

func TestOpenAppliesEmbeddedMigrationsAndReopens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mithra.sqlite3")

	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("open clean database: %v", err)
	}

	got, err := database.AppliedMigrations(ctx, db)
	if err != nil {
		t.Fatalf("read applied migrations: %v", err)
	}
	want, err := database.EmbeddedMigrations()
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("applied migrations = %d, want %d", len(got), len(want))
	}
	for index, migration := range want {
		if got[index].Version != migration.Version || got[index].Checksum != migration.Checksum {
			t.Fatalf("migration %d = %#v, want version %d checksum %s", index, got[index], migration.Version, migration.Checksum)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer reopened.Close()

	if err := database.CheckReady(ctx, reopened); err != nil {
		t.Fatalf("reopened database is not ready: %v", err)
	}
}

func TestApplicationMetadataRequiresNonNullKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO application_metadata(key, value, updated_at)
		VALUES(?, ?, ?)
	`, nil, "value", "2026-07-18T00:00:00Z"); err == nil {
		t.Fatal("application metadata accepted a NULL key")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO application_metadata(key, value, updated_at)
		VALUES(?, ?, ?)
	`, "household_name", "Mithra", "2026-07-18T00:00:00Z"); err != nil {
		t.Fatalf("application metadata rejected a normal key: %v", err)
	}
}

func TestOpenRestrictsDatabaseAndSidecarPermissions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "mithra.sqlite3")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed permissive database file: %v", err)
	}

	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE TABLE permission_probe (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("write WAL permission probe: %v", err)
	}

	sidecars := 0
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) && candidate != path {
				continue
			}
			t.Fatalf("stat SQLite file %q: %v", candidate, err)
		}
		if candidate != path {
			sidecars++
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Errorf("SQLite file %q permissions = %04o, want 0600", filepath.Base(candidate), permissions)
		}
	}
	if sidecars == 0 {
		t.Fatal("WAL mode created no sidecar files to verify")
	}
}

func TestOpenRejectsUnsafeParentAndDatabasePaths(t *testing.T) {
	t.Parallel()

	t.Run("group or world writable parent", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o777); err != nil {
			t.Fatalf("make database directory permissive: %v", err)
		}
		path := filepath.Join(directory, "mithra.sqlite3")

		_, err := database.Open(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "group or world writable") {
			t.Fatalf("unsafe parent error = %v, want writable-parent rejection", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("unsafe parent created database: %v", statErr)
		}
	})

	t.Run("symlink database", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.sqlite3")
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatalf("create symlink target: %v", err)
		}
		path := filepath.Join(directory, "mithra.sqlite3")
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("create database symlink: %v", err)
		}

		_, err := database.Open(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("symlink database error = %v, want regular-file rejection", err)
		}
		info, statErr := os.Stat(target)
		if statErr != nil {
			t.Fatalf("stat symlink target: %v", statErr)
		}
		if permissions := info.Mode().Perm(); permissions != 0o644 {
			t.Fatalf("symlink target permissions = %04o, want unchanged 0644", permissions)
		}
	})

	t.Run("directory database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mithra.sqlite3")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create directory database path: %v", err)
		}

		_, err := database.Open(context.Background(), path)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("directory database error = %v, want regular-file rejection", err)
		}
	})
}

func TestOpenRejectsFileURIWithoutCreatingItsTarget(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	_, err := database.Open(context.Background(), "file:"+path+"?mode=ro")
	if err == nil || !strings.Contains(err.Error(), "URI") {
		t.Fatalf("SQLite URI error = %v, want URI rejection", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("rejected URI created target: %v", statErr)
	}
}

// Relative paths like MITHRA_DB=.local/mithra.sqlite3 must open for local/dev.
// net/url file:// construction used to treat the first segment as URI authority.
func TestOpenRelativeDotLocalPath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	ctx := context.Background()
	path := filepath.Join(".local", "mithra.sqlite3")
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("open relative .local database: %v", err)
	}
	defer db.Close()

	if err := database.CheckReady(ctx, db); err != nil {
		t.Fatalf("relative .local database is not ready: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("relative database file missing after open: %v", err)
	}
}

func TestFailedOpenRestrictsExistingSidecars(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "mithra.sqlite3")
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(candidate, []byte("not a SQLite database"), 0o644); err != nil {
			t.Fatalf("seed permissive SQLite file %q: %v", candidate, err)
		}
	}

	if _, err := database.Open(context.Background(), path); err == nil {
		t.Fatal("open corrupt database succeeded")
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat failed-open SQLite file %q: %v", candidate, err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Errorf("failed-open SQLite file %q permissions = %04o, want 0600", filepath.Base(candidate), permissions)
		}
	}
}

func TestOpenRejectsAStorageModeWithoutWAL(t *testing.T) {
	t.Parallel()

	_, err := database.Open(context.Background(), ":memory:")
	if err == nil || !strings.Contains(err.Error(), "WAL") {
		t.Fatalf("in-memory database error = %v, want explicit WAL rejection", err)
	}
}

func TestApplyMigrationsRejectsChecksumDrift(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	migrations, err := database.EmbeddedMigrations()
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	migrations[0].Checksum = "different-checksum"

	err = database.ApplyMigrations(ctx, db, migrations)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum drift error = %v, want checksum mismatch", err)
	}
}

func TestApplyMigrationsRejectsNonPrefixHistoryBeforeApplyingAnything(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	migrations := []database.Migration{
		{
			Version:  1,
			Name:     "001_first.sql",
			Checksum: "first",
			SQL:      "CREATE TABLE first_migration_probe (id INTEGER PRIMARY KEY);",
		},
		{
			Version:  2,
			Name:     "002_second.sql",
			Checksum: "second",
			SQL:      "CREATE TABLE second_migration_probe (id INTEGER PRIMARY KEY);",
		},
	}
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations(version, name, checksum, applied_at)
		VALUES(2, '002_second.sql', 'second', '2026-07-18T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed non-prefix migration history: %v", err)
	}

	err = database.ApplyMigrations(context.Background(), db, migrations)
	if err == nil || !strings.Contains(err.Error(), "not an exact prefix") {
		t.Fatalf("non-prefix history error = %v, want exact-prefix rejection", err)
	}

	var migrationRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationRows); err != nil {
		t.Fatalf("count migration ledger rows: %v", err)
	}
	if migrationRows != 1 {
		t.Fatalf("migration ledger rows = %d, want only the seeded version 2", migrationRows)
	}
	for _, table := range []string{"first_migration_probe", "second_migration_probe"} {
		var tables int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&tables); err != nil {
			t.Fatalf("inspect migration SQL writes for %q: %v", table, err)
		}
		if tables != 0 {
			t.Fatalf("non-prefix history applied migration SQL for %q", table)
		}
	}
}

func TestApplyMigrationsRejectsTransactionControlBeforeAnyWrites(t *testing.T) {
	t.Parallel()

	statements := map[string]string{
		"BEGIN":     "BeGiN",
		"COMMIT":    "cOmMiT",
		"ROLLBACK":  "RoLlBaCk",
		"SAVEPOINT": "SaVePoInT migration_escape",
		"RELEASE":   "ReLeAsE migration_escape",
		"END":       "eNd",
	}
	for name, statement := range statements {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite3", ":memory:")
			if err != nil {
				t.Fatalf("open SQLite database: %v", err)
			}
			defer db.Close()

			err = database.ApplyMigrations(context.Background(), db, []database.Migration{{
				Version:  1,
				Name:     "001_escape_probe.sql",
				Checksum: "test",
				SQL:      "CREATE TABLE escape_probe (id INTEGER PRIMARY KEY); /* ignored; COMMIT; */\n-- ignored; ROLLBACK;\n" + statement + ";",
			}})
			if err == nil || !strings.Contains(err.Error(), "transaction-control statement "+name) {
				t.Fatalf("transaction-control error = %v, want %s rejection", err, name)
			}

			var tables int
			if err := db.QueryRow(`
				SELECT COUNT(*)
				FROM sqlite_master
				WHERE type = 'table' AND name IN ('schema_migrations', 'escape_probe')
			`).Scan(&tables); err != nil {
				t.Fatalf("inspect rejected migration writes: %v", err)
			}
			if tables != 0 {
				t.Fatalf("rejected migration persisted %d tables, want no schema or ledger writes", tables)
			}
		})
	}
}

func TestApplyMigrationsAllowsTransactionWordsInLiteralsCommentsAndTriggers(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	err = database.ApplyMigrations(context.Background(), db, []database.Migration{{
		Version:  1,
		Name:     "001_safe_words.sql",
		Checksum: "test",
		SQL: `
			CREATE TABLE notes (body TEXT NOT NULL DEFAULT 'BEGIN; COMMIT; ROLLBACK; SAVEPOINT; RELEASE; END');
			/* BEGIN; COMMIT; ROLLBACK; SAVEPOINT; RELEASE; END; */
			CREATE TRIGGER notes_after_insert AFTER INSERT ON notes
			BEGIN
				INSERT INTO notes(body) VALUES('trigger body');
			END;
		`,
	}})
	if err != nil {
		t.Fatalf("apply safe migration: %v", err)
	}

	var ledgerRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&ledgerRows); err != nil {
		t.Fatalf("read migration ledger: %v", err)
	}
	if ledgerRows != 1 {
		t.Fatalf("migration ledger rows = %d, want 1", ledgerRows)
	}
}

func TestOpenRejectsUnknownNewerSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	_, err = raw.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations(version, name, checksum, applied_at)
		VALUES(999, 'future.sql', 'future', '2026-07-18T00:00:00Z');
	`)
	if err != nil {
		t.Fatalf("seed future schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	_, err = database.Open(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("future schema error = %v, want newer than this binary", err)
	}
}

func TestSQLiteCapabilitiesAndReadinessFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "mithra.sqlite3"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE VIRTUAL TABLE runtime_fts_probe USING fts5(content)`); err != nil {
		t.Fatalf("FTS5 is unavailable: %v", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT load_extension('not-a-real-extension')`); err == nil {
		t.Fatal("extension loading unexpectedly succeeded")
	}

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE readiness_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE readiness_child (parent_id INTEGER NOT NULL REFERENCES readiness_parent(id));
		PRAGMA foreign_keys = OFF;
		INSERT INTO readiness_child(parent_id) VALUES(42);
		PRAGMA foreign_keys = ON;
	`); err != nil {
		t.Fatalf("seed foreign-key violation: %v", err)
	}
	if err := database.CheckReady(ctx, db); err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("foreign-key readiness error = %v, want foreign-key failure", err)
	}
}

func TestOpenRejectsCorruptDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mithra.sqlite3")
	if err := os.WriteFile(path, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}

	_, err := database.Open(context.Background(), path)
	if err == nil {
		t.Fatal("open corrupt database succeeded")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt database error = %v, want an integrity/open failure", err)
	}
}
