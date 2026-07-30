#!/bin/bash
# Prépare et vérifie le passage du HTTP boot en HTTPS (BCM2712 / Pi 5).
#
# Ce script ne modifie AUCUN Pi. Il produit les éléments nécessaires (certificat
# DER, hash SHA-256, fragment de configuration EEPROM) et vérifie côté serveur ce
# qui peut l'être. L'application sur le Pi de test reste MANUELLE et explicite.
#
# Prérequis documentés (raspberrypi.com, boot-http.adoc) :
#   - CPU BCM2712 (Pi 5) uniquement
#   - bootloader du 5 avril 2024 ou plus récent
#   - HTTP_CACERT_HASH défini => téléchargement en HTTPS ; absent => aucun boot
#     possible, le listener HTTP clair du serveur a été supprimé
#
# Usage : ./scripts/test-https-boot.sh [hostname_ou_ip]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CERT="$ROOT/private/certs/server.crt"
OUT="$ROOT/private/certs"
HOST="${1:-bootloader.sabsystem.com}"
HTTPS_PORT="${HTTPS_PORT:-18443}"

echo "=== 1. Certificat serveur ==="
if [ ! -f "$CERT" ]; then
  echo "ERREUR: $CERT introuvable. Lancer d'abord ./scripts/gen-server-cert.sh" >&2
  exit 1
fi
openssl x509 -in "$CERT" -noout -subject -dates -ext subjectAltName

echo
echo "=== 2. Le SAN couvre-t-il $HOST ? ==="
# Le pin échoue si le nom utilisé dans l'URL n'est pas dans le SAN.
if openssl x509 -in "$CERT" -noout -ext subjectAltName | grep -qF "$HOST"; then
  echo "OK : $HOST present dans le SAN"
else
  echo "ATTENTION : $HOST absent du SAN — le boot HTTPS echouera."
  echo "  Regenerer : ./scripts/gen-server-cert.sh <IP> $HOST"
fi

echo
echo "=== 3. Conversion DER + hash SHA-256 (pour l'EEPROM) ==="
# rpi-eeprom-config --cacertder attend du DER, pas du PEM.
openssl x509 -in "$CERT" -outform DER -out "$OUT/server.der"
chmod 644 "$OUT/server.der"
HASH=$(sha256sum "$OUT/server.der" | awk '{print $1}')
echo "DER  : $OUT/server.der"
echo "HASH : $HASH"

echo
echo "=== 4. Fragment de configuration EEPROM ==="
cat > "$OUT/eeprom-https.txt" <<EOF
# Fragment a fusionner dans la config EEPROM du Pi de TEST.
# HTTP_CACERT_HASH present => le bootloader telecharge en HTTPS.
HTTP_HOST=$HOST
HTTP_CACERT_HASH=$HASH
EOF
cat "$OUT/eeprom-https.txt"
echo "(ecrit dans $OUT/eeprom-https.txt)"

echo
echo "=== 5. Le serveur repond-il en HTTPS sur les fichiers de boot ? ==="
# boot.img/boot.sig sont servis uniquement sur le listener HTTPS. Un 404 ici est
# normal depuis cette machine : la MAC n'est pas resoluble via ARP.
for f in boot.img boot.sig; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    --cacert "$CERT" "https://$HOST:$HTTPS_PORT/$f" 2>/dev/null || echo "ERR")
  echo "  https://$HOST:$HTTPS_PORT/$f -> $code"
done
echo
echo "  Un code 000 signale un probleme de TLS ou de joignabilite, pas la whitelist."

cat <<'EOF'

======================================================================
SUITE — A EXECUTER MANUELLEMENT, SUR UN PI DE TEST UNIQUEMENT
======================================================================

AVERTISSEMENT
  Une EEPROM mal configuree empeche le boot reseau. Prevoir une carte SD
  de recovery AVANT de commencer. Ne jamais appliquer sur un Pi de production.

A. Verifier le prerequis bootloader (sur le Pi de test)
     vcgencmd bootloader_version      # doit etre >= 2024-04-05
     cat /proc/cpuinfo | grep -i model # doit etre un Pi 5 (BCM2712)

B. Appliquer la configuration sur le Pi de TEST
     sudo rpi-eeprom-config --out /tmp/eeprom.txt          # sauvegarde !
     cp /tmp/eeprom.txt /tmp/eeprom-backup.txt             # garder l'original
     # fusionner le fragment de l'etape 4 dans /tmp/eeprom.txt
     sudo rpi-eeprom-config --apply /tmp/eeprom.txt --cacertder /chemin/server.der

C. Rebooter et observer
   Cote serveur, suivre les logs :
     journalctl -u minilinux-server-go -f
   Attendu si HTTPS fonctionne : les lignes "HTTP request" pour boot.img/boot.sig
   portent scheme=https. Un echec de pin apparait en WARN "http server" avec un
   "TLS handshake error".

POINTS VALIDES — boot reel du 2026-07-30 (Box 3, 2C:CF:67:87:2B:EC)
  1. Le port non standard 18443 est accepte par le bootloader.
  2. HTTP_CACERT_HASH s'applique bien a un HTTP_HOST personnalise.
  3. Le certificat auto-signe est accepte, sans chaine complete.
  Trace : boot.sig et boot.img servis en 200 avec scheme=https local=…:18443,
  ports source firmware 1026/1028, aucun TLS handshake error.

EN CAS D'ECHEC — retour arriere
     sudo rpi-eeprom-config --apply /tmp/eeprom-backup.txt
   ATTENTION : le listener HTTP clair a ete supprime. Restaurer l'EEPROM d'origine
   ne permet donc PAS de rebooter le Pi par le reseau — il faut une carte SD de
   recovery. C'est le prix du HTTPS obligatoire.

RAPPEL DE PERIMETRE
  Le HTTPS protege le TRANSPORT. Il ne retire pas les secrets presents dans
  boot.img : les cles SSH d'hote resteraient identiques sur tout le parc et
  presentes sur chaque SD. Les correctifs C1/C2 de docs/securite-boot-image.md
  restent necessaires, independamment du resultat de ce test.
EOF
