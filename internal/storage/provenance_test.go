package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/policy"
)

func TestProvenanceAndSearchConstraintsPreservePrivacyAndIntegrity(t *testing.T) {
	service, db, owner, _ := storageFixture(t)
	source, err := service.Store(context.Background(), owner, []byte("supported fact"), Metadata{Family: "text", Version: 1, LocatorKind: "source", LocatorValue: "capture-1"})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	insertEvidence := func(id, household, actor, visibility, family string) error {
		_, err := db.Exec(`INSERT INTO evidence_links(id,record_family,record_id,household_id,owner_user_id,visibility,source_id,source_family,source_version,locator_kind,locator_value,created_at) VALUES(?,'finance','record-1',?,?,?,?,?,1,'source','capture-1',?)`, id, household, actor, visibility, source.ID, family, stamp)
		return err
	}
	if err := insertEvidence("valid", owner.HouseholdID, owner.ActorID, string(policy.Personal), "text"); err != nil {
		t.Fatalf("valid evidence: %v", err)
	}
	if err := insertEvidence("broader", owner.HouseholdID, owner.ActorID, string(policy.Shared), "text"); err == nil {
		t.Fatal("shared evidence cited a personal source")
	}
	if err := insertEvidence("wrong-family", owner.HouseholdID, owner.ActorID, string(policy.Personal), "pdf"); err == nil {
		t.Fatal("evidence accepted the wrong source family")
	}
	if err := insertEvidence("foreign", "foreign-home", owner.ActorID, string(policy.Personal), "text"); err == nil {
		t.Fatal("evidence crossed household scope")
	}
	if _, err := db.Exec(`UPDATE evidence_links SET visibility='shared' WHERE id='valid'`); err == nil {
		t.Fatal("evidence scope changed through UPDATE")
	}

	if _, err := db.Exec(`INSERT INTO search_entries(record_family,record_id,household_id,owner_user_id,visibility,content) VALUES('finance','record-1',?,?,?,'annual contribution')`, owner.HouseholdID, owner.ActorID, policy.Personal); err != nil {
		t.Fatal(err)
	}
	var matches int
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_entries_fts WHERE search_entries_fts MATCH 'contribution'`).Scan(&matches); err != nil || matches != 1 {
		t.Fatalf("search matches = %d, %v", matches, err)
	}
	if _, err := db.Exec(`UPDATE search_entries SET visibility='shared' WHERE record_family='finance' AND record_id='record-1'`); err == nil {
		t.Fatal("search scope changed through UPDATE")
	}
	if _, err := db.Exec(`UPDATE search_entries SET rowid=rowid+100 WHERE record_family='finance' AND record_id='record-1'`); err == nil {
		t.Fatal("search FTS identity changed through UPDATE")
	}
	if _, err := db.Exec(`UPDATE search_entries SET content='retirement contribution' WHERE record_family='finance' AND record_id='record-1'`); err != nil {
		t.Fatalf("content-only search update: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_entries_fts WHERE search_entries_fts MATCH 'retirement'`).Scan(&matches); err != nil || matches != 1 {
		t.Fatalf("updated search matches = %d, %v", matches, err)
	}
	if _, err := db.Exec(`DELETE FROM search_entries WHERE record_family='finance' AND record_id='record-1'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM search_entries_fts`).Scan(&matches); err != nil || matches != 0 {
		t.Fatalf("deleted search rows = %d, %v", matches, err)
	}
	if _, err := db.Exec(`INSERT INTO search_entries_fts(rowid,content) VALUES(999,'orphan')`); err != nil {
		t.Fatal(err)
	}
	if err := database.CheckReady(context.Background(), db); err == nil || !strings.Contains(err.Error(), "orphaned") {
		t.Fatalf("orphan readiness error = %v", err)
	}
}
