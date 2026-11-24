#!/usr/bin/env bash
# Build a Developer ID-signed and notarized macOS app locally.
#
# Prerequisites:
#   1. A "Developer ID Application" certificate in your login keychain.
#   2. Notary credentials stored once:
#        xcrun notarytool store-credentials notary \
#          --apple-id "you@example.com" --team-id "TEAMID" \
#          --password "app-specific-password"
#
# Usage:
#   MACOS_SIGNING_IDENTITY="Developer ID Application: Name (TEAMID)" \
#     bash scripts/build-macos-signed.sh [darwin/arm64]
#
# The script adds macos.signingIdentity and macos.notarize to mygo.json for
# the build and restores the file on exit; no credentials are written to the
# repository.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

platform="${1:-darwin/arm64}"
identity="${MACOS_SIGNING_IDENTITY:-}"
profile="${NOTARY_KEYCHAIN_PROFILE:-notary}"
keychain="${NOTARY_KEYCHAIN:-}"

if [ -z "$identity" ]; then
  echo "Set MACOS_SIGNING_IDENTITY to a Developer ID Application identity." >&2
  echo "List identities with: security find-identity -v -p codesigning" >&2
  exit 1
fi
case "$identity" in
  "Developer ID Application: "*) ;;
  *)
    echo "Not a Developer ID Application identity: $identity" >&2
    echo "An Apple Development identity cannot be notarized." >&2
    exit 1
    ;;
esac
if ! security find-identity -v -p codesigning | grep -Fq "$identity"; then
  echo "Developer ID identity not found in the keychain: $identity" >&2
  echo "Export and install the certificate first; see BUILD.md." >&2
  exit 1
fi

backup="$(mktemp)"
cp mygo.json "$backup"
restore() {
  cp "$backup" mygo.json
  rm -f "$backup"
}
trap restore EXIT

MACOS_SIGNING_IDENTITY="$identity" \
NOTARY_KEYCHAIN_PROFILE="$profile" \
NOTARY_KEYCHAIN="$keychain" \
python3 - <<'PY'
import json
import os

with open("mygo.json", encoding="utf-8") as f:
    config = json.load(f)
macos = config.setdefault("macos", {})
macos["signingIdentity"] = os.environ["MACOS_SIGNING_IDENTITY"]
notarize = {"keychainProfile": os.environ["NOTARY_KEYCHAIN_PROFILE"]}
if os.environ.get("NOTARY_KEYCHAIN"):
    notarize["keychain"] = os.environ["NOTARY_KEYCHAIN"]
macos["notarize"] = notarize
with open("mygo.json", "w", encoding="utf-8") as f:
    json.dump(config, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY

echo "Signing and notarizing as: $identity"
echo "This can take several minutes while Apple processes the app and the DMG."
CGO_ENABLED=0 go tool mygo build -platform "$platform"
