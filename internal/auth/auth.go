// Package auth owns allowlisted password bootstrap and cookie-session state.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/glnarayanan/mithra/internal/household"
	"github.com/glnarayanan/mithra/internal/policy"
)

const (
	minPasswordBytes = 12
	maxPasswordBytes = 128
	resetLifetime    = time.Hour
	sessionLifetime  = 14 * 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("credentials are invalid")
	ErrInvalidReset       = errors.New("reset link is invalid or expired")
	ErrPassword           = errors.New("password does not meet requirements")
	ErrThrottled          = errors.New("try again later")
	ErrSession            = errors.New("session is invalid")
	ErrCSRF               = errors.New("request verification failed")
)

type Config struct {
	Now           func() time.Time
	Token         func() (string, error)
	PasswordSlots int
}

type Service struct {
	db         *sql.DB
	now        func() time.Time
	token      func() (string, error)
	passwords  chan struct{}
	households *household.Service
}

type Session struct {
	Cookie    string
	CSRF      string
	ExpiresAt time.Time
	Scope     policy.ActorScope
}

type ResetDelivery struct {
	Email, Token string
	ExpiresAt    time.Time
}

func New(db *sql.DB, cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Token == nil {
		cfg.Token = randomToken
	}
	if cfg.PasswordSlots < 1 {
		cfg.PasswordSlots = 2
	}
	return &Service{db: db, now: cfg.Now, token: cfg.Token, passwords: make(chan struct{}, cfg.PasswordSlots), households: household.New(db, household.Config{Now: cfg.Now, Token: cfg.Token})}
}

func (s *Service) SynchronizeAllowlist(ctx context.Context, emails []string) error {
	return s.households.SyncAllowlist(ctx, emails)
}

// RequestPasswordReset returns no delivery for unknown or disabled addresses;
// HTTP callers always render the same acknowledgement either way.
func (s *Service) RequestPasswordReset(ctx context.Context, email, throttleKey string) (*ResetDelivery, error) {
	if ok, err := s.Allow(ctx, "reset:"+throttleKey, 5, 15*time.Minute); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrThrottled
	}
	normalized, err := household.NormalizeEmail(email)
	if err != nil {
		return nil, nil
	}
	var userID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email=? AND status IN ('pending','active')`, normalized).Scan(&userID); err != nil {
		return nil, nil
	}
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	now := s.timestamp()
	expires := s.now().UTC().Add(resetLifetime)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET revoked_at=? WHERE user_id=? AND used_at IS NULL AND revoked_at IS NULL`, now, userID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO password_reset_tokens(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, tokenHash(token), userID, expires.Format(time.RFC3339Nano), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ResetDelivery{Email: normalized, Token: token, ExpiresAt: expires}, nil
}

