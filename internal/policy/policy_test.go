package policy

import "testing"

func TestPersonalDefaultAndFourPathAuthorization(t *testing.T) {
	scope := ActorScope{ActorID: "a1", HouseholdID: "h1", Role: "adult"}
	shared := Resource{HouseholdID: "h1", OwnerID: "a2", Visibility: Shared, Version: 4}
	personal := Resource{HouseholdID: "h1", OwnerID: "a1", Visibility: Personal, Version: 3}
	foreign := Resource{HouseholdID: "h2", OwnerID: "a1", Visibility: Shared, Version: 1}
	if PersonalDefault("") != Personal || PersonalDefault(Shared) != Shared {
		t.Fatal("visibility must default to personal")
	}
	if !scope.CanRead(shared) || !scope.CanRead(personal) || scope.CanRead(foreign) {
		t.Fatal("read matrix is not household/private safe")
	}
	if err := scope.CanMutate(shared, 4); err != nil {
		t.Fatalf("shared edit: %v", err)
	}
	if err := scope.CanMutate(shared, 3); err != ErrConflict {
		t.Fatalf("stale edit = %v, want conflict", err)
	}
	if err := scope.CanMutate(Resource{HouseholdID: "h1", OwnerID: "a2", Visibility: Personal, Version: 1}, 1); err != ErrUnauthorized {
		t.Fatalf("partner private edit = %v", err)
	}
}
