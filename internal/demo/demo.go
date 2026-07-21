// Package demo creates and safely resets Mithra's marked Build Week household.
package demo

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/capture"
	"github.com/glnarayanan/mithra/internal/coaching"
	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/installer"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

const (
	HouseholdID    = "d0000000000000000000000000000001"
	FixtureVersion = "build-week-v1"
	ownerSeedID    = "d0000000000000000000000000000002"
	partnerSeedID  = "d0000000000000000000000000000003"
)

var ErrUnsafeReset = errors.New("demo reset refused")

type Config struct {
	DatabasePath, SourceRoot, BackupRoot string
	OwnerEmail, PartnerEmail             string
	MasterKey                            []byte
	BeforeComplete                       func() error // test seam after production services have written the candidate.
}

type Receipt struct {
	HouseholdID, FixtureVersion, BackupArchive    string
	SharedRecords, OwnerPersonal, PartnerPersonal int
}

type userState struct {
	id, email, status, password string
}

// Reset works only against the immutable marked household. It takes an
// authenticated encrypted backup before mutation and restores that exact
// generation if any production service or final verification fails.
func Reset(ctx context.Context, cfg Config) (receipt Receipt, err error) {
	if ctx == nil || len(cfg.MasterKey) != 32 {
		return receipt, ErrUnsafeReset
	}
	databasePath, sourceRoot, backupRoot, err := validatePaths(cfg)
	if err != nil {
		return receipt, err
	}
	ownerEmail, err := normalizeEmail(cfg.OwnerEmail)
	if err != nil {
		return receipt, err
	}
	partnerEmail, err := normalizeEmail(cfg.PartnerEmail)
	if err != nil || ownerEmail == partnerEmail {
		return receipt, ErrUnsafeReset
	}
	lock, err := acquireLock(filepath.Join(filepath.Dir(filepath.Dir(databasePath)), ".mithra-demo-reset.lock"))
	if err != nil {
		return receipt, err
	}
	defer releaseLock(lock)

	db, err := database.Open(ctx, databasePath)
	if err != nil {
		return receipt, err
	}
	closed := false
	closeDB := func() error {
		if closed {
			return nil
		}
		closed = true
		return db.Close()
	}
	defer closeDB()
	if err := proveExclusive(ctx, db); err != nil {
		return receipt, err
	}
	if err := preflightIdentity(ctx, db, ownerEmail, partnerEmail); err != nil {
		return receipt, err
	}
	sources, err := storage.New(db, sourceRoot, cfg.MasterKey)
	if err != nil {
		return receipt, err
	}
	journalPath := filepath.Join(filepath.Dir(sourceRoot), "deletion.journal")
	journal, err := imports.NewDeletionJournal(journalPath, cfg.MasterKey)
	if err != nil {
		return receipt, err
	}
	paths := installer.Paths{Data: filepath.Dir(databasePath), Database: databasePath, Sources: sourceRoot, Journal: journalPath, Backups: backupRoot}
	archive, err := installer.CreateBackup(ctx, paths, cfg.MasterKey, time.Now())
	if err != nil {
		return receipt, err
	}
	if err := installer.VerifyBackupArchive(archive, cfg.MasterKey); err != nil {
		return receipt, fmt.Errorf("verify pre-reset backup: %w", err)
	}
	receipt = Receipt{HouseholdID: HouseholdID, FixtureVersion: FixtureVersion, BackupArchive: archive}

	rollback := func(cause error) error {
		closeErr := closeDB()
		restoreErr := installer.RestoreGeneration(paths, archive, cfg.MasterKey, func() error {
			return installer.DatabasePreflight(ctx, databasePath)
		})
		return errors.Join(cause, closeErr, restoreErr)
	}
	oldKeys, owner, partner, err := recreateIdentity(ctx, db, ownerEmail, partnerEmail)
	if err != nil {
		return receipt, rollback(err)
	}
	financeRecords := finance.New(db)
	healthRecords := health.New(db)
	planningRecords := planning.New(db)
	importRecords := imports.NewService(db, sources, financeRecords, healthRecords, planningRecords, journal)
	captureRecords := capture.New(db, sources)

	ownerActor := policy.ActorScope{ActorID: owner.id, HouseholdID: HouseholdID, Role: "owner"}
	partnerActor := policy.ActorScope{ActorID: partner.id, HouseholdID: HouseholdID, Role: "adult"}
	if err := seedSampleTrends(ctx, sources, importRecords, ownerActor); err != nil {
		return receipt, rollback(err)
	}
	if err := seedFinance(ctx, sources, importRecords, partnerActor, policy.Personal, partnerFinanceCSV(), partnerFinanceProposals()); err != nil {
		return receipt, rollback(err)
	}
	if err := seedPlanningCaptures(ctx, captureRecords, ownerActor); err != nil {
		return receipt, rollback(err)
	}
	if cfg.BeforeComplete != nil {
		if err := cfg.BeforeComplete(); err != nil {
			return receipt, rollback(err)
		}
	}
	if err := verifyFixture(ctx, db, ownerActor, partnerActor, &receipt); err != nil {
		return receipt, rollback(err)
	}
	if err := restoreAccessState(ctx, db, owner, partner); err != nil {
		return receipt, rollback(err)
	}
	if err := closeDB(); err != nil {
		return receipt, rollback(err)
	}
	for _, key := range oldKeys {
		if safeStorageKey(key) {
			if err := os.Remove(filepath.Join(sourceRoot, key+".enc")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return receipt, rollback(err)
			}
		}
	}
	if err := syncDirectory(sourceRoot); err != nil {
		return receipt, rollback(err)
	}
	return receipt, nil
}

