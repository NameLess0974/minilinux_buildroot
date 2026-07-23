#!/bin/bash
# Reproduit rpi-eeprom-digest : signe boot.img -> boot.sig avec la clef privee RSA.
# La signature porte sur le SHA256 de l'image (le champ "ts:" est informatif, non signe).
set -e

# Racine du projet (le script vit dans scripts/).
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

IMG="${1:-$ROOT/data/boot/boot.img}"
SIG="${2:-$ROOT/data/boot/boot.sig}"
KEY="${3:-$ROOT/private/keys/bootkey-private.pem}"

HASH=$(sha256sum "$IMG" | awk '{print $1}')
TS=$(date +%s)

# Signature RSA/PKCS1v1.5 du binaire de l'image (openssl re-calcule le sha256), en hex
RSA=$(openssl dgst -sha256 -sign "$KEY" "$IMG" | xxd -p | tr -d '\n')

{
  echo "$HASH"
  echo "ts: $TS"
  echo "rsa2048: $RSA"
} > "$SIG"

echo "OK -> $SIG"
cat "$SIG"
