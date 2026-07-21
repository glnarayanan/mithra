// Package database opens and verifies Mithra's SQLite database.
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/migrations"
	sqlite3 "github.com/mattn/go-sqlite3"
)

const (
	busyTimeoutMilliseconds = 5_000
	maxMigrationVersion     = 1<<63 - 1
)

var migrationName = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// Migration is one checksum-locked schema change.
type Migration struct {
	Version  int64
	Name     string
	Checksum string
	SQL      string
}

// AppliedMigration records the migration identity persisted by SQLite.
type AppliedMigration struct {
	Version  int64
	Name     string
	Checksum string
}

// Open initializes the SQLite connection, applies the embedded migration set,
// and confirms that the resulting database is safe to serve.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, path, os.Geteuid(), os.Getegid())
}

// OpenForOwner opens a stopped service's database while a root-owned
// maintenance command retains process control.
func OpenForOwner(ctx context.Context, path string, uid, gid int) (*sql.DB, error) {
	if uid < 0 || gid < 0 {
		return nil, errors.New("SQLite service ownership is invalid")
	}
	return open(ctx, path, uid, gid)
}

func open(ctx context.Context, path string, uid, gid int) (*sql.DB, error) {
	set, err := EmbeddedMigrations()
	if err != nil {
		return nil, err
	}
	return openWithMigrations(ctx, path, set, uid, gid)
}

// OpenWithMigrations exists for migration verification and focused tests. The
// application itself always uses the embedded migration set through Open.
func OpenWithMigrations(ctx context.Context, path string, set []Migration) (*sql.DB, error) {
	return openWithMigrations(ctx, path, set, os.Geteuid(), os.Getegid())
}

func openWithMigrations(ctx context.Context, path string, set []Migration, uid, gid int) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite database path is required")
	}
	databasePath, fileBacked, err := databaseFilePath(path)
	if err != nil {
		return nil, err
	}
	if fileBacked {
		if err := preparePrivateDatabaseFile(databasePath, uid, gid); err != nil {
			return nil, err
		}
		if err := restrictDatabaseFiles(databasePath); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := initialize(ctx, db, set); err != nil {
		_ = db.Close()
		return nil, err
	}
	if fileBacked {
		if err := restrictDatabaseFiles(databasePath); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func databaseFilePath(path string) (string, bool, error) {
	if path == ":memory:" {
		return "", false, nil
	}
	if strings.HasPrefix(path, "file:") {
		return "", false, errors.New("SQLite file URI paths are not supported")
	}
	return path, true, nil
}

func preparePrivateDatabaseFile(path string, uid, gid int) error {
	if err := ensurePrivateParentDirectory(path, uid, gid); err != nil {
		return err
	}

	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return fmt.Errorf("create private SQLite database: %w", createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("close prepared SQLite database: %w", closeErr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("inspect SQLite database path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SQLite database path must be a regular file, got %s", info.Mode().Type())
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open existing SQLite database: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("inspect opened SQLite database: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return errors.New("SQLite database path changed while it was being opened")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict SQLite database permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close existing SQLite database: %w", err)
	}
	return nil
}

func restrictDatabaseFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) && candidate != path {
				continue
			}
			return fmt.Errorf("restrict SQLite file %q permissions: %w", filepath.Base(candidate), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("SQLite file %q must be a regular file, got %s", filepath.Base(candidate), info.Mode().Type())
		}
		if err := os.Chmod(candidate, 0o600); err != nil {
			return fmt.Errorf("restrict SQLite file %q permissions: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

func initialize(ctx context.Context, db *sql.DB, set []Migration) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping SQLite database: %w", err)
	}
	if err := configureSQLite(ctx, db); err != nil {
		return err
	}
	if err := checkSQLiteCapabilities(ctx, db); err != nil {
		return err
	}
	if err := ApplyMigrations(ctx, db, set); err != nil {
		return err
	}
	if err := CheckReady(ctx, db); err != nil {
		return err
	}
	return nil
}

func ensurePrivateParentDirectory(path string, uid, gid int) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create SQLite directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect SQLite directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite parent path must be a real directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("SQLite parent directory %q is group or world writable", directory)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("SQLite parent directory ownership is unavailable")
	}
	if stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
		return fmt.Errorf("SQLite parent directory %q is not owned by the service identity", directory)
	}
	return nil
}

func sqliteDSN(path string) string {
	if path == ":memory:" {
		return path + sqliteParameters(path)
	}
	// file:// URIs require an absolute path component. Relative inputs such as
	// ".local/mithra.sqlite3" become "file://.local/..." under url.URL.String(),
	// so go-sqlite3 treats ".local" as the URI authority and rejects the DSN.
	// Resolve to an absolute path first (local/dev and production both work).
	resolved := path
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			resolved = abs
		}
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(resolved)}
	uri.RawQuery = sqliteParameters("")[1:]
	return uri.String()
}

