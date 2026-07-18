package imports

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeletionJournalAuthenticatesAndFsyncsOpaqueIntents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deletion.journal")
	journal, err := NewDeletionJournal(path, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	intent := DeletionIntent{ID: strings.Repeat("a", 32), HouseholdID: "home", OwnerID: "owner", SourceID: "source-secret", Digest: strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	if err := journal.Append(intent); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("source-secret")) || bytes.Contains(raw, []byte("home")) {
		t.Fatalf("journal leaked plaintext: %q", raw)
	}
	read, err := journal.ReadAll()
	if err != nil || len(read) != 1 || read[0].SourceID != intent.SourceID {
		t.Fatalf("read = %#v, %v", read, err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ReadAll(); err == nil {
		t.Fatal("tampered journal accepted")
	}
}
