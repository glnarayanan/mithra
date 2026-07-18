package installer

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type ReleaseManifest struct {
	Version   string
	Artifacts map[string]ReleaseArtifact
}

type ReleaseArtifact struct {
	SHA256 string
	Size   int64
}

// CanonicalManifest permits one byte representation, so replacing both a
// binary and its checksum cannot bypass the detached publisher signature.
func CanonicalManifest(manifest ReleaseManifest) ([]byte, error) {
	version := strings.TrimSpace(manifest.Version)
	if version == "" || strings.ContainsAny(version, "\r\n\t ") || len(manifest.Artifacts) == 0 {
		return nil, errors.New("invalid release manifest")
	}
	names := make([]string, 0, len(manifest.Artifacts))
	for name, artifact := range manifest.Artifacts {
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\\\r\n\t ") || artifact.Size < 1 || len(artifact.SHA256) != 64 {
			return nil, errors.New("invalid release manifest")
		}
		if decoded, err := hex.DecodeString(artifact.SHA256); err != nil || hex.EncodeToString(decoded) != artifact.SHA256 {
			return nil, errors.New("invalid release manifest")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	out.WriteString("mithra-release-v1\nversion ")
	out.WriteString(version)
	out.WriteByte('\n')
	for _, name := range names {
		artifact := manifest.Artifacts[name]
		fmt.Fprintf(&out, "artifact %s %d %s\n", name, artifact.Size, artifact.SHA256)
	}
	return []byte(out.String()), nil
}

func ParseManifest(raw []byte) (ReleaseManifest, error) {
	if len(raw) == 0 || len(raw) > 128<<10 || string(raw[len(raw)-1:]) != "\n" {
		return ReleaseManifest{}, errors.New("invalid release manifest")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) < 3 || lines[0] != "mithra-release-v1" || !strings.HasPrefix(lines[1], "version ") {
		return ReleaseManifest{}, errors.New("invalid release manifest")
	}
	manifest := ReleaseManifest{Version: strings.TrimPrefix(lines[1], "version "), Artifacts: map[string]ReleaseArtifact{}}
	for _, line := range lines[2:] {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != "artifact" {
			return ReleaseManifest{}, errors.New("invalid release manifest")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || manifest.Artifacts[fields[1]].SHA256 != "" {
			return ReleaseManifest{}, errors.New("invalid release manifest")
		}
		manifest.Artifacts[fields[1]] = ReleaseArtifact{SHA256: fields[3], Size: size}
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil || string(canonical) != string(raw) {
		return ReleaseManifest{}, errors.New("release manifest is not canonical")
	}
	return manifest, nil
}

func VerifyRelease(raw, signature []byte, publicKey ed25519.PublicKey, name string, artifact []byte) (ReleaseManifest, error) {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, raw, signature) {
		return ReleaseManifest{}, errors.New("release signature verification failed")
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return ReleaseManifest{}, err
	}
	expected, ok := manifest.Artifacts[name]
	if !ok || int64(len(artifact)) != expected.Size {
		return ReleaseManifest{}, errors.New("release artifact is absent or has the wrong size")
	}
	digest := sha256.Sum256(artifact)
	if hex.EncodeToString(digest[:]) != expected.SHA256 {
		return ReleaseManifest{}, errors.New("release artifact checksum mismatch")
	}
	return manifest, nil
}

func DecodePublisherKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("invalid publisher key")
	}
	return ed25519.PublicKey(decoded), nil
}
