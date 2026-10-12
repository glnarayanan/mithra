package database

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

// KeyBytes is the raw SQLCipher page-key length derived from the master key.
const KeyBytes = 32

var (
	// ErrEncryptionUnavailable means this binary links plain SQLite rather
	// than SQLCipher, so a key would be silently ignored.
	ErrEncryptionUnavailable = errors.New("SQLite build does not support database encryption")
	// ErrPlaintextDatabase means an existing database file is unencrypted.
	ErrPlaintextDatabase = errors.New("SQLite database is not encrypted")
	// ErrDatabaseKey means the database could not be read with the derived key.
	ErrDatabaseKey = errors.New("SQLite database key does not match")
)

var plaintextHeader = []byte("SQLite format 3\x00")

var encryptionProbe = sync.OnceValue(func() bool {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return false
	}
	defer db.Close()
	var version sql.NullString
	if err := db.QueryRow("PRAGMA cipher_version").Scan(&version); err != nil {
		return false
	}
	return strings.TrimSpace(version.String) != ""
})

// EncryptionAvailable reports whether this binary is linked against SQLCipher.
func EncryptionAvailable() bool { return encryptionProbe() }

// DeriveKey derives the SQLCipher page key from the Mithra master key with a
// purpose label distinct from every other master-key derivation.
func DeriveKey(masterKey []byte) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("database key requires the 32-byte master key")
	}
	return hkdf.Key(sha256.New, masterKey, []byte("mithra/sqlite/v1"), "mithra/sqlite/page-key", KeyBytes)
}

// IsPlaintext reports whether path holds a non-empty unencrypted SQLite
// database. A missing or empty file is not plaintext: it will be created with
// whatever key the caller supplies.
func IsPlaintext(path string) (bool, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	header := make([]byte, len(plaintextHeader))
	n, err := io.ReadFull(file, header)
	if n == 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
		return false, nil
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return false, err
	}
	return bytes.Equal(header[:n], plaintextHeader), nil
}

// KeyForFile returns the page key to open path with, or nil when the file must
// be opened as plain SQLite: the binary lacks SQLCipher, or the file is an
// existing unencrypted database. New and encrypted files always get a key.
func KeyForFile(path string, masterKey []byte) ([]byte, error) {
	if !EncryptionAvailable() {
		return nil, nil
	}
	plaintext, err := IsPlaintext(path)
	if err != nil {
		return nil, err
	}
	if plaintext {
		return nil, nil
	}
	return DeriveKey(masterKey)
}

// KeyParameter is the SQLCipher URI parameter for a raw page key. The x'..'
// form skips SQLCipher's passphrase derivation because the key is already
// uniformly random. It is empty when key is nil.
func KeyParameter(key []byte) string {
	if key == nil {
		return ""
	}
	return "key=x%27" + strings.ToUpper(hex.EncodeToString(key)) + "%27"
}

// FileDSN builds a "file:" DSN in the shape the maintenance code has always
// used, adding the page key when one is supplied.
func FileDSN(path, parameters string, key []byte) string {
	dsn := "file:" + path
	separator := "?"
	for _, value := range []string{parameters, KeyParameter(key)} {
		if value != "" {
			dsn += separator + value
			separator = "&"
		}
	}
	return dsn
}

// verifyEncryption proves the key opens the file. The caller has already
// checked EncryptionAvailable, so any failure here is a key mismatch (or a
// file that is not a database at all); a wrong key fails on the first read.
func verifyEncryption(ctx context.Context, db *sql.DB, key []byte) error {
	if key == nil {
		return nil
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&count); err != nil {
		return ErrDatabaseKey
	}
	return nil
}
