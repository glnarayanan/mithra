package imports

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/secrets"
)

type DeletionIntent struct {
	ID, HouseholdID, OwnerID, SourceID, Digest string
	CreatedAt                                  time.Time
}
type journalLine struct{ ID, Envelope string }
type DeletionJournal struct {
	path string
	box  *secrets.Box
}

func NewDeletionJournal(path string, masterKey []byte) (*DeletionJournal, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidInput
	}
	box, err := secrets.New(masterKey, secrets.Backups)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return &DeletionJournal{path: path, box: box}, nil
}
func (j *DeletionJournal) Append(intent DeletionIntent) error {
	if j == nil || j.box == nil || intent.ID == "" || intent.HouseholdID == "" || intent.OwnerID == "" || intent.SourceID == "" || len(intent.Digest) != 64 {
		return ErrInvalidInput
	}
	plain, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	envelope, err := j.box.Seal(plain, []byte("deletion-intent-v1\x00"+intent.ID))
	clear(plain)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(journalLine{ID: intent.ID, Envelope: base64.RawURLEncoding.EncodeToString(envelope)})
	line = append(line, '\n')
	file, err := os.OpenFile(j.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return err
	}
	return file.Sync()
}
func (j *DeletionJournal) ReadAll() ([]DeletionIntent, error) {
	if j == nil || j.box == nil {
		return nil, ErrInvalidInput
	}
	file, err := os.Open(j.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 64<<10)
	var out []DeletionIntent
	seen := map[string]bool{}
	for scanner.Scan() {
		var line journalLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.ID == "" || seen[line.ID] {
			return nil, errors.New("invalid deletion journal")
		}
		encoded, err := base64.RawURLEncoding.DecodeString(line.Envelope)
		if err != nil {
			return nil, err
		}
		plain, err := j.box.Open(encoded, []byte("deletion-intent-v1\x00"+line.ID))
		if err != nil {
			return nil, err
		}
		var intent DeletionIntent
		err = json.Unmarshal(plain, &intent)
		clear(plain)
		_, digestErr := hex.DecodeString(intent.Digest)
		if err != nil || intent.ID != line.ID || len(intent.Digest) != 64 || digestErr != nil || intent.Digest != strings.ToLower(intent.Digest) {
			return nil, errors.New("invalid deletion journal")
		}
		seen[line.ID] = true
		out = append(out, intent)
	}
	return out, scanner.Err()
}
