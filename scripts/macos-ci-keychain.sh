#!/usr/bin/env bash
# Turn CSC_LINK + CSC_KEY_PASSWORD into a temporary keychain whose code
# signing identity is trusted. electron-builder only sees identities that
# `security find-identity -v` calls valid, and an untrusted self-signed
# certificate is not valid, so it would silently ad-hoc sign instead.
#
# Prints the keychain path on stdout. The caller exports it as CSC_KEYCHAIN
# and unsets CSC_LINK: when CSC_LINK is set, electron-builder ignores
# CSC_KEYCHAIN and imports the p12 into a fresh keychain with no trust.
#
# The trust lives only in this keychain. It is not part of the shipped app.

set -euo pipefail

if [[ -z "${CSC_LINK:-}" ]]; then
  echo "CSC_LINK is empty" >&2
  exit 1
fi
if [[ -z "${CSC_KEY_PASSWORD:-}" ]]; then
  echo "CSC_KEY_PASSWORD is empty" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/multica-signing.XXXXXX")
chmod 700 "$work"
p12="$work/cert.p12"
public_cert="$work/cert.pem"
keychain="$work/signing.keychain-db"
keychain_password=$(openssl rand -base64 32)

materialize_p12() {
  local link="$1"
  if [[ "$link" == file://* ]]; then
    link="${link#file://}"
  fi
  if [[ "$link" == /* || "$link" == ./* || "$link" == ~/* ]]; then
    if [[ "$link" == ~/* ]]; then
      link="${HOME}/${link#~/}"
    fi
    cp "$link" "$p12"
    return
  fi
  if [[ -f "$link" ]]; then
    cp "$link" "$p12"
    return
  fi
  # Base64 of a p12, which is what the CSC_LINK secret holds. Long, or
  # padded. A short non-file value is not a certificate.
  if [[ ${#link} -gt 2048 || "$link" == *= ]]; then
    printf '%s' "$link" | base64 -d >"$p12"
    return
  fi
  echo "CSC_LINK is neither a p12 file nor base64" >&2
  exit 1
}

materialize_p12 "$CSC_LINK"
chmod 600 "$p12"

# stdout is the keychain path and nothing else. Callers capture it.
security create-keychain -p "$keychain_password" "$keychain" >/dev/null
security set-keychain-settings "$keychain" >/dev/null
security unlock-keychain -p "$keychain_password" "$keychain" >/dev/null
security import "$p12" -k "$keychain" -P "$CSC_KEY_PASSWORD" \
  -T /usr/bin/codesign -T /usr/bin/security -T /usr/bin/productbuild >/dev/null

# The p12 this repo's wizard writes uses the legacy PKCS#12 algorithms so
# `security import` accepts it. -legacy matches that on the way back out.
openssl pkcs12 -legacy -in "$p12" -nokeys -passin "env:CSC_KEY_PASSWORD" -out "$public_cert" >/dev/null
security add-trusted-cert -r trustRoot -p codeSign -k "$keychain" "$public_cert" >/dev/null
security set-key-partition-list -S apple-tool:,apple:,codesign: -s \
  -k "$keychain_password" "$keychain" >/dev/null

valid=$(security find-identity -v -p codesigning "$keychain" | awk '/valid identities found/{print $1; exit}')
if [[ -z "${valid:-}" || "$valid" -lt 1 ]]; then
  echo "no trusted code signing identity in $keychain" >&2
  security find-identity -p codesigning "$keychain" >&2 || true
  exit 1
fi

rm -f "$p12" "$public_cert"
# The keychain password is not printed. The keychain stays unlocked for this
# login session; the caller deletes the directory when packaging finishes.
printf '%s\n' "$keychain"
