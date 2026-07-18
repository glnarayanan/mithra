// Package household implements the two-adult membership and invitation rules.
package household

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
	ErrInviteInvalid = errors.New("invitation is invalid or unavailable")
	ErrNotOwner      = errors.New("only the household owner can invite")
	ErrAlreadyBound  = errors.New("account already belongs to a household")
	ErrAdultLimit    = errors.New("household already has two adults")
	ErrRecovery      = errors.New("ownership recovery is unavailable")
)

type Config struct {
	Now   func() time.Time
	Token func() (string, error)
}

type Service struct {
	db    *sql.DB
	now   func() time.Time
	token func() (string, error)
}

type Invitation struct {
	Token     string
	ExpiresAt time.Time
}

type Member struct {
	Email  string
	Role   string
	Status string
}

func New(db *sql.DB, cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Token == nil {
		cfg.Token = randomToken
	}
	return &Service{db: db, now: cfg.Now, token: cfg.Token}
}

// SyncAllowlist atomically disables removed users and revokes their active
// access material. Entries re-added by an operator must bootstrap again.
func (s *Service) SyncAllowlist(ctx context.Context, emails []string) error {
	allowed := map[string]struct{}{}
	for _, email := range emails {
		normalized, err := NormalizeEmail(email)
		if err != nil {
			return err
		}
		allowed[normalized] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.timestamp()
	rows, err := tx.QueryContext(ctx, `SELECT id,email,status FROM users`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var id, email, status string
		if err := rows.Scan(&id, &email, &status); err != nil {
			return err
		}
		if _, ok := allowed[email]; ok {
			seen[email] = struct{}{}
			if status == "disabled" {
				if _, err := tx.ExecContext(ctx, `UPDATE users SET status='pending',disabled_at=NULL,updated_at=? WHERE id=?`, now, id); err != nil {
					return err
				}
			}
			continue
		}
		if status != "disabled" {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET status='disabled',disabled_at=?,updated_at=? WHERE id=?`, now, now, id); err != nil {
				return err
			}
		}
		if err := revokeUserTx(ctx, tx, id, now); err != nil {
			return err
		}
		if err := closeOwnerHouseholdTx(ctx, tx, id, now); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for email := range allowed {
		if _, ok := seen[email]; !ok {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, newID(), email, now, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Service) CreateInvitation(ctx context.Context, scope policy.ActorScope, invitedEmail string, lifetime time.Duration) (Invitation, error) {
	if !scope.Valid() || scope.Role != "owner" {
		return Invitation{}, ErrNotOwner
	}
	email, err := NormalizeEmail(invitedEmail)
	if err != nil || lifetime <= 0 {
		return Invitation{}, ErrInviteInvalid
	}
	token, err := s.token()
	if err != nil {
		return Invitation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback()
	var owner, status string
	if err := tx.QueryRowContext(ctx, `SELECT owner_user_id,status FROM households WHERE id=?`, scope.HouseholdID).Scan(&owner, &status); err != nil || owner != scope.ActorID || status != "active" {
		return Invitation{}, ErrNotOwner
	}
	var candidateStatus, bound string
	err = tx.QueryRowContext(ctx, `SELECT u.status,COALESCE(m.household_id,'') FROM users u LEFT JOIN household_members m ON m.user_id=u.id WHERE u.email=?`, email).Scan(&candidateStatus, &bound)
	if err != nil || candidateStatus == "disabled" {
		return Invitation{}, ErrInviteInvalid
	}
	if bound != "" {
		return Invitation{}, ErrAlreadyBound
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM household_members WHERE household_id=?`, scope.HouseholdID).Scan(&count); err != nil {
		return Invitation{}, err
	}
	if count >= 2 {
		return Invitation{}, ErrAdultLimit
	}
	now := s.timestamp()
	expires := s.now().UTC().Add(lifetime)
	if _, err := tx.ExecContext(ctx, `UPDATE invitations SET revoked_at=? WHERE household_id=? AND invited_email=? AND used_at IS NULL AND revoked_at IS NULL`, now, scope.HouseholdID, email); err != nil {
		return Invitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO invitations(token_hash,household_id,inviter_user_id,invited_email,expires_at,created_at) VALUES(?,?,?,?,?,?)`, tokenHash(token), scope.HouseholdID, scope.ActorID, email, expires.Format(time.RFC3339Nano), now); err != nil {
		return Invitation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Invitation{}, err
	}
	return Invitation{Token: token, ExpiresAt: expires}, nil
}

func (s *Service) Members(ctx context.Context, householdID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.email,m.role,u.status FROM household_members m JOIN users u ON u.id=m.user_id WHERE m.household_id=? AND u.status='active' ORDER BY CASE m.role WHEN 'owner' THEN 0 ELSE 1 END, u.email`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []Member
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.Email, &member.Role, &member.Status); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// AttachActivationTx is deliberately transaction-local: password activation
// and membership creation either both commit or neither does.
func (s *Service) AttachActivationTx(ctx context.Context, tx *sql.Tx, userID, email, invite string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT household_id FROM household_members WHERE user_id=?`, userID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := s.timestamp()
	if invite != "" {
		return s.acceptInvitationTx(ctx, tx, userID, email, invite, now)
	}
	householdID := newID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO households(id,status,owner_user_id,created_at,updated_at) VALUES(?,'active',?,?,?)`, householdID, userID, now, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,'owner',?)`, householdID, userID, now)
	return err
}

func (s *Service) acceptInvitationTx(ctx context.Context, tx *sql.Tx, userID, email, raw, now string) error {
	var householdID, inviterID, invited, expires, used, revoked, householdStatus string
	err := tx.QueryRowContext(ctx, `SELECT i.household_id,i.inviter_user_id,i.invited_email,i.expires_at,COALESCE(i.used_at,''),COALESCE(i.revoked_at,''),h.status FROM invitations i JOIN households h ON h.id=i.household_id WHERE i.token_hash=?`, tokenHash(raw)).Scan(&householdID, &inviterID, &invited, &expires, &used, &revoked, &householdStatus)
	if err != nil || !strings.EqualFold(email, invited) || used != "" || revoked != "" || householdStatus != "active" {
		return ErrInviteInvalid
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !s.now().UTC().Before(expiresAt) {
		return ErrInviteInvalid
	}
	var inviterStatus, owner string
	if err := tx.QueryRowContext(ctx, `SELECT u.status,h.owner_user_id FROM users u JOIN households h ON h.id=? WHERE u.id=?`, householdID, inviterID).Scan(&inviterStatus, &owner); err != nil || inviterStatus != "active" || owner != inviterID {
		return ErrInviteInvalid
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM household_members WHERE household_id=?`, householdID).Scan(&count); err != nil {
		return err
	}
	if count >= 2 {
		return ErrAdultLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,'adult',?)`, householdID, userID, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE invitations SET used_at=? WHERE token_hash=? AND used_at IS NULL AND revoked_at IS NULL`, now, tokenHash(raw))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrInviteInvalid
	}
	return nil
}

