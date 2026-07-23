# Certificats TLS du serveur minilinux

Ce dossier contient le certificat TLS auto-signe du serveur HTTPS (port 18443).

## Fichiers

| Fichier | Public | Role |
|---|---|---|
| `server.crt` | Oui | Certificat. A copier sur chaque Pi (pin). Versionnable. |
| `server.key` | Non (secret) | Cle privee. Ne quitte jamais le serveur. Exclue de git (`.gitignore`). |

## Chemin a recuperer pour le SCP (cote Pi / build)

```
/home/sabuser/minilinux_buildroot/private/certs/server.crt
```

Exemple de copie vers une machine de build Buildroot :

```bash
scp user@<SERVEUR>:/home/sabuser/minilinux_buildroot/private/certs/server.crt ./
# puis l'integrer au rootfs overlay -> /etc/minilinux/server.crt dans l'image
```

Ne jamais copier `server.key`.

## Regenerer le certificat

Le cert present ici a ete genere avec une IP/hostname donnes. Si l'IP du serveur
change, regenere-le (le SAN doit correspondre a l'URL utilisee par les Pi) :

```bash
cd /home/sabuser/minilinux_buildroot
./scripts/gen-server-cert.sh <IP_DU_SERVEUR> [hostname...]
# ex: ./scripts/gen-server-cert.sh 192.168.1.10 minilinux.local
```

Puis re-deployer `server.crt` sur les Pi (rebuild image), sinon le pin echouera.

## Verifier le SAN

```bash
openssl x509 -in server.crt -noout -text | grep -A1 'Subject Alternative Name'
```

Le SAN doit contenir exactement l'IP/hostname que les Pi mettent dans l'URL
`https://<ici>:18443/...`, sinon `curl --cacert` refuse avec une erreur de hostname.