func sqliteParameters(existing string) string {
	separator := "?"
	if strings.Contains(existing, "?") {
		separator = "&"
	}
	return separator + "_foreign_keys=on&_busy_timeout=" + strconv.Itoa(busyTimeoutMilliseconds) + "&_journal_mode=WAL"
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = NORMAL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMilliseconds),
		"PRAGMA wal_autocheckpoint = 1000",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite with %q: %w", statement, err)
		}
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable SQLite WAL mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("SQLite WAL mode is unavailable: effective mode is %q", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read SQLite foreign-key setting: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("SQLite foreign-key enforcement is unavailable")
	}
	return nil
}

func checkSQLiteCapabilities(ctx context.Context, db *sql.DB) error {
	if err := checkFTS5(ctx, db); err != nil {
		return err
	}
	if err := checkExtensionLoadingDisabled(ctx, db); err != nil {
		return err
	}
	return nil
}

func checkFTS5(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "CREATE VIRTUAL TABLE temp.mithra_fts5_probe USING fts5(content)"); err != nil {
		return fmt.Errorf("SQLite FTS5 capability is unavailable: %w", err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE temp.mithra_fts5_probe"); err != nil {
		return fmt.Errorf("clean up SQLite FTS5 capability probe: %w", err)
	}
	return nil
}