func (s *Service) RecoverOwner(ctx context.Context, householdID, candidateEmail string) error {
	email, err := NormalizeEmail(candidateEmail)
	if err != nil {
		return ErrRecovery
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, owner string
	if err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(owner_user_id,'') FROM households WHERE id=?`, householdID).Scan(&status, &owner); err != nil || status != "closed" || owner != "" {
		return ErrRecovery
	}
	var userID, userStatus, bound string
	if err := tx.QueryRowContext(ctx, `SELECT u.id,u.status,COALESCE(m.household_id,'') FROM users u LEFT JOIN household_members m ON m.user_id=u.id WHERE u.email=?`, email).Scan(&userID, &userStatus, &bound); err != nil || userStatus == "disabled" || (userStatus != "pending" && userStatus != "active") || (bound != "" && bound != householdID) {
		return ErrRecovery
	}
	now := s.timestamp()
	if _, err := tx.ExecContext(ctx, `DELETE FROM household_members WHERE household_id=? AND role='owner' AND user_id<>?`, householdID, userID); err != nil {
		return err
	}
	if bound == householdID {
		if _, err := tx.ExecContext(ctx, `UPDATE household_members SET role='owner' WHERE household_id=? AND user_id=?`, householdID, userID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO household_members(household_id,user_id,role,created_at) VALUES(?,?,'owner',?)`, householdID, userID, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE households SET status='active',owner_user_id=?,updated_at=? WHERE id=?`, userID, now, householdID); err != nil {
		return err
	}
	return tx.Commit()
}

func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) < 3 || len(email) > 254 || strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return "", errors.New("invalid email")
	}
	return email, nil
}
func (s *Service) timestamp() string { return s.now().UTC().Format(time.RFC3339Nano) }
func tokenHash(token string) string {
	value := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", value[:])
}
func newID() string {
	value, err := randomToken()
	if err != nil {
		panic(err)
	}
	return value
}
func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func revokeUserTx(ctx context.Context, tx *sql.Tx, userID, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE invitations SET revoked_at=? WHERE (inviter_user_id=? OR invited_email=(SELECT email FROM users WHERE id=?)) AND revoked_at IS NULL AND used_at IS NULL`, now, userID, userID)
	return err
}

// closeOwnerHouseholdTx freezes a household as soon as its owner is removed.
// Existing adult cookies are also revoked, so re-opening through explicit
// recovery cannot make a previously valid browser session live again.
func closeOwnerHouseholdTx(ctx context.Context, tx *sql.Tx, userID, now string) error {
	var householdID string
	err := tx.QueryRowContext(ctx, `SELECT household_id FROM household_members WHERE user_id=? AND role='owner'`, userID).Scan(&householdID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE households SET status='closed',owner_user_id=NULL,updated_at=? WHERE id=?`, now, householdID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id IN (SELECT user_id FROM household_members WHERE household_id=?) AND revoked_at IS NULL`, now, householdID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE invitations SET revoked_at=? WHERE household_id=? AND used_at IS NULL AND revoked_at IS NULL`, now, householdID)
	return err
}
