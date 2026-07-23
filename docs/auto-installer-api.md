# Guide d'implementation - client auto-installer

STATUT : WIP. Le cote serveur est complet et deploye. Ce document est la reference
pour cabler le client. La section 0 resume ce qui est deja en place cote serveur et
ce qui reste a ta charge cote auto-installer.

Ce document decrit tout ce que le script `auto-installer.sh` (cote Pi) peut et doit
utiliser pour dialoguer avec le serveur : endpoints, formats attendus, pin du
certificat TLS, et sequence d'appels a cabler sur le flux d'installation existant.

URL du serveur : `https://bootloader.sabsystem.com:18443` (HTTPS, port 18443).
Le certificat inclut ce hostname dans son SAN, donc le pin `--cacert` fonctionne
avec cette URL. Les fichiers de boot restent sur `http://bootloader.sabsystem.com:18743`.

## 0. Repartition serveur / client

Deja en place et deploye cote serveur (rien a faire) :

- Les deux listeners : HTTP 18743 (boot) et HTTPS 18443 (tout le reste).
- Le certificat TLS auto-signe, SAN incluant `bootloader.sabsystem.com` et l'IP.
  Fichier public a recuperer : `/home/sabuser/minilinux_buildroot/private/certs/server.crt`.
- Endpoints d'ingestion `POST /api/v1/events` et `POST /api/v1/logs`, proteges par
  la whitelist MAC (403 si MAC inconnue). Pas de token requis cote Pi.
- Endpoint `GET|POST /confirm/<MAC>` en HTTPS (machine a etats).
- Endpoint `GET /images/...` en HTTPS (whitelist MAC).
- Dashboard, API fleet et actions manuelles, proteges par auth admin HTTP Basic
  (cote serveur seulement, ne concerne pas le Pi).
- Retention et purge automatique de la telemetrie.

A ta charge cote auto-installer (avec ce guide) :

