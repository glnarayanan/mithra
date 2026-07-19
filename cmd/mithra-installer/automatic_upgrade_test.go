package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/glnarayanan/mithra/internal/installer"
)

func TestPrepareAutomaticUpgradeVerifiesDownloadedRelease(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	application := []byte("application-v1.2.1")
	candidate := []byte("installer-v1.2.1")
	applicationDigest := sha256.Sum256(application)
	candidateDigest := sha256.Sum256(candidate)
	manifest, err := installer.CanonicalManifest(installer.ReleaseManifest{Version: "v1.2.1", Artifacts: map[string]installer.ReleaseArtifact{
		"mithra-linux-amd64":           {SHA256: hex.EncodeToString(applicationDigest[:]), Size: int64(len(application))},
		"mithra-installer-linux-amd64": {SHA256: hex.EncodeToString(candidateDigest[:]), Size: int64(len(candidate))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifest)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "RELEASE-MANIFEST":
			_, _ = w.Write(manifest)
		case "RELEASE-MANIFEST.sig":
			_, _ = w.Write(signature)
		case "mithra-linux-amd64":
			_, _ = w.Write(application)
		case "mithra-installer-linux-amd64":
			_, _ = w.Write(candidate)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stage, err := prepareAutomaticUpgrade(context.Background(), server.Client(), server.URL, "amd64", base64.RawStdEncoding.EncodeToString(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	defer stage.cleanup()
	if stage.Version != "v1.2.1" {
		t.Fatalf("version = %q", stage.Version)
	}
	for path, expected := range map[string][]byte{stage.Application: application, stage.Candidate: candidate, stage.Manifest: manifest, stage.Signature: signature} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != string(expected) {
			t.Fatalf("staged %s mismatch: %v", filepath.Base(path), err)
		}
	}
	info, err := os.Stat(stage.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("candidate mode = %v", info.Mode().Perm())
	}
}

func TestPrepareAutomaticUpgradeRejectsTamperedArtifactAndHTTP(t *testing.T) {
	if _, err := prepareAutomaticUpgrade(context.Background(), http.DefaultClient, "http://example.com/releases", "amd64", "invalid"); err == nil {
		t.Fatal("HTTP release source was accepted")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	application := []byte("application")
	candidate := []byte("installer")
	applicationDigest := sha256.Sum256(application)
	candidateDigest := sha256.Sum256(candidate)
	manifest, err := installer.CanonicalManifest(installer.ReleaseManifest{Version: "v1.2.1", Artifacts: map[string]installer.ReleaseArtifact{
		"mithra-linux-amd64":           {SHA256: hex.EncodeToString(applicationDigest[:]), Size: int64(len(application))},
		"mithra-installer-linux-amd64": {SHA256: hex.EncodeToString(candidateDigest[:]), Size: int64(len(candidate))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifest)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "RELEASE-MANIFEST":
			_, _ = w.Write(manifest)
		case "RELEASE-MANIFEST.sig":
			_, _ = w.Write(signature)
		case "mithra-linux-amd64":
			_, _ = w.Write([]byte("tampered!!"))
		case "mithra-installer-linux-amd64":
			_, _ = w.Write(candidate)
		}
	}))
	defer server.Close()
	if _, err := prepareAutomaticUpgrade(context.Background(), server.Client(), server.URL, "amd64", base64.RawStdEncoding.EncodeToString(publicKey)); err == nil {
		t.Fatal("tampered application was accepted")
	}
}
