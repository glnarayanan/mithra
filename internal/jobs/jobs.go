// Package jobs implements durable SQLite work leases with revision fencing.
package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
)

var (
	ErrInvalid     = errors.New("invalid job request")
	ErrUnavailable = errors.New("no job is available")
	ErrLease       = errors.New("job lease is invalid")
	ErrStale       = errors.New("job inputs are stale")
)

type Spec struct {
	Kind           string
	SubjectID      string
	SourceID       string
	Visibility     policy.Visibility
	IdempotencyKey string
	MaxAttempts    int
}

type Job struct {
	ID                       string
	Kind                     string
	HouseholdID              string
	OwnerID                  string
	Visibility               policy.Visibility
	SourceID                 string
	SubjectID                string
	ExpectedSharedRevision   int64
	ExpectedPersonalRevision int64
	State                    string
	Attempts                 int
	MaxAttempts              int
	Generation               int64
}

type Lease struct {
	Job   Job
	Token string
}

type Service struct {
	db    *sql.DB
	now   func() time.Time
	token func() (string, error)
}

func New(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now, token: randomToken}
}

func (s *Service) Enqueue(ctx context.Context, scope policy.ActorScope, spec Spec) (Job, error) {
	spec.Visibility = policy.PersonalDefault(spec.Visibility)
	if s == nil || s.db == nil || !scope.Valid() || !validSpec(spec) {
		return Job{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, ErrInvalid
	}
	defer tx.Rollback()
	sharedRevision, personalRevision, err := currentRevisions(ctx, tx, scope)
	if err != nil {
		return Job{}, ErrInvalid
	}
	if spec.SourceID != "" {
		var householdID, ownerID, visibility, state string
		err := tx.QueryRowContext(ctx, `SELECT household_id,owner_user_id,visibility,state FROM sources WHERE id=?`, spec.SourceID).Scan(&householdID, &ownerID, &visibility, &state)
		resource := policy.Resource{HouseholdID: householdID, OwnerID: ownerID, Visibility: policy.Visibility(visibility)}
		if err != nil || state != "live" || !scope.CanRead(resource) || (resource.Visibility == policy.Personal && spec.Visibility == policy.Shared) {
			return Job{}, ErrInvalid
		}
	}
	id, err := s.token()
	if err != nil {
		return Job{}, ErrInvalid
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	idempotencyHash := hash(spec.IdempotencyKey)
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,household_id,owner_user_id,visibility,source_id,subject_id,idempotency_hash,expected_shared_revision,expected_personal_revision,state,max_attempts,available_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'queued',?,?,?,?) ON CONFLICT(household_id,owner_user_id,kind,idempotency_hash) DO NOTHING`,
		id, spec.Kind, scope.HouseholdID, scope.ActorID, spec.Visibility, nullable(spec.SourceID), spec.SubjectID, idempotencyHash, sharedRevision, personalRevision, spec.MaxAttempts, now, now, now)
	if err != nil {
		return Job{}, ErrInvalid
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT id,kind,household_id,owner_user_id,visibility,COALESCE(source_id,''),subject_id,expected_shared_revision,expected_personal_revision,state,attempts,max_attempts,lease_generation FROM jobs WHERE household_id=? AND owner_user_id=? AND kind=? AND idempotency_hash=?`, scope.HouseholdID, scope.ActorID, spec.Kind, idempotencyHash))
	if err != nil {
		return Job{}, ErrInvalid
	}
	if err := tx.Commit(); err != nil {
		return Job{}, ErrInvalid
	}
	return job, nil
}