1. Embarquer `server.crt` dans l'image au chemin `/etc/minilinux/server.crt`.
2. Basculer les URLs du script vers `https://bootloader.sabsystem.com:18443` pour
   images, confirm, telemetrie (l'ancien HTTP ne sert plus que boot.img/boot.sig).
3. Ajouter `--cacert /etc/minilinux/server.crt` a chaque appel HTTPS.
4. Cabler `send_event()` sur chaque etape et `send_logs()` avant reboot (section 9).

## 1. Vue d'ensemble : deux canaux

Le serveur ecoute sur deux ports avec deux roles distincts.

| Port | Protocole | Usage | Qui appelle |
|---|---|---|---|
| 18743 | HTTP (clair) | `boot.img`, `boot.sig` uniquement | Firmware EEPROM (ne sait pas faire TLS) |
| 18443 | HTTPS (TLS pin) | Images, telemetrie, confirm, health | auto-installer.sh (Linux booté, curl) |

Regle : tout ce que le script fait apres le boot passe en HTTPS. Le HTTP ne sert
que les deux fichiers de boot, telecharges par le firmware avant que Linux demarre.

Les fichiers de boot restent surs en clair car `boot.sig` est une signature RSA
verifiee par le firmware : une image modifiee est rejetee.

## 2. Certificat TLS : recuperation et pin

### 2.1 Recuperer le certificat (une fois, cote build)

Le certificat public du serveur se trouve ici :

```
/home/sabuser/minilinux_buildroot/private/certs/server.crt
```

Le copier vers la machine de build :

```bash
scp user@bootloader.sabsystem.com:/home/sabuser/minilinux_buildroot/private/certs/server.crt ./
```

Ne jamais recuperer `server.key` (cle privee, reste sur le serveur).

### 2.2 Integrer le cert dans l'image (rootfs overlay Buildroot)

Placer `server.crt` dans l'overlay du rootfs pour qu'il soit present au boot, au
chemin attendu par le script :

```
/etc/minilinux/server.crt
```

### 2.3 Utiliser le pin dans le script

Toutes les requetes HTTPS doivent passer `--cacert` pointant sur ce fichier :

```bash
CACERT=/etc/minilinux/server.crt
curl --cacert "$CACERT" https://bootloader.sabsystem.com:18443/...
```

Effet : chiffrement plus authentification du serveur. Si un attaquant tente un
MITM avec un autre certificat, curl refuse (exit 60). Ne pas utiliser `-k`
(insecure) : cela desactive cette protection.

Contrainte : le SAN du certificat doit correspondre exactement a l'IP ou au
hostname utilise dans l'URL, sinon curl echoue avec une erreur de hostname. Voir
`private/certs/README.md` pour regenerer le cert si l'IP du serveur change.

## 3. Telechargement de l'image (HTTPS)

```bash
SERVER=bootloader.sabsystem.com
CACERT=/etc/minilinux/server.crt

curl --cacert "$CACERT" --fail --show-error \
     "https://$SERVER:18443/images/final_image.img.xz" -o /tmp/image.img.xz

curl --cacert "$CACERT" --fail --show-error \
     "https://$SERVER:18443/images/final_image.sig" -o /tmp/image.sig
```

L'acces aux images est restreint : seule une MAC presente dans la whitelist du
serveur (et resolvable via ARP) recoit le fichier. Sinon reponse 404.

## 4. Telemetrie de progression : POST /api/v1/events

Un evenement par etape. Best-effort : la reponse est ignoree par le client, et un
echec (serveur injoignable, 4xx, 5xx) ne doit pas interrompre l'installation.

### 4.1 Format

```
POST https://bootloader.sabsystem.com:18443/api/v1/events
Content-Type: application/json
```

Corps :

```json
{
  "mac": "AA:BB:CC:DD:EE:FF",
  "boot_id": "AA:BB:CC:DD:EE:FF-1727428309",
  "ts": 1727428309,
  "step": "download_signature",
  "status": "ok",
  "progress": 20,
  "attempt": 1,
  "message": "Signature telechargee",
  "details": {}
}
```

### 4.2 Champs

| Champ | Type | Description |
|---|---|---|
| `mac` | string | Identite machine, format `AA:BB:CC:DD:EE:FF` majuscules. Requis. |
| `boot_id` | string | `MAC-<timestamp_boot>`, groupe une session (survit aux reboots). |
| `ts` | int | Timestamp Unix client. Informatif seulement (horloge fausse au boot). |
| `step` | string | Etape, valeurs de l'enum ci-dessous. |
| `status` | string | `start`, `ok`, `error` ou `retry`. |
| `progress` | int | 0-100. Borne cote serveur. |
| `attempt` | int | Numero de tentative (1 a 3). |
| `message` | string | Texte libre. |
| `details` | object | Extra selon l'etape (voir 4.4). |

Notes serveur :
- Le serveur horodate a reception (`received_at`) et utilise cela pour l'ordre.
  Le `ts` client sert seulement au delta interne.
- Si `mac` est absent, le serveur tente une resolution ARP. Toujours l'envoyer.
- Si `boot_id` est absent, le serveur retombe sur la MAC (groupement degrade).
- Une MAC hors whitelist recoit 403.

### 4.3 Enum step

| step | progress | Moment |
|---|---|---|
| `boot_start` | 0 | Demarrage |
| `wait_device` | 0 | Attente /dev/mmcblk0 |
| `device_ready` | 5 | Carte SD detectee |
| `network_check` | 10 | Connectivite OK |
| `download_signature` | 20 | Signature telechargee |
| `wipe_device` | 25 | Effacement carte |
| `stream_flash` | 25-90 | Streaming plus dd (long) |
| `flash_done` | 90 | Flash termine |
| `verify_signature` | 92 | Verification RSA |
| `install_success` | 95 | Signature valide |
| `reboot` | 100 | Reboot imminent |

### 4.4 details selon l'etape

- `stream_flash` : `{ "bytes_written": 1234567890, "image_size_est": 3200000000, "speed_mbps": 42 }`
- `verify_signature` : `{ "hash": "sha256:abc...", "valid": true }`
- `wait_device` : `{ "timeout_s": 30 }`

## 5. Erreurs : meme route, status "error"

Meme endpoint `POST /api/v1/events`, avec `status: "error"` et un `error_code`
dans `details` :

```json
{
  "mac": "AA:BB:CC:DD:EE:FF",
  "boot_id": "AA:BB:CC:DD:EE:FF-1727428309",
  "ts": 1727428339,
  "step": "wait_device",
  "status": "error",
  "attempt": 1,
  "message": "Device /dev/mmcblk0 introuvable",
  "details": { "error_code": "no_sdcard" }
}
```

Le serveur extrait `error_code` de `details` pour l'indexer.

### Enum error_code

| error_code | Cas |
|---|---|
| `no_sdcard` | /dev/mmcblk0 absent apres timeout |
| `no_network` | Pas de connectivite |
| `signature_download` | Echec telechargement signature |
| `signature_invalid` | Signature RSA invalide |
| `stream_failed` | curl/xz/dd a echoue |
| `flash_failed` | Echec general du flash |
| `max_retries` | Abandon apres 3 tentatives |

Pour `stream_failed`, remonter le maillon fautif dans `details` :

```json
{ "error_code": "stream_failed", "curl_exit": 0, "xz_exit": 1, "dd_exit": 0 }
```

## 6. Logs complets : POST /api/v1/logs

Envoye une fois, juste avant reboot (succes ou echec), pour disposer du detail
post-mortem sans ecran.

```
POST https://bootloader.sabsystem.com:18443/api/v1/logs
Content-Type: text/plain
X-Machine-MAC: AA:BB:CC:DD:EE:FF
X-Boot-Id: AA:BB:CC:DD:EE:FF-1727428309
```

Corps : le texte brut (sortie du script plus dmesg). Taille limitee a 4 Mo cote
serveur. Stocke indexe par `boot_id`.

## 7. Confirmation finale : /confirm (compatibilite)

L'endpoint existant reste disponible, desormais en HTTPS :

```bash
curl --cacert "$CACERT" "https://$SERVER:18443/confirm/$MAC?status=success"
curl --cacert "$CACERT" "https://$SERVER:18443/confirm/$MAC?status=error&code=signature_invalid"
```

Il pilote la machine a etats (passage en surveillance apres un flash reussi). Les
evenements `/api/v1/events` sont pour le suivi ; `/confirm` pour la logique de
boot. Emettre les deux : un `install_success`/`reboot` en telemetrie, et le
`confirm?status=success` pour la machine a etats.

## 8. Reponses serveur

Toutes les routes de telemetrie repondent :

```
200 OK   { "ok": true }
```

Le client ignore le corps. Codes d'erreur possibles cote client : 400 (JSON
invalide ou MAC manquante), 403 (MAC hors whitelist), 405 (mauvaise methode).
Aucun ne doit interrompre l'installation.

