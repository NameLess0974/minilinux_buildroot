#!/bin/bash
# Configure l'EEPROM d'un Raspberry Pi 5 pour le boot HTTPS.
#
# Régénère le binaire depuis :
#   bin/pieeprom-base.bin  + config/boot.conf
#   + keys/bootkey-public.pem  + certs/server.der
# puis le flashe. Le flash prend effet au prochain reboot.
#
# Ne pas retirer --cacertder : le bootloader vérifie le certificat DER
# embarqué, pas seulement HTTP_CACERT_HASH.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE="$DIR/bin/pieeprom-base.bin"
CONF="$DIR/config/boot.conf"
PUBKEY="$DIR/keys/bootkey-public.pem"
CERT="$DIR/certs/server.der"
REF="$DIR/bin/pieeprom-https.bin"
OUT="/tmp/pieeprom-configured.bin"

INSTALL_SERVICE=0
ASSUME_YES=0
for arg in "$@"; do
    case "$arg" in
        --install-service) INSTALL_SERVICE=1 ;;
        -y|--yes)          ASSUME_YES=1 ;;
        -h|--help)
            echo "usage: $0 [--install-service] [-y]"
            echo "  --install-service  installe flash-eeprom.service (reflash à chaque boot)"
            echo "  -y                 pas de confirmation"
            exit 0 ;;
        *) echo "argument inconnu : $arg" >&2; exit 1 ;;
    esac
done

die() { echo "ERREUR: $*" >&2; exit 1; }
ok()  { echo "  [ok] $*"; }

# Deux modes d'execution : root direct (service systemd) ou via sudo
# (interactif). Le service tourne en root et n'a pas de tty, donc appeler
# sudo y echouerait.
if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
else
    SUDO="sudo"
    sudo -v || die "sudo requis"
fi

echo "=== 1. Vérification du matériel ==="
MODEL=$(tr -d '\0' < /proc/device-tree/model)
echo "  modèle : $MODEL"
case "$MODEL" in
    *"Raspberry Pi 5"*) ok "Pi 5 détecté" ;;
    *) die "ce script cible le Pi 5 (EEPROM 2712). Modèle incompatible." ;;
esac
command -v rpi-eeprom-config >/dev/null || die "rpi-eeprom-config absent (apt install rpi-eeprom)"
command -v rpi-eeprom-update >/dev/null || die "rpi-eeprom-update absent"
rpi-eeprom-config --help 2>&1 | grep -q -- '--cacertder' \
    || die "rpi-eeprom-config ne supporte pas --cacertder (rpi-eeprom trop ancien, il faut >= 28.9)"
ok "outils rpi-eeprom présents, --cacertder disponible"

echo "=== 2. Vérification des fichiers ==="
for f in "$BASE" "$CONF" "$PUBKEY" "$CERT"; do
    [ -f "$f" ] || die "fichier manquant : $f"
    ok "$(basename "$f")"
done

echo "=== 3. Cohérence certificat / configuration ==="
CERT_HASH=$(sha256sum "$CERT" | cut -d' ' -f1)
CONF_HASH=$(grep -oP 'HTTP_CACERT_HASH=\K\S*' "$CONF" || true)
[ -n "$CONF_HASH" ] || die "HTTP_CACERT_HASH absent de boot.conf : le boot HTTPS échouera"
echo "  sha256(server.der) : $CERT_HASH"
echo "  HTTP_CACERT_HASH   : $CONF_HASH"
[ "$CERT_HASH" = "$CONF_HASH" ] || die "hash du certificat != HTTP_CACERT_HASH. Le Pi rejettera le serveur."
ok "le hash correspond"

CERT_END=$(openssl x509 -inform DER -in "$CERT" -noout -enddate | cut -d= -f2)
openssl x509 -inform DER -in "$CERT" -noout -checkend 0 >/dev/null 2>&1 \
    || die "certificat EXPIRÉ le $CERT_END"
ok "certificat valide jusqu'au $CERT_END"

echo "=== 4. Vérification de la clé publique ==="
openssl rsa -pubin -in "$PUBKEY" -noout >/dev/null 2>&1 || die "bootkey-public.pem illisible"
KEY_FP=$(openssl rsa -pubin -in "$PUBKEY" -outform DER 2>/dev/null | sha256sum | cut -d' ' -f1)
echo "  fingerprint : $KEY_FP"
ok "clé publique valide"

echo "=== 5. Configuration à appliquer ==="
sed 's/^/  /' "$CONF"
echo
echo "  Config EEPROM actuelle de ce Pi :"
$SUDO rpi-eeprom-config 2>/dev/null | sed 's/^/    /' || echo "    (illisible)"

# Confirmation seulement en interactif. Sans terminal (service systemd, cron,
# pipe), on ne peut pas poser la question : on flashe directement.
if [ "$ASSUME_YES" -eq 0 ] && [ -t 0 ]; then
    echo
    read -rp ">>> Flasher l'EEPROM avec cette configuration ? [oui/NON] " REP
    [ "$REP" = "oui" ] || { echo "annulé."; exit 0; }
elif [ "$ASSUME_YES" -eq 0 ]; then
    echo "  (pas de terminal : mode automatique)"
fi

echo "=== 6. Génération du binaire ==="
$SUDO rpi-eeprom-config \
     --config "$CONF" \
     --pubkey "$PUBKEY" \
     --cacertder "$CERT" \
     --out "$OUT" \
     "$BASE" || die "génération échouée"
$SUDO chown "$(id -u):$(id -g)" "$OUT" 2>/dev/null || true
ok "généré : $OUT"

echo "=== 7. Vérification du binaire généré ==="
diff <(rpi-eeprom-config "$OUT") "$CONF" >/dev/null \
    || die "le binaire généré ne contient pas la config attendue"
ok "config conforme"

# Le certificat DER doit être physiquement présent dans le binaire.
python3 - "$CERT" "$OUT" <<'PYEOF' || die "certificat DER absent du binaire généré (--cacertder n'a pas pris effet)"
import sys
der = open(sys.argv[1], 'rb').read()
data = open(sys.argv[2], 'rb').read()
off = data.find(der)
print(f"  certificat DER embarqué à l'offset {off}" if off >= 0 else "  certificat DER ABSENT")
sys.exit(0 if off >= 0 else 1)
PYEOF
ok "certificat embarqué"

# Comparaison avec le binaire de référence, s'il est présent.
if [ -f "$REF" ]; then
    if cmp -s "$REF" "$OUT"; then
        ok "identique à pieeprom-https.bin (référence)"
    else
        echo "  [!] le binaire généré diffère de pieeprom-https.bin"
        echo "      octets différents : $(cmp -l "$REF" "$OUT" | wc -l)"
        echo "      (normal si boot.conf, la clé ou le certificat ont été modifiés)"
    fi
fi

echo "=== 8. Flash ==="
$SUDO rpi-eeprom-update -d -f "$OUT" || die "flash échoué"
ok "flash programmé"

if [ "$INSTALL_SERVICE" -eq 1 ]; then
    echo "=== 9. Installation du service ==="
    $SUDO cp "$DIR/systemd/flash-eeprom.service" /etc/systemd/system/
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable flash-eeprom.service
    ok "flash-eeprom.service activé (reflash à chaque démarrage)"
fi

echo
echo "================================================"
echo " TERMINÉ. Redémarrer pour appliquer :  sudo reboot"
echo " Puis vérifier :  sudo rpi-eeprom-config"
echo "================================================"