func (s *Service) Claim(ctx context.Context, leaseFor time.Duration) (Lease, error) {
	if s == nil || s.db == nil || leaseFor < time.Second || leaseFor > time.Hour {
		return Lease{}, ErrInvalid
	}
	token, err := s.token()
	if err != nil {
		return Lease{}, ErrUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, ErrUnavailable
	}
	defer tx.Rollback()
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',lease_token_hash=NULL,leased_until=NULL,last_error_code='attempts_exhausted',updated_at=? WHERE state='leased' AND leased_until<=? AND attempts>=max_attempts`, nowText, nowText); err != nil {
		return Lease{}, ErrUnavailable
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE attempts<max_attempts AND ((state='queued' AND available_at<=?) OR (state='leased' AND leased_until<=?)) ORDER BY available_at,created_at LIMIT 1`, nowText, nowText).Scan(&id)
	if err != nil {
		return Lease{}, ErrUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='leased',attempts=attempts+1,lease_generation=lease_generation+1,lease_token_hash=?,leased_until=?,updated_at=? WHERE id=? AND attempts<max_attempts AND ((state='queued' AND available_at<=?) OR (state='leased' AND leased_until<=?))`, hash(token), now.Add(leaseFor).Format(time.RFC3339Nano), nowText, id, nowText, nowText)
	if err != nil {
		return Lease{}, ErrUnavailable
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Lease{}, ErrUnavailable
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT id,kind,household_id,owner_user_id,visibility,COALESCE(source_id,''),subject_id,expected_shared_revision,expected_personal_revision,state,attempts,max_attempts,lease_generation FROM jobs WHERE id=?`, id))
	if err != nil {
		return Lease{}, ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, ErrUnavailable
	}
	return Lease{Job: job, Token: token}, nil
}

// Authorize must run immediately before rebuilding provider context or making
// an outbound call. It cancels work whose actor, source, or revision changed
// after enqueue or lease acquisition.
func (s *Service) Authorize(ctx context.Context, lease Lease) error {
	if !validLease(lease) {
		return ErrLease
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrLease
	}
	defer tx.Rollback()
	job, valid, err := s.lockedJob(ctx, tx, lease)
	if err != nil || !valid {
		return ErrLease
	}
	if !s.current(ctx, tx, job) {
		if err := s.cancelStale(ctx, tx, job); err != nil {
			return ErrLease
		}
		if err := tx.Commit(); err != nil {
			return ErrLease
		}
		return ErrStale
	}
	return tx.Commit()
}

// Complete runs publish inside the same transaction as the lease, membership,
// source, and revision checks. Stale work is cancelled before publish can run.
func (s *Service) Complete(ctx context.Context, lease Lease, publish func(*sql.Tx) error) error {
	if publish == nil || !validLease(lease) {
		return ErrLease
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrLease
	}
	defer tx.Rollback()
	job, valid, err := s.lockedJob(ctx, tx, lease)
	if err != nil || !valid {
		return ErrLease
	}
	if !s.current(ctx, tx, job) {
		if err := s.cancelStale(ctx, tx, job); err != nil {
			return ErrLease
		}
		if err := tx.Commit(); err != nil {
			return ErrLease
		}
		return ErrStale
	}
	if err := publish(tx); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',lease_token_hash=NULL,leased_until=NULL,last_error_code=NULL,updated_at=? WHERE id=? AND state='leased' AND lease_generation=? AND lease_token_hash=?`, s.timestamp(), job.ID, job.Generation, hash(lease.Token))
	if err != nil {
		return ErrLease
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrLease
	}
	return tx.Commit()
}

func (s *Service) cancelStale(ctx context.Context, tx *sql.Tx, job Job) error {
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',lease_token_hash=NULL,leased_until=NULL,last_error_code='stale_inputs',updated_at=? WHERE id=? AND state='leased' AND lease_generation=?`, s.timestamp(), job.ID, job.Generation)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrLease
	}
	return nil
}

