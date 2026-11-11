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
	ErrAlreadyMember = errors.New("person already belongs to this household")
	ErrNoInvitation  = errors.New("no open invitation for that address")
	ErrNotPartner    = errors.New("only the partner can be removed")
	ErrNotOwnerEmail = errors.New("only the configured owner can start a household")
)

type Config struct {
	Now   func() time.Time
	Token func() (string, error)
}

type Service struct {
	db         *sql.DB
	now        func() time.Time
	token      func() (string, error)
	ownerEmail string
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

// SetOwnerEmail names the one address that may start this install's household
// without an invitation. It is set once at startup, before any request.
func (s *Service) SetOwnerEmail(email string) error {
	if strings.TrimSpace(email) == "" {
		s.ownerEmail = ""
		return nil
	}
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	s.ownerEmail = normalized
	return nil
}

// OwnerEmail returns the configured owner address, or "" when none is set.
func (s *Service) OwnerEmail() string { return s.ownerEmail }

// EnsureOwner records the configured owner address as a pending account so
// the first sign-in can choose a password. It never disables anyone: once a
// household exists, people are invited and removed from Settings.
func (s *Service) EnsureOwner(ctx context.Context) error {
	if s.ownerEmail == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.timestamp()
	var id, status, bound string
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.status,COALESCE(m.household_id,'') FROM users u LEFT JOIN household_members m ON m.user_id=u.id WHERE u.email=?`, s.ownerEmail).Scan(&id, &status, &bound)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, newID(), s.ownerEmail, now, now); err != nil {
			return err
		}
	case err != nil:
		return err
	case status == "disabled" && bound == "":
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status='pending',password_hash='',disabled_at=NULL,updated_at=? WHERE id=?`, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HasHousehold reports whether this install already has a real (non-demo)
// household, open or closed.
func (s *Service) HasHousehold(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM households WHERE id NOT IN (SELECT household_id FROM demo_households)`).Scan(&count)
	return count > 0, err
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
	now := s.timestamp()
	var candidateID, candidateStatus, bound, role string
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.status,COALESCE(m.household_id,''),COALESCE(m.role,'') FROM users u LEFT JOIN household_members m ON m.user_id=u.id WHERE u.email=?`, email).Scan(&candidateID, &candidateStatus, &bound, &role)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Settings is the only way in for a second adult: the invitation
		// creates the account, which stays pending until the link is used.
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, newID(), email, now, now); err != nil {
			return Invitation{}, err
		}
	case err != nil:
		return Invitation{}, err
	case bound != "" && bound != scope.HouseholdID:
		return Invitation{}, ErrAlreadyBound
	case bound == scope.HouseholdID && (role == "owner" || candidateStatus != "disabled"):
		return Invitation{}, ErrAlreadyMember
	case candidateStatus == "disabled":
		// A removed partner, or someone whose invitation was cancelled. A
		// fresh invitation restores them; they choose a new password.
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status='pending',password_hash='',disabled_at=NULL,updated_at=? WHERE id=?`, now, candidateID); err != nil {
			return Invitation{}, err
		}
	}
	if bound != scope.HouseholdID {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM household_members WHERE household_id=?`, scope.HouseholdID).Scan(&count); err != nil {
			return Invitation{}, err
		}
		if count >= 2 {
			return Invitation{}, ErrAdultLimit
		}
	}
	expires := s.now().UTC().Add(lifetime)
	// The household has one partner place, so only the newest invitation
	// stays open. Accounts that only had an older invitation close again.
	if _, err := tx.ExecContext(ctx, `UPDATE users SET status='disabled',disabled_at=?,updated_at=? WHERE status='pending' AND password_hash='' AND email<>? AND email IN (SELECT invited_email FROM invitations WHERE household_id=? AND used_at IS NULL AND revoked_at IS NULL) AND id NOT IN (SELECT user_id FROM household_members WHERE role='owner')`, now, now, email, scope.HouseholdID); err != nil {
		return Invitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invitations SET revoked_at=? WHERE household_id=? AND used_at IS NULL AND revoked_at IS NULL`, now, scope.HouseholdID); err != nil {
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

// Access is one line of the household access list in Settings: an adult
// member, or someone with an open invitation.
type Access struct {
	UserID    string // empty for someone who has only an invitation
	Email     string
	Role      string // owner, adult, or invited
	State     string // active, pending, removed, invited, or expired
	Since     time.Time
	ExpiresAt time.Time
}

// Access lists the owner first, then the partner or the open invitation.
func (s *Service) Access(ctx context.Context, householdID string) ([]Access, error) {
	type invite struct{ created, expires time.Time }
	invites := map[string]invite{}
	rows, err := s.db.QueryContext(ctx, `SELECT invited_email,created_at,expires_at FROM invitations WHERE household_id=? AND used_at IS NULL AND revoked_at IS NULL ORDER BY created_at`, householdID)
	if err != nil {
		return nil, err
	}
	var order []string
	for rows.Next() {
		var email, created, expires string
		if err := rows.Scan(&email, &created, &expires); err != nil {
			rows.Close()
			return nil, err
		}
		email = strings.ToLower(email)
		if _, seen := invites[email]; !seen {
			order = append(order, email)
		}
		invites[email] = invite{parseTime(created), parseTime(expires)}
	}
	if err := errors.Join(rows.Close(), rows.Err()); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT u.id,u.email,m.role,u.status,m.created_at FROM household_members m JOIN users u ON u.id=m.user_id WHERE m.household_id=? ORDER BY CASE m.role WHEN 'owner' THEN 0 ELSE 1 END, m.created_at`, householdID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now().UTC()
	var list []Access
	for rows.Next() {
		var entry Access
		var status, joined string
		if err := rows.Scan(&entry.UserID, &entry.Email, &entry.Role, &status, &joined); err != nil {
			return nil, err
		}
		entry.Since = parseTime(joined)
		switch status {
		case "active":
			entry.State = "active"
		case "pending":
			entry.State = "pending"
		default:
			entry.State = "removed"
		}
		if open, ok := invites[strings.ToLower(entry.Email)]; ok {
			if entry.State != "active" {
				entry.State, entry.Since, entry.ExpiresAt = inviteState(open.expires, now), open.created, open.expires
			}
			delete(invites, strings.ToLower(entry.Email))
		}
		list = append(list, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, email := range order {
		if open, ok := invites[email]; ok {
			list = append(list, Access{Email: email, Role: "invited", State: inviteState(open.expires, now), Since: open.created, ExpiresAt: open.expires})
		}
	}
	return list, nil
}

func inviteState(expires, now time.Time) string {
	if now.Before(expires) {
		return "invited"
	}
	return "expired"
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

// CancelInvitation withdraws every open invitation to email. The invited
// account is closed again unless it is an active member.
func (s *Service) CancelInvitation(ctx context.Context, scope policy.ActorScope, invitedEmail string) error {
	tx, email, err := s.ownerTx(ctx, scope, invitedEmail)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.timestamp()
	result, err := tx.ExecContext(ctx, `UPDATE invitations SET revoked_at=? WHERE household_id=? AND invited_email=? AND used_at IS NULL AND revoked_at IS NULL`, now, scope.HouseholdID, email)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNoInvitation
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET status='disabled',disabled_at=?,updated_at=? WHERE email=? AND status='pending' AND password_hash='' AND id NOT IN (SELECT user_id FROM household_members WHERE role='owner')`, now, now, email); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET revoked_at=? WHERE revoked_at IS NULL AND user_id IN (SELECT id FROM users WHERE email=? AND status='disabled')`, now, email); err != nil {
		return err
	}
	return tx.Commit()
}

// RemovePartner ends the partner's access at once: sessions, reset links and
// invitations are revoked and the password is cleared. Their records and
// their place in the household stay, so the owner can invite them again.
func (s *Service) RemovePartner(ctx context.Context, scope policy.ActorScope, partnerEmail string) error {
	tx, email, err := s.ownerTx(ctx, scope, partnerEmail)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID, status string
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.status FROM household_members m JOIN users u ON u.id=m.user_id WHERE m.household_id=? AND m.role='adult' AND u.email=?`, scope.HouseholdID, email).Scan(&userID, &status)
	if err != nil || status == "disabled" {
		return ErrNotPartner
	}
	now := s.timestamp()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET status='disabled',password_hash='',disabled_at=?,updated_at=? WHERE id=?`, now, now, userID); err != nil {
		return err
	}
	if err := revokeUserTx(ctx, tx, userID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ownerTx(ctx context.Context, scope policy.ActorScope, raw string) (*sql.Tx, string, error) {
	if !scope.Valid() || scope.Role != "owner" {
		return nil, "", ErrNotOwner
	}
	email, err := NormalizeEmail(raw)
	if err != nil {
		return nil, "", ErrInviteInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	var owner, status string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(owner_user_id,''),status FROM households WHERE id=?`, scope.HouseholdID).Scan(&owner, &status); err != nil || owner != scope.ActorID || status != "active" {
		tx.Rollback()
		return nil, "", ErrNotOwner
	}
	return tx, email, nil
}

// AttachActivationTx is deliberately transaction-local: password activation
// and membership creation either both commit or neither does.
func (s *Service) AttachActivationTx(ctx context.Context, tx *sql.Tx, userID, email, invite string) error {
	now := s.timestamp()
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT household_id FROM household_members WHERE user_id=?`, userID).Scan(&existing)
	if err == nil {
		// A returning member (a re-invited partner, or anyone after a
		// restore) keeps their place; any open invitation is now spent.
		if invite != "" {
			var invited string
			if err := tx.QueryRowContext(ctx, `SELECT household_id FROM invitations WHERE token_hash=?`, tokenHash(invite)).Scan(&invited); err != nil || invited != existing {
				return ErrAlreadyBound
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE invitations SET used_at=? WHERE household_id=? AND invited_email=? AND used_at IS NULL AND revoked_at IS NULL`, now, existing, email)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if invite != "" {
		return s.acceptInvitationTx(ctx, tx, userID, email, invite, now)
	}
	// Without an invitation only the configured owner may start the one
	// household this install keeps.
	if s.ownerEmail == "" || !strings.EqualFold(email, s.ownerEmail) {
		return ErrNotOwnerEmail
	}
	var households int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM households WHERE id NOT IN (SELECT household_id FROM demo_households)`).Scan(&households); err != nil {
		return err
	}
	if households > 0 {
		return ErrNotOwnerEmail
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
	now := s.timestamp()
	var userID, userStatus, bound string
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.status,COALESCE(m.household_id,'') FROM users u LEFT JOIN household_members m ON m.user_id=u.id WHERE u.email=?`, email).Scan(&userID, &userStatus, &bound)
	if errors.Is(err, sql.ErrNoRows) {
		// The operator names the new owner directly; they choose a password
		// through the usual reset link.
		userID, userStatus = newID(), "pending"
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,email,status,created_at,updated_at) VALUES(?,?,'pending',?,?)`, userID, email, now, now); err != nil {
			return err
		}
	} else if err != nil || (bound != "" && bound != householdID) {
		return ErrRecovery
	} else if userStatus == "disabled" {
		// The operator chose this person explicitly. They come back pending
		// and must choose a new password through a reset link.
		userStatus = "pending"
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status='pending',password_hash='',disabled_at=NULL,updated_at=? WHERE id=?`, now, userID); err != nil {
			return err
		}
	}
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
