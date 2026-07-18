package secrets

import (
	"bytes"
	"errors"
	"testing"
)

func TestEnvelopeRoundTripIsRandomAndPurposeBound(t *testing.T) {
	master := bytes.Repeat([]byte{0x41}, MasterKeyBytes)
	context := []byte("household:home/settings:openai")
	settings, err := New(master, Settings)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("provider-secret")
	first, err := settings.Seal(plaintext, context)
	if err != nil {
		t.Fatal(err)
	}
	second, err := settings.Seal(plaintext, context)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, plaintext) || bytes.Contains(second, plaintext) {
		t.Fatal("envelopes were deterministic or exposed plaintext")
	}
	opened, err := settings.Open(first, context)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("open = %q, %v", opened, err)
	}

	sources, err := New(master, Sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sources.Open(first, context); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong purpose error = %v", err)
	}
	wrongKey, err := New(bytes.Repeat([]byte{0x42}, MasterKeyBytes), Settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.Open(first, context); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong key error = %v", err)
	}
	if _, err := settings.Open(first, []byte("household:other/settings:openai")); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong context error = %v", err)
	}
}

func TestEnvelopeRejectsMalformedAndTamperedInput(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{0x51}, MasterKeyBytes), Backups)
	if err != nil {
		t.Fatal(err)
	}
	context := []byte("backup:generation-7")
	envelope, err := box.Seal([]byte("database and files"), context)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"empty":         nil,
		"truncated":     append([]byte(nil), envelope[:len(envelope)-1]...),
		"wrong magic":   append([]byte(nil), envelope...),
		"wrong version": append([]byte(nil), envelope...),
		"tampered":      append([]byte(nil), envelope...),
	}
	cases["wrong magic"][0] ^= 0xff
	cases["wrong version"][4]++
	cases["tampered"][len(cases["tampered"])-1] ^= 0x01
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := box.Open(candidate, context); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnvelopeValidatesKeyPurposePlaintextAndContextBounds(t *testing.T) {
	for _, size := range []int{0, MasterKeyBytes - 1, MasterKeyBytes + 1} {
		if _, err := New(make([]byte, size), Settings); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("key size %d error = %v", size, err)
		}
	}
	if _, err := New(make([]byte, MasterKeyBytes), Purpose("custom")); !errors.Is(err, ErrInvalidPurpose) {
		t.Fatalf("purpose error = %v", err)
	}
	box, err := New(make([]byte, MasterKeyBytes), Sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Seal(nil, []byte("source:1")); !errors.Is(err, ErrInvalidPlaintext) {
		t.Fatalf("empty plaintext error = %v", err)
	}
	if _, err := box.Seal(make([]byte, MaxPlaintextBytes+1), []byte("source:1")); !errors.Is(err, ErrInvalidPlaintext) {
		t.Fatalf("oversized plaintext error = %v", err)
	}
	if _, err := box.Seal([]byte("value"), nil); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("empty context error = %v", err)
	}
	if _, err := box.Seal([]byte("value"), make([]byte, maxContextBytes+1)); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("oversized context error = %v", err)
	}
}
