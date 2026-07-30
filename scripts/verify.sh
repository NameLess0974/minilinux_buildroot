#!/bin/bash
# Vérifie la cohérence du dossier sans rien flasher.
# Utilisable sur le Pi comme sur une machine de dev (les contrôles
# nécessitant rpi-eeprom sont sautés si l'outil est absent).
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"

FAIL=0
ok()   { echo "  [ok]   $*"; }
bad()  { echo "  [FAIL] $*"; FAIL=1; }
skip() { echo "  [skip] $*"; }

echo "=== 1. Présence des fichiers ==="
for f in bin/pieeprom-base.bin bin/pieeprom-https.bin certs/server.der \
         config/boot.conf keys/bootkey-public.pem scripts/setup.sh \
         systemd/flash-eeprom.service; do
    [ -f "$f" ] && ok "$f" || bad "$f manquant"
done

echo "=== 2. Aucune clé privée ==="
if find . -name '*private*' -o -name '*.key' | grep -q .; then
    bad "clé privée présente dans le dossier"
else
    ok "aucune clé privée"
fi

echo "=== 3. Checksums ==="
if [ -f CHECKSUMS.sha256 ]; then
    if sha256sum -c CHECKSUMS.sha256 --quiet 2>/dev/null; then
        ok "tous les checksums correspondent"
    else
        bad "checksum(s) divergent(s) :"
        sha256sum -c CHECKSUMS.sha256 2>&1 | grep -v ': OK$' | sed 's/^/         /'
    fi
else
    skip "CHECKSUMS.sha256 absent"
fi

echo "=== 4. HTTP_CACERT_HASH == sha256(server.der) ==="
CERT_HASH=$(sha256sum certs/server.der | cut -d' ' -f1)
CONF_HASH=$(grep -oP 'HTTP_CACERT_HASH=\K\S*' config/boot.conf || true)
echo "         cert : $CERT_HASH"
echo "         conf : $CONF_HASH"
[ -n "$CONF_HASH" ] || bad "HTTP_CACERT_HASH absent de boot.conf"
[ "$CERT_HASH" = "$CONF_HASH" ] && ok "correspondance" || bad "MISMATCH"

echo "=== 5. Certificat ==="
if openssl x509 -inform DER -in certs/server.der -noout -checkend 0 >/dev/null 2>&1; then
    ok "valide jusqu'au $(openssl x509 -inform DER -in certs/server.der -noout -enddate | cut -d= -f2)"
else
    bad "certificat EXPIRÉ"
fi
openssl x509 -inform DER -in certs/server.der -noout -ext subjectAltName 2>/dev/null | tail -1 | sed 's/^/         SAN:/'

echo "=== 6. Clé publique ==="
if openssl rsa -pubin -in keys/bootkey-public.pem -noout >/dev/null 2>&1; then
    FP=$(openssl rsa -pubin -in keys/bootkey-public.pem -outform DER 2>/dev/null | sha256sum | cut -d' ' -f1)
    echo "         fingerprint : $FP"
    [ "$FP" = "1f695f22df151e6bb0fd41488ab855ac0fcb1b8be053d6242b16365ba19731b4" ] \
        && ok "fingerprint attendu" || bad "fingerprint inattendu"
else
    bad "bootkey-public.pem illisible"
fi

echo "=== 7. Certificat embarqué dans pieeprom-https.bin ==="
python3 - <<'PYEOF'
der = open('certs/server.der', 'rb').read()
for name, expect in [('bin/pieeprom-https.bin', True), ('bin/pieeprom-base.bin', False)]:
    off = open(name, 'rb').read().find(der)
    found = off >= 0
    status = "ok" if found == expect else "FAIL"
    where = f"offset {off}" if found else "absent"
    print(f"  [{status:4}] {name} : {where}")
PYEOF

echo "=== 8. Version du firmware ==="
for f in bin/*.bin; do
    V=$(strings "$f" | grep -m1 -E '^20[0-9]{2}/[0-9]{2}/[0-9]{2}')
    echo "         $(basename "$f") : $V"
    [ "$V" = "2025/11/27" ] || bad "$(basename "$f") : version inattendue"
done

echo "=== 9. setup.sh ==="
bash -n scripts/setup.sh 2>/dev/null && ok "syntaxe valide" || bad "erreur de syntaxe"
grep -q -- '--cacertder' scripts/setup.sh \
    && ok "utilise --cacertder" || bad "--cacertder absent : les binaires générés seront inutilisables"

echo "=== 10. Reproductibilité du build ==="
if command -v rpi-eeprom-config >/dev/null 2>&1; then
    TMP=$(mktemp /tmp/eeprom-verify-XXXXXX.bin)
    if sudo rpi-eeprom-config --config config/boot.conf --pubkey keys/bootkey-public.pem \
            --cacertder certs/server.der --out "$TMP" bin/pieeprom-base.bin >/dev/null 2>&1; then
        cmp -s bin/pieeprom-https.bin "$TMP" \
            && ok "rebuild identique à pieeprom-https.bin" \
            || bad "rebuild différent ($(cmp -l bin/pieeprom-https.bin "$TMP" | wc -l) octets)"
    else
        bad "génération échouée"
    fi
    sudo rm -f "$TMP"
else
    skip "rpi-eeprom-config absent (vérification à faire sur le Pi)"
fi

echo
[ "$FAIL" -eq 0 ] && echo "=== RÉSULTAT : tout est cohérent ===" \
                  || echo "=== RÉSULTAT : des contrôles ont ÉCHOUÉ ==="
exit "$FAIL"
