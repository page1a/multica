#!/usr/bin/env bash
# Create the long-lived self-signed code-signing certificate used by kun
# Desktop releases. The owner runs this once, on a Mac. The private key and
# the .p12 never belong in the git repo, an issue, or a comment.
#
# What it writes (mode 0600, outside the repo):
#   key.pem       private key. Do not copy this anywhere shared.
#   cert.pem      public certificate. Safe to import on machines that install
#                 the app, if the release notes say the cert must be trusted.
#   cert.p12      key + cert, encrypted. This is what CI imports.
#   csc-link.b64  base64 of cert.p12, one line, no trailing newline. Feed it
#                 to `gh secret set CSC_LINK`.
#
# Usage:
#   scripts/macos-self-sign-cert.sh
#   scripts/macos-self-sign-cert.sh --out "$HOME/Library/Application Support/multica-signing"
#
# A password is read from the terminal and is not printed. For a local
# experiment only, MULTICA_CERT_PASSWORD plus --non-interactive skips the
# prompt. Do not put that password in the repo.

set -euo pipefail

CN="Multica Kun Self-Signed"
DAYS=3650
OUT="${HOME}/Library/Application Support/multica-signing"
NONINTERACTIVE=0
FORCE=0

usage() {
  cat <<'EOF'
Usage: scripts/macos-self-sign-cert.sh [--out DIR] [--cn NAME] [--days N]
       [--non-interactive] [--force]

Creates a Code Signing self-signed certificate (default validity 3650 days,
default CN "Multica Kun Self-Signed") and exports an encrypted .p12 plus a
one-line base64 file for the CSC_LINK GitHub secret.

Refuses to write inside a git checkout. Refuses to overwrite an existing
key unless --force is passed: a new certificate changes the designated
requirement and already-shipped builds stop accepting updates.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out)
      OUT="${2:?--out needs a directory}"
      shift 2
      ;;
    --cn)
      CN="${2:?--cn needs a name}"
      shift 2
      ;;
    --days)
      DAYS="${2:?--days needs a number}"
      shift 2
      ;;
    --non-interactive)
      NONINTERACTIVE=1
      shift
      ;;
    --force)
      FORCE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if ! [[ "$DAYS" =~ ^[0-9]+$ ]] || [[ "$DAYS" -lt 1 ]]; then
  echo "--days must be a positive integer" >&2
  exit 2
fi

if [[ "$OUT" != /* ]]; then
  echo "--out must be an absolute path" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)
case "$OUT" in
  "$repo_root"|"$repo_root"/*)
    echo "refusing to write the certificate inside the git repo ($repo_root)" >&2
    exit 1
    ;;
esac

mkdir -p "$OUT"
chmod 700 "$OUT"

if [[ -e "$OUT/key.pem" || -e "$OUT/cert.p12" ]]; then
  if [[ "$FORCE" -ne 1 ]]; then
    echo "certificate already exists in $OUT" >&2
    echo "not regenerating: a new key would make shipped builds reject later updates." >&2
    echo "pass --force only when you intend to start a new signing identity." >&2
    exit 1
  fi
  rm -f "$OUT/key.pem" "$OUT/cert.pem" "$OUT/cert.p12" "$OUT/csc-link.b64" "$OUT/openssl.cnf"
fi

password="${MULTICA_CERT_PASSWORD:-}"
if [[ -z "$password" ]]; then
  if [[ "$NONINTERACTIVE" -eq 1 ]]; then
    echo "MULTICA_CERT_PASSWORD is required with --non-interactive" >&2
    exit 2
  fi
  read -r -s -p "p12 password (this becomes CSC_KEY_PASSWORD): " password
  echo
  read -r -s -p "repeat password: " password_again
  echo
  if [[ "$password" != "$password_again" ]]; then
    echo "passwords did not match" >&2
    exit 2
  fi
fi
if [[ -z "$password" ]]; then
  echo "refusing an empty p12 password" >&2
  exit 2
fi

cnf="$OUT/openssl.cnf"
cat >"$cnf" <<EOF
[ req ]
distinguished_name = req_distinguished_name
prompt = no
x509_extensions = codesign_ext

[ req_distinguished_name ]
CN = ${CN}

[ codesign_ext ]
basicConstraints = critical, CA:true
keyUsage = critical, digitalSignature, keyCertSign
extendedKeyUsage = critical, codeSigning
EOF
chmod 600 "$cnf"

openssl req -x509 -newkey rsa:3072 -sha256 -days "$DAYS" -nodes \
  -config "$cnf" \
  -keyout "$OUT/key.pem" \
  -out "$OUT/cert.pem"
chmod 600 "$OUT/key.pem" "$OUT/cert.pem"

# OpenSSL 3's default PKCS#12 uses AES-256. macOS `security import` (and
# therefore electron-builder on the CI runner) rejects that archive with
# "MAC verification failed" even when the password is correct. The legacy
# algorithms are what Apple's tool still imports.
openssl pkcs12 -export -legacy \
  -inkey "$OUT/key.pem" \
  -in "$OUT/cert.pem" \
  -out "$OUT/cert.p12" \
  -name "$CN" \
  -passout "pass:${password}"
chmod 600 "$OUT/cert.p12"

base64 <"$OUT/cert.p12" | tr -d '\n' >"$OUT/csc-link.b64"
chmod 600 "$OUT/csc-link.b64"
rm -f "$cnf"

# Drop the password from this process's variables before printing next steps.
unset password password_again

cat <<EOF

Certificate written to:
  $OUT

Private files (do not commit, paste, or attach these):
  $OUT/key.pem
  $OUT/cert.p12
  $OUT/csc-link.b64

Public certificate (only this one may leave the machine, and only if an
install has to trust it):
  $OUT/cert.pem

Put the secrets on the GitHub repo yourself. This script does not call gh.

  gh secret set CSC_LINK --repo jeff-kunkun/multica < "$OUT/csc-link.b64"
  gh secret set CSC_KEY_PASSWORD --repo jeff-kunkun/multica

The second command prompts. Type the same password you just chose. Do not
pass it on the command line.

A local packaging run uses the same inputs electron-builder sees in CI:

  export CSC_LINK="$OUT/cert.p12"
  export CSC_KEY_PASSWORD='(the password)'
  unset CSC_IDENTITY_AUTO_DISCOVERY
  pnpm --filter @multica/desktop package -- --mac --arm64

Confirm the identity afterwards with:

  codesign -dv --verbose=4 /Applications/Multica.app 2>&1 | head
  codesign -d -r- /Applications/Multica.app

A usable signature shows Authority=${CN} and a designated requirement that
mentions the certificate, not \`cdhash\`. Signature=adhoc means the p12 was
not used.
EOF