## 9. Sequence a cabler dans auto-installer.sh

L'idee : une fonction `send_event()` appelee a chaque changement d'etape (la ou
`set_progress` est deja appele), plus un envoi de logs avant reboot.

```bash
SERVER=bootloader.sabsystem.com
CACERT=/etc/minilinux/server.crt
MAC="$(get_mac_address)"                 # deja disponible dans le script
BOOT_ID="${MAC}-$(cat /proc/uptime | cut -d. -f1)"   # MAC + uptime, stable par session

# Envoi d'un evenement de progression. Best-effort, non bloquant.
send_event() {
  local step="$1" status="$2" progress="$3" message="$4" details="${5:-{}}"
  local attempt="${ATTEMPT:-1}"
  curl --cacert "$CACERT" --max-time 5 --silent --output /dev/null \
    -X POST "https://$SERVER:18443/api/v1/events" \
    -H "Content-Type: application/json" \
    -d "{\"mac\":\"$MAC\",\"boot_id\":\"$BOOT_ID\",\"ts\":$(date +%s),\"step\":\"$step\",\"status\":\"$status\",\"progress\":$progress,\"attempt\":$attempt,\"message\":\"$message\",\"details\":$details}" \
    || true
}

# Envoi du blob de logs. A appeler juste avant reboot.
send_logs() {
  local logfile="$1"
  curl --cacert "$CACERT" --max-time 15 --silent --output /dev/null \
    -X POST "https://$SERVER:18443/api/v1/logs" \
    -H "Content-Type: text/plain" \
    -H "X-Machine-MAC: $MAC" \
    -H "X-Boot-Id: $BOOT_ID" \
    --data-binary @"$logfile" \
    || true
}
```

Points d'appel indicatifs :

```bash
send_event boot_start          start 0   "Demarrage"
send_event wait_device         start 0   "Attente carte SD" '{"timeout_s":30}'
send_event device_ready        ok    5    "Carte SD detectee"
send_event network_check       ok    10   "Reseau OK"
send_event download_signature  ok    20   "Signature telechargee"
send_event wipe_device         start 25   "Effacement carte"
# pendant le stream, periodiquement :
send_event stream_flash        ok    50   "Flash en cours" "{\"bytes_written\":$BW,\"speed_mbps\":$SPD}"
send_event flash_done          ok    90   "Flash termine"
send_event verify_signature    start 92   "Verification signature"
send_event install_success     ok    95   "Signature valide" '{"valid":true}'
send_event reboot              ok    100  "Reboot"

# sur erreur, exemple :
send_event wait_device error 0 "mmcblk0 introuvable" '{"error_code":"no_sdcard"}'

# avant reboot, dans tous les cas :
send_logs /var/log/install.log
```

Contraintes a respecter :
- `--max-time` sur chaque curl pour ne jamais bloquer l'install si le serveur ne
  repond pas.
- `|| true` (ou equivalent) pour que l'echec d'un envoi n'arrete pas le script.
- Echapper correctement les guillemets dans `message` et `details` (JSON valide).
- `boot_id` fixe pour toute la session (calcule une fois au debut).