func seedSampleTrends(ctx context.Context, sources *storage.Service, service *imports.Service, actor policy.ActorScope) error {
	if err := seedFinance(ctx, sources, service, actor, policy.Shared, sharedFinanceCSV(), sharedFinanceProposals()); err != nil {
		return err
	}
	return seedHealth(ctx, sources, service, actor)
}

func validatePaths(cfg Config) (string, string, string, error) {
	databasePath, err := filepath.Abs(strings.TrimSpace(cfg.DatabasePath))
	if err != nil || strings.TrimSpace(cfg.DatabasePath) == "" {
		return "", "", "", ErrUnsafeReset
	}
	sourceRoot, err := filepath.Abs(strings.TrimSpace(cfg.SourceRoot))
	if err != nil || strings.TrimSpace(cfg.SourceRoot) == "" {
		return "", "", "", ErrUnsafeReset
	}
	dataRoot := filepath.Dir(databasePath)
	relative, err := filepath.Rel(dataRoot, sourceRoot)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", "", ErrUnsafeReset
	}
	backupRoot := strings.TrimSpace(cfg.BackupRoot)
	if backupRoot == "" {
		backupRoot = filepath.Join(filepath.Dir(dataRoot), ".mithra-demo-reset-backups")
	}
	backupRoot, err = filepath.Abs(backupRoot)
	if err != nil {
		return "", "", "", ErrUnsafeReset
	}
	if relative, _ := filepath.Rel(dataRoot, backupRoot); relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", "", "", errors.New("demo backup directory must be outside the live data directory")
	}
	return databasePath, sourceRoot, backupRoot, nil
}

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another demo reset is already running")
	}
	return file, nil
}

func releaseLock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func proveExclusive(ctx context.Context, db *sql.DB) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return errors.New("demo reset requires an offline exclusive maintenance window")
	}
	_, err = connection.ExecContext(ctx, "COMMIT")
	return err
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", ErrUnsafeReset
	}
	return value, nil
}

