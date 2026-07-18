// Package policy centralizes server-derived household authorization.
package policy

import "errors"

type Visibility string

const (
	Personal Visibility = "personal"
	Shared   Visibility = "shared"
)

var (
	ErrUnauthorized = errors.New("not authorized")
	ErrConflict     = errors.New("stale version")
)

// ActorScope is server derived from a current authenticated session.
type ActorScope struct {
	ActorID     string
	HouseholdID string
	Role        string
}

type Resource struct {
	HouseholdID string
	OwnerID     string
	Visibility  Visibility
	Version     int64
}

func (s ActorScope) Valid() bool {
	return s.ActorID != "" && s.HouseholdID != "" && (s.Role == "owner" || s.Role == "adult")
}

func (s ActorScope) CanRead(resource Resource) bool {
	if !s.Valid() || resource.HouseholdID != s.HouseholdID {
		return false
	}
	return resource.Visibility == Shared || (resource.Visibility == Personal && resource.OwnerID == s.ActorID)
}

func (s ActorScope) CanMutate(resource Resource, expectedVersion int64) error {
	if !s.CanRead(resource) {
		return ErrUnauthorized
	}
	if expectedVersion != resource.Version {
		return ErrConflict
	}
	return nil
}

func PersonalDefault(value Visibility) Visibility {
	if value == Shared {
		return Shared
	}
	return Personal
}