// SetPassword consumes one reset token or a first-use invitation, revokes prior
// sessions, and creates a fresh cookie-only session. When both are present, the
// reset identifies the account and the invitation only controls membership.
func (s *Service) SetPassword(ctx context.Context, resetToken, password, invitationToken string) (Session, error) {
	if err := validatePassword(password); err != nil {
		return Session{}, err
	}
	userID, err := s.passwordSubject(ctx, resetToken, invitationToken)
	if err != nil {
		return Session{}, ErrInvalidReset
	}
	hash, err := s.hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	cookie, err := s.token()
	if err != nil {
		return Session{}, err
	}
	csrf, err := s.token()
	if err != nil {
		return Session{}, err
	}
	now := s.timestamp()
	sessionExpires := s.now().UTC().Add(sessionLifetime)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var email string
	if resetToken != "" {
		var currentID, currentStatus, currentExpires, currentUsed, currentRevoked string
		err = tx.QueryRowContext(ctx, `SELECT u.id,u.email,u.status,r.expires_at,COALESCE(r.used_at,''),COALESCE(r.revoked_at,'') FROM password_reset_tokens r JOIN users u ON u.id=r.user_id WHERE r.token_hash=?`, tokenHash(resetToken)).Scan(&currentID, &email, &currentStatus, &currentExpires, &currentUsed, &currentRevoked)
		if err != nil || currentID != userID || currentStatus == "disabled" || currentUsed != "" || currentRevoked != "" || !before(s.now(), currentExpires) {
			return Session{}, ErrInvalidReset
		}
	} else {
		var currentStatus, currentHash, currentExpires, currentUsed, currentRevoked string
		err = tx.QueryRowContext(ctx, `SELECT u.email,u.status,u.password_hash,i.expires_at,COALESCE(i.used_at,''),COALESCE(i.revoked_at,'') FROM invitations i JOIN users u ON u.email=i.invited_email WHERE i.token_hash=? AND u.id=?`, tokenHash(invitationToken), userID).Scan(&email, &currentStatus, &currentHash, &currentExpires, &currentUsed, &currentRevoked)
		if err != nil || currentStatus == "disabled" || currentHash != "" || currentUsed != "" || currentRevoked != "" || !before(s.now(), currentExpires) {
			return Session{}, ErrInvalidReset
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,status='active',updated_at=? WHERE id=?`, hash, now, userID); err != nil {
		return Session{}, err
	}
	if err := s.households.AttachActivationTx(ctx, tx, userID, email, invitationToken); err != nil {
		return Session{}, err
	}
	if resetToken != "" {
		result, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL AND revoked_at IS NULL`, now, tokenHash(resetToken))
		if err != nil {
			return Session{}, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return Session{}, ErrInvalidReset
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID); err != nil {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sessions(token_hash,csrf_hash,user_id,expires_at,created_at) VALUES(?,?,?,?,?)`, tokenHash(cookie), tokenHash(csrf), userID, sessionExpires.Format(time.RFC3339Nano), now); err != nil {
		return Session{}, err
	}
	scope, err := scopeForUserTx(ctx, tx, userID)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{Cookie: cookie, CSRF: csrf, ExpiresAt: sessionExpires, Scope: scope}, nil
}

func (s *Service) passwordSubject(ctx context.Context, resetToken, invitationToken string) (string, error) {
	if resetToken != "" {
		var userID, status, expires, used, revoked string
		err := s.db.QueryRowContext(ctx, `SELECT u.id,u.status,r.expires_at,COALESCE(r.used_at,''),COALESCE(r.revoked_at,'') FROM password_reset_tokens r JOIN users u ON u.id=r.user_id WHERE r.token_hash=?`, tokenHash(resetToken)).Scan(&userID, &status, &expires, &used, &revoked)
		if err == nil && status != "disabled" && used == "" && revoked == "" && before(s.now(), expires) {
			return userID, nil
		}
		return "", ErrInvalidReset
	}
	if invitationToken != "" {
		var userID, status, passwordHash, expires, used, revoked, householdStatus string
		err := s.db.QueryRowContext(ctx, `SELECT u.id,u.status,u.password_hash,i.expires_at,COALESCE(i.used_at,''),COALESCE(i.revoked_at,''),h.status FROM invitations i JOIN users u ON u.email=i.invited_email JOIN households h ON h.id=i.household_id WHERE i.token_hash=?`, tokenHash(invitationToken)).Scan(&userID, &status, &passwordHash, &expires, &used, &revoked, &householdStatus)
		if err == nil && status != "disabled" && passwordHash == "" && used == "" && revoked == "" && householdStatus == "active" && before(s.now(), expires) {
			return userID, nil
		}
	}
	return "", ErrInvalidReset
}

func (s *Service) Login(ctx context.Context, email, password, throttleKey string) (Session, error) {
	if ok, err := s.Allow(ctx, "login:"+throttleKey, 10, 15*time.Minute); err != nil {
		return Session{}, err
	} else if !ok {
		return Session{}, ErrThrottled
	}
	normalized, err := household.NormalizeEmail(email)
	if err != nil {
		return Session{}, ErrInvalidCredentials
	}
	var userID, encoded, status string
	err = s.db.QueryRowContext(ctx, `SELECT id,password_hash,status FROM users WHERE email=?`, normalized).Scan(&userID, &encoded, &status)
	if err != nil || status != "active" {
		s.verifyEncoded(dummyPasswordHash, password)
		return Session{}, ErrInvalidCredentials
	}
	if !s.verifyPassword(encoded, password) {
		return Session{}, ErrInvalidCredentials
	}
	return s.newSession(ctx, userID, "")
}

func (s *Service) Authenticate(ctx context.Context, cookie string) (policy.ActorScope, error) {
	var userID, expires, revoked, status, householdID, role, householdStatus string
	err := s.db.QueryRowContext(ctx, `SELECT s.user_id,s.expires_at,COALESCE(s.revoked_at,''),u.status,m.household_id,m.role,h.status FROM browser_sessions s JOIN users u ON u.id=s.user_id JOIN household_members m ON m.user_id=u.id JOIN households h ON h.id=m.household_id WHERE s.token_hash=?`, tokenHash(cookie)).Scan(&userID, &expires, &revoked, &status, &householdID, &role, &householdStatus)
	if err != nil || revoked != "" || status != "active" || householdStatus != "active" || !before(s.now(), expires) {
		return policy.ActorScope{}, ErrSession
	}
	return policy.ActorScope{ActorID: userID, HouseholdID: householdID, Role: role}, nil
}

func (s *Service) VerifyCSRF(ctx context.Context, cookie, csrf string) error {
	if _, err := s.Authenticate(ctx, cookie); err != nil {
		return ErrCSRF
	}
	var expected string
	if err := s.db.QueryRowContext(ctx, `SELECT csrf_hash FROM browser_sessions WHERE token_hash=? AND revoked_at IS NULL`, tokenHash(cookie)).Scan(&expected); err != nil {
		return ErrCSRF
	}
	actual := tokenHash(csrf)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return ErrCSRF
	}
	return nil
}

// RotateSession revokes the presented session before issuing its replacement.
func (s *Service) RotateSession(ctx context.Context, cookie string) (Session, error) {
	scope, err := s.Authenticate(ctx, cookie)
	if err != nil {
		return Session{}, err
	}
	return s.newSession(ctx, scope.ActorID, tokenHash(cookie))
}

// RevokeSession makes logout effective immediately. Unknown cookies are
// intentionally treated as already logged out.
func (s *Service) RevokeSession(ctx context.Context, cookie string) error {
	if strings.TrimSpace(cookie) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, s.timestamp(), tokenHash(cookie))
	return err
}
func (s *Service) CreateInvitation(ctx context.Context, scope policy.ActorScope, email string, lifetime time.Duration) (household.Invitation, error) {
	return s.households.CreateInvitation(ctx, scope, email, lifetime)
}
func (s *Service) RecoverOwner(ctx context.Context, householdID, email string) error {
	return s.households.RecoverOwner(ctx, householdID, email)
}
func (s *Service) Members(ctx context.Context, scope policy.ActorScope) ([]household.Member, error) {
	if !scope.Valid() {
		return nil, ErrSession
	}
	return s.households.Members(ctx, scope.HouseholdID)
}

// Allow is the minimal SQLite fixed-window throttle used before expensive
// Argon2 work. Callers choose a non-sensitive opaque key such as client IP.
func (s *Service) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if strings.TrimSpace(key) == "" || limit < 1 || window <= 0 {
		return false, errors.New("invalid throttle")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	var started string
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT window_started_at,attempts FROM auth_throttles WHERE throttle_key=?`, key).Scan(&started, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO auth_throttles(throttle_key,window_started_at,attempts,updated_at) VALUES(?,?,1,?)`, key, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	began, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return false, err
	}
	allowed := now.Sub(began) >= window || attempts < limit
	if now.Sub(began) >= window {
		_, err = tx.ExecContext(ctx, `UPDATE auth_throttles SET window_started_at=?,attempts=1,updated_at=? WHERE throttle_key=?`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), key)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE auth_throttles SET attempts=attempts+1,updated_at=? WHERE throttle_key=?`, now.Format(time.RFC3339Nano), key)
	}
	if err != nil {
		return false, err
	}
	return allowed, tx.Commit()
}