func checkExtensionLoadingDisabled(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open SQLite capability connection: %w", err)
	}
	defer connection.Close()

	err = connection.Raw(func(raw any) error {
		sqliteConnection, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unexpected SQLite driver connection")
		}
		loadErr := sqliteConnection.LoadExtension("", "")
		if loadErr == nil || !strings.Contains(loadErr.Error(), "Extensions have been disabled for static builds") {
			return fmt.Errorf("SQLite extension loading is enabled: %v", loadErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// EmbeddedMigrations returns the validated migrations compiled into the binary.
func EmbeddedMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	set := make([]Migration, 0, len(entries))
	versions := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := migrationName.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil || version <= 0 || version > maxMigrationVersion {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if _, exists := versions[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %d", version)
		}
		versions[version] = struct{}{}

		sqlText, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		checksum := sha256.Sum256(sqlText)
		set = append(set, Migration{
			Version:  version,
			Name:     entry.Name(),
			Checksum: fmt.Sprintf("%x", checksum),
			SQL:      string(sqlText),
		})
	}
	if len(set) == 0 {
		return nil, errors.New("no embedded migrations found")
	}
	sort.Slice(set, func(left, right int) bool { return set[left].Version < set[right].Version })
	return set, nil
}

// ApplyMigrations atomically applies an ordered migration set, rejecting
// checksum drift and a schema created by a newer binary.
func ApplyMigrations(ctx context.Context, db *sql.DB, set []Migration) error {
	if len(set) == 0 {
		return errors.New("migration set is empty")
	}
	if err := validateMigrationSet(set); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	available := make(map[int64]Migration, len(set))
	for _, migration := range set {
		available[migration.Version] = migration
	}
	for _, migration := range applied {
		current, known := available[migration.Version]
		if !known {
			return fmt.Errorf("database schema version %d is newer than this binary", migration.Version)
		}
		if current.Name != migration.Name || current.Checksum != migration.Checksum {
			return fmt.Errorf("migration checksum mismatch for version %d", migration.Version)
		}
	}
	for index, migration := range applied {
		if index >= len(set) {
			return errors.New("migration history is longer than this binary's migration set")
		}
		if migration.Version != set[index].Version {
			return fmt.Errorf("migration history is not an exact prefix of this binary's migration set: expected version %d before version %d", set[index].Version, migration.Version)
		}
	}

	alreadyApplied := make(map[int64]struct{}, len(applied))
	for _, migration := range applied {
		alreadyApplied[migration.Version] = struct{}{}
	}
	for _, migration := range set {
		if _, exists := alreadyApplied[migration.Version]; exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES(?, ?, ?, ?)",
			migration.Version,
			migration.Name,
			migration.Checksum,
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func validateMigrationSet(set []Migration) error {
	var previous int64
	for _, migration := range set {
		if migration.Version <= 0 || migration.Version <= previous {
			return errors.New("migrations must have strictly increasing positive versions")
		}
		if migration.Name == "" || migration.Checksum == "" || strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("migration %d is incomplete", migration.Version)
		}
		if statement := transactionControlStatement(migration.SQL); statement != "" {
			return fmt.Errorf("migration %d contains prohibited transaction-control statement %s", migration.Version, statement)
		}
		previous = migration.Version
	}
	return nil
}

func transactionControlStatement(sqlText string) string {
	scanner := sqlTokenScanner{text: sqlText}
	statementStart := true
	triggerPrefix := 0
	inTriggerBody := false
	triggerEnding := false
	triggerBodyStart := false

	for {
		token, ok := scanner.next()
		if !ok {
			return ""
		}
		if token == ";" {
			switch {
			case inTriggerBody:
				triggerBodyStart = true
			case triggerEnding:
				triggerEnding = false
				triggerPrefix = 0
				statementStart = true
			default:
				triggerPrefix = 0
				statementStart = true
			}
			continue
		}

		word := strings.ToUpper(token)
		if inTriggerBody {
			if triggerBodyStart && word == "END" {
				inTriggerBody = false
				triggerEnding = true
			}
			triggerBodyStart = false
			continue
		}
		if triggerEnding {
			continue
		}
		if statementStart {
			if isTransactionControlStatement(word) {
				return word
			}
			statementStart = false
			if word == "CREATE" {
				triggerPrefix = 1
			}
			continue
		}

		switch triggerPrefix {
		case 1:
			switch word {
			case "TEMP", "TEMPORARY":
				triggerPrefix = 2
			case "TRIGGER":
				triggerPrefix = 3
			default:
				triggerPrefix = 0
			}
		case 2:
			if word == "TRIGGER" {
				triggerPrefix = 3
			} else {
				triggerPrefix = 0
			}
		case 3:
			if word == "BEGIN" {
				inTriggerBody = true
				triggerBodyStart = true
			}
		}
	}
}

func isTransactionControlStatement(word string) bool {
	switch word {
	case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "END":
		return true
	default:
		return false
	}
}

type sqlTokenScanner struct {
	text     string
	position int
}

func (scanner *sqlTokenScanner) next() (string, bool) {
	for scanner.position < len(scanner.text) {
		character := scanner.text[scanner.position]
		switch {
		case character == ';':
			scanner.position++
			return ";", true
		case isSQLWhitespace(character):
			scanner.position++
		case character == '-' && scanner.hasPrefix("--"):
			scanner.position += 2
			for scanner.position < len(scanner.text) && scanner.text[scanner.position] != '\n' {
				scanner.position++
			}
		case character == '/' && scanner.hasPrefix("/*"):
			scanner.position += 2
			for scanner.position+1 < len(scanner.text) && !scanner.hasPrefix("*/") {
				scanner.position++
			}
			if scanner.position+1 < len(scanner.text) {
				scanner.position += 2
			}
		case character == '\'' || character == '"' || character == '`':
			scanner.skipQuoted(character)
		case character == '[':
			scanner.position++
			for scanner.position < len(scanner.text) && scanner.text[scanner.position] != ']' {
				scanner.position++
			}
			if scanner.position < len(scanner.text) {
				scanner.position++
			}
		case isSQLIdentifierCharacter(character):
			start := scanner.position
			for scanner.position < len(scanner.text) && isSQLIdentifierCharacter(scanner.text[scanner.position]) {
				scanner.position++
			}
			return scanner.text[start:scanner.position], true
		default:
			scanner.position++
		}
	}
	return "", false
}

func (scanner *sqlTokenScanner) hasPrefix(prefix string) bool {
	return strings.HasPrefix(scanner.text[scanner.position:], prefix)
}

func (scanner *sqlTokenScanner) skipQuoted(quote byte) {
	scanner.position++
	for scanner.position < len(scanner.text) {
		if scanner.text[scanner.position] != quote {
			scanner.position++
			continue
		}
		scanner.position++
		if scanner.position < len(scanner.text) && scanner.text[scanner.position] == quote {
			scanner.position++
			continue
		}
		return
	}
}

func isSQLWhitespace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func isSQLIdentifierCharacter(character byte) bool {
	return character == '_' || character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

// AppliedMigrations returns the migration identities currently stored in the
// database. It is intentionally read-only so operations can inspect readiness.
func AppliedMigrations(ctx context.Context, db *sql.DB) ([]AppliedMigration, error) {
	return appliedMigrations(ctx, db)
}

type migrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func appliedMigrations(ctx context.Context, queryer migrationQueryer) ([]AppliedMigration, error) {
	rows, err := queryer.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()

	var result []AppliedMigration
	for rows.Next() {
		var migration AppliedMigration
		if err := rows.Scan(&migration.Version, &migration.Name, &migration.Checksum); err != nil {
			return nil, fmt.Errorf("scan migration ledger: %w", err)
		}
		result = append(result, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration ledger: %w", err)
	}
	return result, nil
}

// CheckReady catches structural damage that can survive a process restart.
func CheckReady(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run SQLite foreign-key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID any
		var parent string
		var keyIndex int
		if err := rows.Scan(&table, &rowID, &parent, &keyIndex); err != nil {
			return fmt.Errorf("read SQLite foreign-key failure: %w", err)
		}
		return fmt.Errorf("SQLite foreign key integrity check failed for table %s", table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite foreign-key check: %w", err)
	}
	if err := checkSearchIndex(ctx, db); err != nil {
		return err
	}

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("run SQLite integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	return nil
}

func checkSearchIndex(ctx context.Context, db *sql.DB) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='search_entries_fts')`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect search index: %w", err)
	}
	if exists == 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO search_entries_fts(search_entries_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		return errors.New("search index contains an orphaned row")
	}
	return nil
}
