#!/bin/sh
set -eu

repository=${MITHRA_REPOSITORY:-glnarayanan/mithra}
version=${MITHRA_VERSION:?set MITHRA_VERSION to an exact release tag}
publisher_key_b64='__MITHRA_RELEASE_PUBLIC_KEY_PEM_B64__'
operation=install

if [ "${1:-}" = "install" ] || [ "${1:-}" = "upgrade" ]; then
  operation=$1
  shift
fi

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) arch=amd64 ;;
  Linux-aarch64|Linux-arm64) arch=arm64 ;;
  *) echo "Mithra supports Linux amd64 and arm64 only." >&2; exit 1 ;;
esac

for command in curl openssl awk base64 sha256sum mktemp; do
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

verify_artifact() {
  name=$1
  path=$2
  expected=$(awk -v name="$name" '$1=="artifact" && $2==name {print $4}' "$stage/RELEASE-MANIFEST")
  [ -n "$expected" ] || { echo "Release manifest omits $name" >&2; exit 1; }
  actual=$(sha256sum "$path" | awk '{print $1}')
  [ "$actual" = "$expected" ] || { echo "Release digest mismatch for $name" >&2; exit 1; }
}

# Verify this bootstrap too. The documented flow verifies it before execution;
# this protects operators who retain the downloaded script for later use.
verify_artifact install.sh "$0"
verify_artifact "mithra-linux-${arch}" "$stage/mithra-linux-${arch}"
verify_artifact "mithra-installer-linux-${arch}" "$stage/mithra-installer-linux-${arch}"

chmod 0700 "$stage/mithra-installer-linux-${arch}"
exec "$stage/mithra-installer-linux-${arch}" "$operation" \
  --artifact "$stage/mithra-linux-${arch}" \
  --candidate-installer "$stage/mithra-installer-linux-${arch}" \
  --manifest "$stage/RELEASE-MANIFEST" \
  --signature "$stage/RELEASE-MANIFEST.sig" \
  --release-version "$version" "$@"
