// Package secrets provides purpose-bound authenticated encryption for Mithra
// state. Callers supply the master key; this package never reads credentials.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	MasterKeyBytes    = 32
	MaxPlaintextBytes = 16 << 20
	maxContextBytes   = 4 << 10
	envelopeVersion   = byte(1)
	headerBytes       = 6
)

var (
	ErrInvalidKey       = errors.New("invalid master key")
	ErrInvalidPurpose   = errors.New("invalid encryption purpose")
	ErrInvalidPlaintext = errors.New("invalid plaintext")
	ErrInvalidContext   = errors.New("invalid encryption context")
	ErrInvalidEnvelope  = errors.New("invalid encrypted envelope")
)

type Purpose string

const (
	Settings Purpose = "settings"
	Sources  Purpose = "sources"
	Backups  Purpose = "backups"
)

var envelopeMagic = [4]byte{'M', 'T', 'H', 'R'}

// Box encrypts one purpose only. New derives and retains only the purpose key,
// so a settings Box cannot decrypt source or backup ciphertext.
type Box struct {
	aead      cipher.AEAD
	purposeID byte
}

func New(masterKey []byte, purpose Purpose) (*Box, error) {
	if len(masterKey) != MasterKeyBytes {
		return nil, ErrInvalidKey
	}
	purposeID, ok := purposeIdentifier(purpose)
	if !ok {
		return nil, ErrInvalidPurpose
	}
	derived, err := hkdf.Key(sha256.New, masterKey, []byte("mithra/envelope/v1"), "mithra/envelope/"+string(purpose), MasterKeyBytes)
	if err != nil {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(derived)
	clear(derived)
	if err != nil {
		return nil, ErrInvalidKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return &Box{aead: aead, purposeID: purposeID}, nil
}

func (b *Box) Seal(plaintext, context []byte) ([]byte, error) {
	if b == nil || b.aead == nil || len(plaintext) == 0 || len(plaintext) > MaxPlaintextBytes {
		return nil, ErrInvalidPlaintext
	}
	aad, err := b.additionalData(context)
	if err != nil {
		return nil, err
	}
	header := aad[:headerBytes]
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrInvalidEnvelope
	}
	envelope := make([]byte, 0, len(header)+len(nonce)+len(plaintext)+b.aead.Overhead())
	envelope = append(envelope, header...)
	envelope = append(envelope, nonce...)
	envelope = b.aead.Seal(envelope, nonce, plaintext, aad)
	return envelope, nil
}

func (b *Box) Open(envelope, context []byte) ([]byte, error) {
	if b == nil || b.aead == nil {
		return nil, ErrInvalidEnvelope
	}
	minimum := headerBytes + b.aead.NonceSize() + b.aead.Overhead()
	maximum := headerBytes + b.aead.NonceSize() + MaxPlaintextBytes + b.aead.Overhead()
	if len(envelope) < minimum || len(envelope) > maximum || !validHeader(envelope, b.purposeID) {
		return nil, ErrInvalidEnvelope
	}
	aad, err := b.additionalData(context)
	if err != nil {
		return nil, err
	}
	nonceStart := headerBytes
	nonceEnd := nonceStart + b.aead.NonceSize()
	plaintext, err := b.aead.Open(nil, envelope[nonceStart:nonceEnd], envelope[nonceEnd:], aad)
	if err != nil || len(plaintext) == 0 || len(plaintext) > MaxPlaintextBytes {
		return nil, ErrInvalidEnvelope
	}
	return plaintext, nil
}

func (b *Box) additionalData(context []byte) ([]byte, error) {
	if len(context) == 0 || len(context) > maxContextBytes {
		return nil, ErrInvalidContext
	}
	aad := make([]byte, headerBytes+4+len(context))
	copy(aad[:4], envelopeMagic[:])
	aad[4] = envelopeVersion
	aad[5] = b.purposeID
	binary.BigEndian.PutUint32(aad[headerBytes:headerBytes+4], uint32(len(context)))
	copy(aad[headerBytes+4:], context)
	return aad, nil
}

func validHeader(envelope []byte, purposeID byte) bool {
	return len(envelope) >= headerBytes &&
		envelope[0] == envelopeMagic[0] && envelope[1] == envelopeMagic[1] &&
		envelope[2] == envelopeMagic[2] && envelope[3] == envelopeMagic[3] &&
		envelope[4] == envelopeVersion && envelope[5] == purposeID
}

func purposeIdentifier(purpose Purpose) (byte, bool) {
	switch purpose {
	case Settings:
		return 1, true
	case Sources:
		return 2, true
	case Backups:
		return 3, true
	default:
		return 0, false
	}
}