func (s *Service) newSession(ctx context.Context, userID, rotatedFrom string) (Session, error) {
	cookie, err := s.token()
	if err != nil {
		return Session{}, err
	}
	csrf, err := s.token()
	if err != nil {
		return Session{}, err
	}
	expires := s.now().UTC().Add(sessionLifetime)
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	if rotatedFrom != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL`, now, rotatedFrom); err != nil {
			return Session{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sessions(token_hash,csrf_hash,user_id,expires_at,created_at,rotated_from_hash) VALUES(?,?,?,?,?,?)`, tokenHash(cookie), tokenHash(csrf), userID, expires.Format(time.RFC3339Nano), now, rotatedFrom); err != nil {
		return Session{}, err
	}
	scope, err := scopeForUserTx(ctx, tx, userID)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{Cookie: cookie, CSRF: csrf, ExpiresAt: expires, Scope: scope}, nil
}
func scopeForUserTx(ctx context.Context, tx *sql.Tx, userID string) (policy.ActorScope, error) {
	var householdID, role string
	err := tx.QueryRowContext(ctx, `SELECT m.household_id,m.role FROM household_members m JOIN households h ON h.id=m.household_id WHERE m.user_id=? AND h.status='active'`, userID).Scan(&householdID, &role)
	if err != nil {
		return policy.ActorScope{}, ErrSession
	}
	return policy.ActorScope{ActorID: userID, HouseholdID: householdID, Role: role}, nil
}
func (s *Service) hashPassword(password string) (string, error) {
	s.passwords <- struct{}{}
	defer func() { <-s.passwords }()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return "$argon2id$v=19$m=65536,t=3,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}
func (s *Service) verifyPassword(encoded, password string) bool {
	if err := validatePassword(password); err != nil {
		return false
	}
	return s.verifyEncoded(encoded, password)
}

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (s *Service) verifyEncoded(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=1" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != 32 {
		return false
	}
	s.passwords <- struct{}{}
	defer func() { <-s.passwords }()
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
	return subtle.ConstantTimeCompare(expected, actual) == 1
}
func validatePassword(password string) error {
	if len(password) < minPasswordBytes || len(password) > maxPasswordBytes {
		return ErrPassword
	}
	return nil
}
func (s *Service) timestamp() string { return s.now().UTC().Format(time.RFC3339Nano) }
func before(now time.Time, value string) bool {
	at, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && now.UTC().Before(at)
}
func tokenHash(token string) string {
	// Reset, session, invitation, and CSRF tokens are 256-bit random
	// capabilities, not user passwords. A fast digest is safe for indexed lookup.
	// lgtm[go/weak-sensitive-data-hashing]
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}
func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