func preflightIdentity(ctx context.Context, db *sql.DB, ownerEmail, partnerEmail string) error {
	owner, ownerFound, err := existingUser(ctx, db, ownerEmail)
	if err != nil {
		return err
	}
	partner, partnerFound, err := existingUser(ctx, db, partnerEmail)
	if err != nil {
		return err
	}
	for _, candidate := range []struct {
		user  userState
		found bool
	}{{owner, ownerFound}, {partner, partnerFound}} {
		if !candidate.found {
			continue
		}
		if candidate.user.status == "disabled" {
			return ErrUnsafeReset
		}
		var household string
		err := db.QueryRowContext(ctx, `SELECT household_id FROM household_members WHERE user_id=?`, candidate.user.id).Scan(&household)
		if err == nil && household != HouseholdID {
			return ErrUnsafeReset
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var markerOwner, markerPartner string
	markerErr := db.QueryRowContext(ctx, `SELECT owner_user_id,partner_user_id FROM demo_households WHERE household_id=? AND fixture_version=?`, HouseholdID, FixtureVersion).Scan(&markerOwner, &markerPartner)
	if markerErr == nil {
		if !ownerFound || !partnerFound || markerOwner != owner.id || markerPartner != partner.id {
			return ErrUnsafeReset
		}
		return nil
	}
	if !errors.Is(markerErr, sql.ErrNoRows) {
		return markerErr
	}
	var reserved int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM households WHERE id=?`, HouseholdID).Scan(&reserved); err != nil {
		return err
	}
	if reserved != 0 {
		return ErrUnsafeReset
	}
	return nil
}

func existingUser(ctx context.Context, db *sql.DB, email string) (userState, bool, error) {
	var user userState
	err := db.QueryRowContext(ctx, `SELECT id,email,status,password_hash FROM users WHERE email=?`, email).Scan(&user.id, &user.email, &user.status, &user.password)
	if errors.Is(err, sql.ErrNoRows) {
		return userState{}, false, nil
	}
	return user, err == nil, err
}

func recreateIdentity(ctx context.Context, db *sql.DB, ownerEmail, partnerEmail string) ([]string, userState, userState, error) {
	owner, err := ensureUser(ctx, db, ownerEmail, ownerSeedID)
	if err != nil {
		return nil, userState{}, userState{}, err
	}
	partner, err := ensureUser(ctx, db, partnerEmail, partnerSeedID)
	if err != nil {
		return nil, userState{}, userState{}, err
	}
	for _, user := range []userState{owner, partner} {
		var household string
		err := db.QueryRowContext(ctx, `SELECT household_id FROM household_members WHERE user_id=?`, user.id).Scan(&household)
		if err == nil && household != HouseholdID {
			return nil, userState{}, userState{}, ErrUnsafeReset
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, userState{}, userState{}, err
		}
	}
	var markedOwner, markedPartner string
	markerErr := db.QueryRowContext(ctx, `SELECT owner_user_id,partner_user_id FROM demo_households WHERE household_id=? AND fixture_version=?`, HouseholdID, FixtureVersion).Scan(&markedOwner, &markedPartner)
	if markerErr == nil && (markedOwner != owner.id || markedPartner != partner.id) {
		return nil, userState{}, userState{}, ErrUnsafeReset
	}
	if markerErr != nil && !errors.Is(markerErr, sql.ErrNoRows) {
		return nil, userState{}, userState{}, markerErr
	}
	if errors.Is(markerErr, sql.ErrNoRows) {
		var one int
		if err := db.QueryRowContext(ctx, `SELECT 1 FROM households WHERE id=?`, HouseholdID).Scan(&one); err == nil {
			return nil, userState{}, userState{}, ErrUnsafeReset
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, userState{}, userState{}, err
		}
	}
	var oldKeys []string
	rows, err := db.QueryContext(ctx, `SELECT storage_key FROM sources WHERE household_id=?`, HouseholdID)
	if err != nil {
		return nil, userState{}, userState{}, err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, userState{}, userState{}, err
		}
		oldKeys = append(oldKeys, key)
	}
	if err := rows.Close(); err != nil {
		return nil, userState{}, userState{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, userState{}, userState{}, err
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM households WHERE id=? AND EXISTS (SELECT 1 FROM demo_households WHERE household_id=households.id AND fixture_version=?)`, HouseholdID, FixtureVersion); err != nil {
		return nil, userState{}, userState{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET status='active',disabled_at=NULL,updated_at=? WHERE id IN (?,?)`, stamp, owner.id, partner.id); err != nil {
		return nil, userState{}, userState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO households(id,status,owner_user_id,timezone,created_at,updated_at) VALUES(?,'active',?,'Asia/Kolkata',?,?)`, HouseholdID, owner.id, stamp, stamp); err != nil {
		return nil, userState{}, userState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,'owner',?),(?,?,'adult',?)`, HouseholdID, owner.id, stamp, HouseholdID, partner.id, stamp); err != nil {
		return nil, userState{}, userState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO demo_households(household_id,fixture_version,owner_user_id,partner_user_id,created_at) VALUES(?,?,?,?,?)`, HouseholdID, FixtureVersion, owner.id, partner.id, stamp); err != nil {
		return nil, userState{}, userState{}, err
	}
	return oldKeys, owner, partner, tx.Commit()
}

func ensureUser(ctx context.Context, db *sql.DB, email, fallbackID string) (userState, error) {
	var user userState
	err := db.QueryRowContext(ctx, `SELECT id,email,status,password_hash FROM users WHERE email=?`, email).Scan(&user.id, &user.email, &user.status, &user.password)
	if err == nil {
		if user.status == "disabled" {
			return userState{}, ErrUnsafeReset
		}
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return userState{}, err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,status,password_hash,created_at,updated_at) VALUES(?,?,'pending','',?,?)`, fallbackID, email, stamp, stamp); err != nil {
		return userState{}, err
	}
	return userState{id: fallbackID, email: email, status: "pending"}, nil
}

func seedFinance(ctx context.Context, sources *storage.Service, service *imports.Service, actor policy.ActorScope, visibility policy.Visibility, content []byte, proposals imports.ProposalSet) error {
	document, err := imports.New(nil).Extract(ctx, imports.Input{Name: "family-finance.csv", ContentType: "text/csv", Bytes: content})
	if err != nil || len(document.Fragments) < 2 {
		return errors.New("extract demo finance fixture")
	}
	source, err := sources.Store(ctx, actor, content, storage.Metadata{Family: "csv", Version: 1, Visibility: visibility, LocatorKind: "source", LocatorValue: "demo-finance-v1"})
	if err != nil {
		return err
	}
	review, err := service.Stage(ctx, actor, source, "family-finance.csv", proposals, "")
	if err != nil || hasBlocker(review.Issues) {
		return errors.Join(err, errors.New("demo finance mapping did not validate"))
	}
	return service.Commit(ctx, actor, review.ID, review.Version)
}

func seedHealth(ctx context.Context, sources *storage.Service, service *imports.Service, actor policy.ActorScope) error {
	content := healthPDF()
	document, err := imports.New(nil).Extract(ctx, imports.Input{Name: "health-report.pdf", ContentType: "application/pdf", Bytes: content})
	if err != nil || len(document.Fragments) != 1 {
		return errors.Join(errors.New("extract demo health PDF"), err)
	}
	source, err := sources.Store(ctx, actor, content, storage.Metadata{Family: "pdf", Version: 1, Visibility: policy.Personal, LocatorKind: "source", LocatorValue: "demo-health-v1"})
	if err != nil {
		return err
	}
	proposals := healthProposals()
	review, err := service.Stage(ctx, actor, source, "health-report.pdf", proposals, "")
	if err != nil || hasBlocker(review.Issues) {
		return errors.Join(err, fmt.Errorf("demo health mapping did not validate: %+v", review.Issues))
	}
	return service.Commit(ctx, actor, review.ID, review.Version)
}

func seedPlanningCaptures(ctx context.Context, service *capture.Service, actor policy.ActorScope) error {
	samples := []capture.PlanningProposal{
		{Title: "Quarterly finance review", Description: "Household finance review.", Location: "Home", StartsAt: "2026-04-26T10:00", EndsAt: "2026-04-26T11:00", Timezone: "Asia/Kolkata", Status: "completed"},
		{Title: "Home maintenance visit", Description: "Scheduled home maintenance.", Location: "Home", StartsAt: "2026-05-17T09:00", EndsAt: "2026-05-17T10:00", Timezone: "Asia/Kolkata", Status: "completed"},
		{Title: "Travel booking review", Description: "Travel booking details reviewed together.", Location: "Home", StartsAt: "2026-06-21T11:00", EndsAt: "2026-06-21T11:45", Timezone: "Asia/Kolkata", Status: "completed"},
		{Title: "Review travel documents", Description: "Passports and bookings together.", Location: "Home", StartsAt: "2026-07-25T10:00", EndsAt: "2026-07-25T10:45", Timezone: "Asia/Kolkata", Status: "planned"},
		{Title: "Insurance renewal review", Description: "Insurance renewal details.", Location: "Home", StartsAt: "2026-07-27T18:00", EndsAt: "2026-07-27T18:30", Timezone: "Asia/Kolkata", Status: "planned"},
		{Title: "August household planning", Description: "Household dates and plans for August.", Location: "Home", StartsAt: "2026-08-02T10:00", EndsAt: "2026-08-02T10:45", Timezone: "Asia/Kolkata", Status: "planned"},
	}
	for _, sample := range samples {
		receipt, err := service.SubmitText(ctx, actor, capture.TextRequest{Text: sample.Title + ".", Summary: sample.Title + " added.", Visibility: policy.Shared, Proposal: capture.Proposal{Variant: capture.PlanningVariant, Planning: &sample}})
		if err != nil {
			return err
		}
		if err := service.Confirm(ctx, actor, receipt.ID); err != nil {
			return err
		}
	}
	return nil
}

func hasBlocker(issues []imports.Issue) bool {
	for _, issue := range issues {
		if !issue.Warning {
			return true
		}
	}
	return false
}

func verifyFixture(ctx context.Context, db *sql.DB, owner, partner policy.ActorScope, receipt *Receipt) error {
	coach := coaching.New(db)
	ownerOverview, err := coach.Overview(ctx, owner, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		return err
	}
	partnerReview, err := coach.Week(ctx, partner, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil || !ownerOverview.HasRecords || !partnerReview.HasRecords || len(ownerOverview.SharedContext.Facts) == 0 || len(ownerOverview.PersonalContext.Facts) == 0 || len(partnerReview.Personal.Context.Facts) == 0 {
		return errors.New("demo Family Brief or private Week in Review overlay is incomplete")
	}
	receipt.SharedRecords = len(ownerOverview.SharedContext.Facts)
	receipt.OwnerPersonal = len(ownerOverview.PersonalContext.Facts)
	receipt.PartnerPersonal = len(partnerReview.Personal.Context.Facts)
	return nil
}

func restoreAccessState(ctx context.Context, db *sql.DB, owner, partner userState) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, user := range []userState{owner, partner} {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status=?,password_hash=?,disabled_at=NULL,updated_at=? WHERE id=?`, user.status, user.password, stamp, user.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM browser_sessions WHERE user_id=?`, user.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE user_id=?`, user.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM invitations WHERE inviter_user_id=? OR invited_email=?`, user.id, user.email); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type financeSample struct{ kind, label, category, date, endDate, status, amount string }

var sharedFinanceSamples = []financeSample{
	{"income", "Household income", "Income", "2026-04-01", "", "", "180000"},
	{"spending", "Groceries", "Groceries", "2026-04-08", "", "", "14000"},
	{"spending", "Utilities", "Utilities", "2026-04-12", "", "", "6200"},
	{"spending", "Dining out", "Dining", "2026-04-19", "", "", "4800"},
	{"spending", "Home loan EMI", "Debt repayment", "2026-04-03", "", "", "21000"},
	{"spending", "Transport", "Transport", "2026-04-10", "", "", "3500"},
	{"spending", "Insurance", "Insurance", "2026-04-15", "", "", "2500"},
	{"spending", "Healthcare", "Healthcare", "2026-04-17", "", "", "3500"},
	{"income", "Household income", "Income", "2026-05-01", "", "", "182000"},
	{"spending", "Groceries", "Groceries", "2026-05-09", "", "", "14800"},
	{"spending", "Utilities", "Utilities", "2026-05-13", "", "", "5900"},
	{"spending", "Dining out", "Dining", "2026-05-21", "", "", "5200"},
	{"spending", "Home loan EMI", "Debt repayment", "2026-05-03", "", "", "21000"},
	{"spending", "Transport", "Transport", "2026-05-10", "", "", "3700"},
	{"spending", "Insurance", "Insurance", "2026-05-15", "", "", "2500"},
	{"spending", "Healthcare", "Healthcare", "2026-05-17", "", "", "1800"},
	{"income", "Household income", "Income", "2026-06-01", "", "", "185000"},
	{"spending", "Groceries", "Groceries", "2026-06-08", "", "", "15200"},
	{"spending", "Utilities", "Utilities", "2026-06-12", "", "", "6100"},
	{"spending", "Dining out", "Dining", "2026-06-20", "", "", "6500"},
	{"spending", "School fees", "Education", "2026-06-24", "", "", "25000"},
	{"spending", "Home loan EMI", "Debt repayment", "2026-06-03", "", "", "21000"},
	{"spending", "Transport", "Transport", "2026-06-10", "", "", "4200"},
	{"spending", "Insurance", "Insurance", "2026-06-15", "", "", "2500"},
	{"spending", "Healthcare", "Healthcare", "2026-06-17", "", "", "4500"},
	{"income", "Household income", "Income", "2026-07-01", "", "", "188000"},
	{"spending", "Groceries", "Groceries", "2026-07-08", "", "", "14500"},
	{"spending", "Utilities", "Utilities", "2026-07-12", "", "", "6400"},
	{"spending", "Dining out", "Dining", "2026-07-18", "", "", "4200"},
	{"spending", "Home loan EMI", "Debt repayment", "2026-07-03", "", "", "21000"},
	{"spending", "Transport", "Transport", "2026-07-10", "", "", "3800"},
	{"spending", "Insurance", "Insurance", "2026-07-15", "", "", "2500"},
	{"spending", "Healthcare", "Healthcare", "2026-07-17", "", "", "2200"},
	{"budget", "July groceries budget", "Groceries", "2026-07-01", "2026-07-31", "", "18000"},
	{"asset", "Emergency fund", "Savings", "2026-07-01", "", "", "450000"},
	{"liability", "Home loan balance", "Home loan", "2026-07-01", "", "", "3200000"},
	{"obligation", "Insurance renewal", "Insurance", "2026-07-28", "", "pending", "24000"},
	{"obligation", "Vehicle insurance renewal", "Insurance", "2026-08-12", "", "pending", "12000"},
	{"obligation", "Annual home maintenance", "Home", "2026-08-18", "", "pending", "8000"},
}

var partnerFinanceSamples = []financeSample{
	{"spending", "Course subscription", "Learning", "2026-04-12", "", "", "3200"},
	{"spending", "Course subscription", "Learning", "2026-05-12", "", "", "3200"},
	{"spending", "Course subscription", "Learning", "2026-06-12", "", "", "3200"},
	{"spending", "Course subscription", "Learning", "2026-07-12", "", "", "3200"},
}

func financeCSV(samples []financeSample) []byte {
	var out strings.Builder
	out.WriteString("kind,label,category,date,end_date,status,amount\n")
	for _, sample := range samples {
		fmt.Fprintf(&out, "%s,%s,%s,%s,%s,%s,%s\n", sample.kind, sample.label, sample.category, sample.date, sample.endDate, sample.status, sample.amount)
	}
	return []byte(out.String())
}

func financeProposals(samples []financeSample) imports.ProposalSet {
	records := make([]imports.ProposedRecord, 0, len(samples))
	for index, sample := range samples {
		records = append(records, imports.ProposedRecord{Family: "finance", Locator: imports.Locator{Kind: "row", Value: fmt.Sprintf("row:%d", index+2)}, Finance: &imports.FinanceProposal{Kind: sample.kind, Label: sample.label, Category: sample.category, Date: sample.date, EndDate: sample.endDate, Status: sample.status, Amount: sample.amount}})
	}
	return imports.ProposalSet{Records: records}
}

func sharedFinanceCSV() []byte                     { return financeCSV(sharedFinanceSamples) }
func partnerFinanceCSV() []byte                    { return financeCSV(partnerFinanceSamples) }
func sharedFinanceProposals() imports.ProposalSet  { return financeProposals(sharedFinanceSamples) }
func partnerFinanceProposals() imports.ProposalSet { return financeProposals(partnerFinanceSamples) }

var healthSamples = []imports.HealthProposal{
	{Subject: "Owner", Analyte: "Glucose", Specimen: "Blood", ReferenceContext: "fasting", ObservedOn: "2026-04-15", Value: "101", Unit: "mg/dL", ReferenceLow: "70", ReferenceHigh: "99", ReferenceUnit: "mg/dL"},
	{Subject: "Owner", Analyte: "Glucose", Specimen: "Blood", ReferenceContext: "fasting", ObservedOn: "2026-05-15", Value: "98", Unit: "mg/dL", ReferenceLow: "70", ReferenceHigh: "99", ReferenceUnit: "mg/dL"},
	{Subject: "Owner", Analyte: "Glucose", Specimen: "Blood", ReferenceContext: "fasting", ObservedOn: "2026-06-15", Value: "94", Unit: "mg/dL", ReferenceLow: "70", ReferenceHigh: "99", ReferenceUnit: "mg/dL"},
	{Subject: "Owner", Analyte: "Glucose", Specimen: "Blood", ReferenceContext: "fasting", ObservedOn: "2026-07-15", Value: "91", Unit: "mg/dL", ReferenceLow: "70", ReferenceHigh: "99", ReferenceUnit: "mg/dL"},
	{Subject: "Owner", Analyte: "Weight", ObservedOn: "2026-04-01", Value: "78.5", Unit: "kg"},
	{Subject: "Owner", Analyte: "Weight", ObservedOn: "2026-05-01", Value: "77.8", Unit: "kg"},
	{Subject: "Owner", Analyte: "Weight", ObservedOn: "2026-06-01", Value: "77.2", Unit: "kg"},
	{Subject: "Owner", Analyte: "Weight", ObservedOn: "2026-07-01", Value: "76.7", Unit: "kg"},
	{Subject: "Owner", Analyte: "Resting heart rate", ObservedOn: "2026-04-01", Value: "74", Unit: "bpm"},
	{Subject: "Owner", Analyte: "Resting heart rate", ObservedOn: "2026-05-01", Value: "72", Unit: "bpm"},
	{Subject: "Owner", Analyte: "Resting heart rate", ObservedOn: "2026-06-01", Value: "70", Unit: "bpm"},
	{Subject: "Owner", Analyte: "Resting heart rate", ObservedOn: "2026-07-01", Value: "69", Unit: "bpm"},
	{Subject: "Owner", Analyte: "Glucose", Specimen: "Blood", ReferenceContext: "post-meal", ObservedOn: "2026-06-20", Value: "140", Unit: "mg/dL", ReferenceLow: "70", ReferenceHigh: "140", ReferenceUnit: "mg/dL"},
	{Subject: "Owner", Analyte: "Glucose", Specimen: "Blood", ReferenceContext: "post-meal", ObservedOn: "2026-07-15", Value: "7.8", Unit: "mmol/L", ReferenceLow: "3.9", ReferenceHigh: "7.8", ReferenceUnit: "mmol/L"},
}

func healthProposals() imports.ProposalSet {
	records := make([]imports.ProposedRecord, 0, len(healthSamples))
	for index := range healthSamples {
		proposal := healthSamples[index]
		records = append(records, imports.ProposedRecord{Family: "health", Locator: imports.Locator{Kind: "page", Value: "page:1"}, Health: &proposal})
	}
	return imports.ProposalSet{Records: records}
}

func healthPDF() []byte {
	var content strings.Builder
	content.WriteString("BT /F1 9 Tf 54 750 Td (Health report - Owner) Tj")
	for _, sample := range healthSamples {
		fmt.Fprintf(&content, " 0 -16 Td (%s %s %s %s) Tj", sample.ObservedOn, sample.Analyte, sample.Value, sample.Unit)
	}
	content.WriteString(" ET\n")
	stream := content.String()
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return output.Bytes()
}

func safeStorageKey(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
