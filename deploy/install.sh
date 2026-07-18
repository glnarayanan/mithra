#!/bin/sh
set -eu

repository=${MITHRA_REPOSITORY:-glnarayanan/mithra}
version=${MITHRA_VERSION:?set MITHRA_VERSION to an exact release tag}
publisher_key_b64='__MITHRA_RELEASE_PUBLIC_KEY_PEM_B64__'

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) arch=amd64 ;;
  Linux-aarch64|Linux-arm64) arch=arm64 ;;
  *) echo "Mithra supports Linux amd64 and arm64 only." >&2; exit 1 ;;
esac

for command in curl openssl awk sha256sum mktemp; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "Missing prerequisite: $command. Mithra does not install server packages." >&2
    exit 1
  }
done

case "$publisher_key_b64" in
  __MITHRA_*) echo "This installer was not release-built with the pinned publisher key." >&2; exit 1 ;;
esac

stage=$(mktemp -d "${TMPDIR:-/tmp}/mithra-install.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM
base="https://github.com/${repository}/releases/download/${version}"

for name in RELEASE-MANIFEST RELEASE-MANIFEST.sig "mithra-linux-${arch}" "mithra-installer-linux-${arch}"; do
  curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
    --output "$stage/$name" "$base/$name"
done
printf '%s' "$publisher_key_b64" | base64 -d >"$stage/publisher.pem"
openssl pkeyutl -verify -pubin -inkey "$stage/publisher.pem" -rawin \
  -in "$stage/RELEASE-MANIFEST" -sigfile "$stage/RELEASE-MANIFEST.sig" >/dev/null

for name in "mithra-linux-${arch}" "mithra-installer-linux-${arch}"; do
  expected=$(awk -v name="$name" '$1=="artifact" && $2==name {print $4}' "$stage/RELEASE-MANIFEST")
  [ -n "$expected" ] || { echo "Release manifest omits $name" >&2; exit 1; }
  actual=$(sha256sum "$stage/$name" | awk '{print $1}')
  [ "$actual" = "$expected" ] || { echo "Release digest mismatch for $name" >&2; exit 1; }
done

chmod 0700 "$stage/mithra-installer-linux-${arch}"
exec "$stage/mithra-installer-linux-${arch}" install \
  --artifact "$stage/mithra-linux-${arch}" \
  --manifest "$stage/RELEASE-MANIFEST" \
  --signature "$stage/RELEASE-MANIFEST.sig" "$@"