func (s *Service) Fail(ctx context.Context, lease Lease, errorCode string, retryAfter time.Duration) error {
	if !validLease(lease) || !validErrorCode(errorCode) || retryAfter < time.Second || retryAfter > time.Hour {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrLease
	}
	defer tx.Rollback()
	job, valid, err := s.lockedJob(ctx, tx, lease)
	if err != nil || !valid {
		return ErrLease
	}
	state := "queued"
	available := s.now().UTC().Add(retryAfter).Format(time.RFC3339Nano)
	if job.Attempts >= job.MaxAttempts {
		state = "failed"
		available = s.timestamp()
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state=?,available_at=?,lease_token_hash=NULL,leased_until=NULL,last_error_code=?,updated_at=? WHERE id=? AND state='leased' AND lease_generation=? AND lease_token_hash=?`, state, available, errorCode, s.timestamp(), job.ID, job.Generation, hash(lease.Token))
	if err != nil {
		return ErrLease
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrLease
	}
	return tx.Commit()
}

func (s *Service) lockedJob(ctx context.Context, tx *sql.Tx, lease Lease) (Job, bool, error) {
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT id,kind,household_id,owner_user_id,visibility,COALESCE(source_id,''),subject_id,expected_shared_revision,expected_personal_revision,state,attempts,max_attempts,lease_generation FROM jobs WHERE id=? AND state='leased' AND lease_generation=? AND lease_token_hash=? AND leased_until>?`, lease.Job.ID, lease.Job.Generation, hash(lease.Token), s.timestamp()))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	return job, err == nil, err
}

func (s *Service) current(ctx context.Context, tx *sql.Tx, job Job) bool {
	scope := policy.ActorScope{ActorID: job.OwnerID, HouseholdID: job.HouseholdID, Role: "adult"}
	shared, personal, err := currentRevisions(ctx, tx, scope)
	if err != nil || shared != job.ExpectedSharedRevision || personal != job.ExpectedPersonalRevision {
		return false
	}
	if job.SourceID == "" {
		return true
	}
	var householdID, ownerID, visibility, state string
	err = tx.QueryRowContext(ctx, `SELECT household_id,owner_user_id,visibility,state FROM sources WHERE id=?`, job.SourceID).Scan(&householdID, &ownerID, &visibility, &state)
	resource := policy.Resource{HouseholdID: householdID, OwnerID: ownerID, Visibility: policy.Visibility(visibility)}
	if err != nil || state != "live" || !scope.CanRead(resource) {
		return false
	}
	return !(resource.Visibility == policy.Personal && job.Visibility == policy.Shared)
}

func currentRevisions(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope policy.ActorScope) (int64, int64, error) {
	var shared, personal int64
	var userStatus, householdStatus string
	err := query.QueryRowContext(ctx, `SELECT hr.shared_revision,ur.personal_revision,u.status,h.status FROM household_revisions hr JOIN user_revisions ur ON ur.household_id=hr.household_id JOIN users u ON u.id=ur.user_id JOIN household_members m ON m.user_id=u.id AND m.household_id=hr.household_id JOIN households h ON h.id=hr.household_id WHERE hr.household_id=? AND ur.user_id=?`, scope.HouseholdID, scope.ActorID).Scan(&shared, &personal, &userStatus, &householdStatus)
	if err != nil || userStatus != "active" || householdStatus != "active" {
		return 0, 0, ErrInvalid
	}
	return shared, personal, nil
}

func scanJob(row *sql.Row) (Job, error) {
	var job Job
	var visibility string
	err := row.Scan(&job.ID, &job.Kind, &job.HouseholdID, &job.OwnerID, &visibility, &job.SourceID, &job.SubjectID, &job.ExpectedSharedRevision, &job.ExpectedPersonalRevision, &job.State, &job.Attempts, &job.MaxAttempts, &job.Generation)
	job.Visibility = policy.Visibility(visibility)
	return job, err
}

func validSpec(spec Spec) bool {
	if len(spec.SubjectID) < 1 || len(spec.SubjectID) > 128 || len(spec.SourceID) > 128 || len(spec.IdempotencyKey) < 16 || len(spec.IdempotencyKey) > 256 || spec.MaxAttempts < 1 || spec.MaxAttempts > 10 {
		return false
	}
	switch spec.Kind {
	case "extract", "transcribe", "capture", "import", "finance", "health", "planning", "coaching", "email":
		return true
	default:
		return false
	}
}

func validLease(lease Lease) bool {
	return lease.Job.ID != "" && lease.Job.Generation > 0 && lease.Token != ""
}

func validErrorCode(value string) bool {
	if len(value) < 1 || len(value) > 48 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Service) timestamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func hash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeID(value string) bool {
	return strings.TrimSpace(value) == value && value != ""
}
