#!/bin/bash
# Génère un certificat TLS auto-signé pour le serveur minilinux.
# Le SAN (Subject Alternative Name) DOIT contenir l'IP/hostname que les clients
# utilisent, sinon le pin (curl --cacert) échoue avec une erreur de hostname.
#
# Usage: ./gen-server-cert.sh <IP_OU_HOSTNAME_DU_SERVEUR> [autre_san ...]
# Exemple: ./gen-server-cert.sh 192.168.1.10 minilinux.local
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: $0 <IP_OU_HOSTNAME> [san...]" >&2
  echo "Ex:    $0 192.168.1.10 minilinux.local" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CERT_DIR="$ROOT/private/certs"
mkdir -p "$CERT_DIR"

KEY="$CERT_DIR/server.key"
CRT="$CERT_DIR/server.crt"
DAYS=3650  # 10 ans (parc fermé, pas de rotation CA)

# Construit la liste des SAN (IP: si numérique, DNS: sinon)
SAN=""
for host in "$@"; do
  if [[ "$host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    SAN="${SAN}IP:${host},"
  else
    SAN="${SAN}DNS:${host},"
  fi
done
SAN="${SAN%,}"  # retire la virgule finale

echo "==> Génération certificat auto-signé"
echo "    SAN: $SAN"

openssl req -x509 -newkey rsa:4096 -sha256 -days "$DAYS" -nodes \
  -keyout "$KEY" -out "$CRT" \
  -subj "/CN=minilinux-server" \
  -addext "subjectAltName=${SAN}" \
  -addext "keyUsage=digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth"

chmod 600 "$KEY"
chmod 644 "$CRT"

echo
echo "==> Fichiers générés :"
echo "    Clé privée  : $KEY   (garder secrète, 600)"
echo "    Certificat  : $CRT   (à embarquer côté client pour le pin)"
echo
echo "==> CÔTÉ AUTO-INSTALLER (Pi) — où mettre le certificat :"
echo "    1. Copier server.crt DANS l'image auto-installer, chemin recommandé :"
echo "         /etc/minilinux/server.crt        (ou /etc/ssl/certs/minilinux-server.crt)"
echo "       -> à intégrer au build Buildroot (rootfs overlay) pour qu'il soit présent au boot."
echo "    2. Dans auto-installer.sh, pointer curl dessus :"
echo "         CACERT=/etc/minilinux/server.crt"
echo "         curl --cacert \"\$CACERT\" https://<SERVEUR>:18443/images/final_image.img.xz -o /tmp/img.xz"
echo "         curl --cacert \"\$CACERT\" -X POST https://<SERVEUR>:18443/api/v1/events -d '...'"
echo "    NB: seul server.crt (public) va sur le Pi. server.key NE QUITTE JAMAIS le serveur."
echo
echo "==> Vérifier le SAN du certificat :"
echo "    openssl x509 -in $CRT -noout -text | grep -A1 'Subject Alternative Name'"
