# HTTP Boot Server (Go)

Serveur pour le boot réseau de Raspberry Pi avec détection automatique de fallback SD,
télémétrie d'installation et dashboard de suivi du parc.

Optimisé pour gérer 200-500 Raspberry Pi simultanément.

---

## Architecture actuelle (à jour)

Le serveur écoute sur DEUX ports avec des rôles distincts :

| Port | Protocole | Sert | Qui appelle |
|------|-----------|------|-------------|
| 18743 | HTTP (clair) | `boot.img`, `boot.sig` uniquement | Firmware EEPROM (pas de TLS) |
| 18443 | HTTPS (TLS pin) | images, télémétrie, confirm, health, dashboard, actions | auto-installer.sh (Linux booté) |

Le HTTP ne sert que les deux fichiers de boot. Tout le reste est en HTTPS. Les
fichiers de boot restent sûrs en clair car `boot.sig` (signature RSA) est vérifié
par le firmware. Toute requête non-boot sur le port HTTP renvoie 404.

### Certificat TLS (auto-signé, pinné)

Le certificat vit dans `private/certs/` (voir `private/certs/README.md`). Il est
auto-signé et pinné côté client via `curl --cacert`. Le SAN doit contenir
l'IP/hostname exact utilisé dans l'URL par les Pi. Régénérer avec :

```bash
./scripts/gen-server-cert.sh <IP> <hostname...>
# ex: ./scripts/gen-server-cert.sh 172.16.3.56 bootloader.sabsystem.com
```

Seul `private/certs/server.crt` (public) est distribué aux Pi.
`private/certs/server.key` ne quitte jamais le serveur.

### Télémétrie et dashboard

- Ingestion (depuis les Pi, best-effort, whitelist MAC) :
  - `POST /api/v1/events` — un événement par étape d'installation
  - `POST /api/v1/logs` — blob de logs complet en fin de run
- Lecture (admin, protégée par Basic Auth) :
  - `GET /dashboard` — page de suivi temps réel du parc
  - `GET /api/v1/fleet` — état par box (reboots, timing, installation)
  - `GET /api/v1/sessions`, `/api/v1/sessions/{boot_id}`, `/api/v1/logs/{boot_id}`
  - `POST /api/v1/action` — actions manuelles (`reflash`, `block`, `reset`)

Le contrat client complet est dans `docs/auto-installer-api.md`.

### Authentification admin (HTTP Basic)

Si `ADMIN_USER` est défini, le dashboard et toutes les routes de lecture/action
exigent une authentification HTTP Basic (`ADMIN_USER` / `ADMIN_PASS`). Le
navigateur affiche une fenêtre de connexion. Les identifiants circulent chiffrés
car ces routes sont uniquement en HTTPS. Les routes d'ingestion (les Pi) ne sont
PAS concernées (protégées par la whitelist MAC). Si `ADMIN_USER` est vide, l'accès
admin est ouvert (déploiement interne uniquement).

Accès dashboard : `https://<hostname>:18443/dashboard` puis login.

### Rétention télémétrie

Les événements et logs de télémétrie plus vieux que `TELEMETRY_RETENTION`
(défaut 14 jours) sont purgés automatiquement toutes les `TELEMETRY_PURGE_EVERY`
(défaut 6 h).

---

## Table des matières

1. [Logique du Serveur (Machine à États)](#logique-du-serveur-machine-à-états)
2. [Whitelist MAC](#whitelist-mac)
3. [Base de Données SQLite](#base-de-données-sqlite)
4. [Compilation](#compilation)
5. [Lancement](#lancement)
6. [Configuration](#configuration)
7. [Endpoints HTTP](#endpoints-http)
8. [Structure du Projet](#structure-du-projet)
9. [Signature des Fichiers](#signature-des-fichiers)
10. [Logs et Monitoring](#logs-et-monitoring)
11. [Dépannage](#dépannage)

---

## Logique du Serveur (Machine à États)

Le serveur gère automatiquement le cycle de vie de chaque Raspberry Pi via une machine à 3 états.

### Les 3 États

| État | Description | Réponse HTTP pour boot.sig/boot.img |
|------|-------------|-------------------------------------|
| `ALLOWED` (0) | Device autorisé à télécharger | **200 OK** - Sert les fichiers |
| `BLOCKED_MONITORING` (1) | Post-flash, surveillance active | **404** - Force boot SD |

**Note :** La logique est basée sur les requêtes (lazy evaluation), sans timer en background.

### Exemple Concret avec MAC `2C:CF:67:87:2B:EC`

**Scénario 1 : Premier flash - Boot SD stable**
```
1. Pi démarre en boot réseau → GET /boot.sig
2. MAC inconnue → Création état ALLOWED
3. Serveur répond 200 OK, sert boot.sig puis boot.img
4. Pi télécharge l'image, vérifie signature, flash la SD
5. Pi envoie → GET /confirm/2C:CF:67:87:2B:EC?status=success
6. Serveur → État passe à BLOCKED_MONITORING (timestamp initial)
7. Pi reboot sur la SD card (boot SD réussit)
8. 30s après : Pi demande → GET /boot.sig (ordre EEPROM: HTTP puis SD)
9. Serveur → 404 (compteur 1/3, <5min depuis timestamp)
10. Pi continue sur SD → Fonctionne normalement
11. Prochain reboot (ex: 3h après) → GET /boot.sig
12. Serveur détecte >5min → Reset timestamp, compteur reste à 1/3
13. État reste en BLOCKED_MONITORING (surveillance continue)
```

**Scénario 2 : Échec du boot SD (watchdog loop)**
```
1. Pi est en BLOCKED_MONITORING après flash
2. Boot SD échoue → Watchdog déclenche reboot → Boot réseau
3. Pi demande → GET /boot.sig → Reçoit 404 (compteur: 1/3)
4. Watchdog → Reboot → GET /boot.sig → 404 (compteur: 2/3)
5. Watchdog → Reboot → GET /boot.sig → 404 (compteur: 3/3)
6. Serveur détecte 3x 404 en <5 min → ÉCHEC SD détecté
7. État passe à ALLOWED, compteur reset
8. Prochain boot → 200 OK → Re-flash automatique
```

**Scénario 3 : Requête après longue période**
```
1. Pi en BLOCKED_MONITORING stable depuis des jours
2. Pi reboot (maintenance, crash, etc.) → Boot réseau
3. Pi demande → GET /boot.sig
4. Serveur détecte timestamp >5min → Reset monitoring window
5. 404 envoyé (compteur: 1/3), nouveau timestamp
6. Si vraiment cassé : 3x 404 rapides → ALLOWED → Re-flash
7. Si juste un reboot normal : reste en MONITORING (1/3)
```

---

## Whitelist MAC

Seules les adresses MAC présentes dans la whitelist peuvent télécharger les fichiers.

### Fichier `mac_whitelist.txt`

```
# Whitelist des MAC autorisées
# Une adresse MAC par ligne (format XX:XX:XX:XX:XX:XX)
# Les lignes commençant par # sont ignorées

2C:CF:67:87:2B:EC
DC:A6:32:XX:XX:XX
E4:5F:01:XX:XX:XX
```

### Commandes utiles

```bash
# Voir la whitelist actuelle
cat config/mac_whitelist.txt

# Ajouter une MAC
echo "AA:BB:CC:DD:EE:FF" >> config/mac_whitelist.txt

# La whitelist est rechargée automatiquement toutes les 60 secondes
# Pas besoin de redémarrer le serveur
```

---

## Base de Données SQLite

Le serveur utilise SQLite pour stocker l'état de chaque device.

### Accéder à la BDD

```bash
sqlite3 db/devices.db
```

### Commandes SQLite utiles

```sql
-- Affichage lisible
.headers on
.mode column

-- Voir toutes les tables
.tables

-- Voir le schéma
.schema devices
```

### Schéma de la table `devices`

| Colonne | Type | Description |
|---------|------|-------------|
| `mac` | TEXT | Adresse MAC (clé primaire) |
| `state` | INTEGER | 0=Allowed, 1=BlockedMonitoring, 2=BlockedPermanent |
| `block_start` | INTEGER | Timestamp début du blocage (NULL si pas bloqué) |
| `error_404_count` | INTEGER | Nombre de 404 dans la fenêtre actuelle |
| `flash_complete` | INTEGER | 1 si flash terminé avec succès |
| `flash_img` | INTEGER | **1 pour forcer un re-flash** |
| `last_error` | TEXT | Dernier code d'erreur reçu |
| `last_error_time` | INTEGER | Timestamp dernière erreur |
| `last_update` | INTEGER | Timestamp dernière mise à jour |
| `created_at` | INTEGER | Timestamp création |

### Requêtes courantes

```sql
-- Lister tous les devices
SELECT mac, state, error_404_count, flash_complete, flash_img FROM devices;

-- Voir un device spécifique
SELECT * FROM devices WHERE mac = '2C:CF:67:87:2B:EC';

-- Voir les événements 404 récents
SELECT * FROM device_404_events ORDER BY timestamp DESC LIMIT 20;
```

### Forcer un Re-flash (flash_img)

Le plus simple est le bouton Reflash du dashboard. En SQL (équivalent) :

```sql
-- Activer le flag flash_img (bypass le blocage)
UPDATE devices SET flash_img = 1 WHERE mac = '2C:CF:67:87:2B:EC';
```

Le device recevra les fichiers boot au prochain démarrage, même en état BLOCKED.
Le flag est automatiquement remis à 0 après confirmation du flash.

### Réinitialiser un Device

```sql
-- Remettre un device en état ALLOWED (prêt pour flash)
UPDATE devices 
SET state = 0, 
    block_start = NULL, 
    error_404_count = 0, 
    flash_complete = 0,
    flash_img = 0
WHERE mac = '2C:CF:67:87:2B:EC';

-- Supprimer complètement un device
DELETE FROM devices WHERE mac = '2C:CF:67:87:2B:EC';
DELETE FROM device_404_events WHERE mac = '2C:CF:67:87:2B:EC';
```

### Réinitialiser TOUS les Devices

```sql
-- ATTENTION : Remet tous les devices en ALLOWED
UPDATE devices SET state = 0, block_start = NULL, error_404_count = 0;

-- Vider la table des événements 404
DELETE FROM device_404_events;
```

---

## Compilation

### Prérequis

- Go 1.21+
- SQLite inclus (modernc.org/sqlite, pas de CGO requis)

### Commandes

```bash
cd go/

# Compilation standard (architecture locale)
make build

# Compilation pour Raspberry Pi 32-bit (ARMv7)
make build-arm

# Compilation pour Raspberry Pi 4/5 64-bit (ARM64)
make build-arm64

# Nettoyage des fichiers compilés
make clean
```

Le binaire sera créé dans `go/build/minilinux-server`.

---

## Lancement

### Mode développement

```bash
cd go/

# Lancer directement avec go run
make run-dev

# Ou après compilation
make run
```

### Mode production

```bash
# Lancer le binaire directement
./go/build/minilinux-server

# Avec variables d'environnement personnalisées
SERVE_DIRECTORY=/chemin/vers/fichiers \
DATABASE_PATH=/chemin/vers/devices.db \
SERVER_PORT=8080 \
./go/build/minilinux-server
```

### Installation Service Systemd

```bash
# Copier le binaire
sudo cp go/build/minilinux-server /usr/local/bin/minilinux-server-go
sudo chmod +x /usr/local/bin/minilinux-server-go

# Installer le service
sudo cp deploy/minilinux-server-go.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable minilinux-server-go
sudo systemctl start minilinux-server-go
```

### Contrôler le Service

```bash
sudo systemctl status minilinux-server-go   # Statut
sudo systemctl start minilinux-server-go    # Démarrer
sudo systemctl stop minilinux-server-go     # Arrêter
sudo systemctl restart minilinux-server-go  # Redémarrer
```

---

## Configuration

### Variables d'environnement

| Variable | Défaut | Description |
|----------|--------|-------------|
| `PROJECT_ROOT` | /home/sabuser/minilinux_buildroot | Racine ancrant tous les chemins par défaut |
| `SERVER_PORT` | 18743 | Port d'écoute HTTP (boot uniquement) |
| `HTTPS_PORT` | 18443 | Port d'écoute HTTPS (tout le reste) |
| `SERVE_DIRECTORY` | {PROJECT_ROOT}/data | Répertoire des fichiers servis |
| `DATABASE_PATH` | {PROJECT_ROOT}/db/devices.db | Chemin base SQLite |
| `WHITELIST_FILE` | {PROJECT_ROOT}/config/mac_whitelist.txt | Fichier whitelist |
| `TLS_CERT_FILE` | {PROJECT_ROOT}/private/certs/server.crt | Certificat TLS (active HTTPS si présent) |
| `TLS_KEY_FILE` | {PROJECT_ROOT}/private/certs/server.key | Clé privée TLS |
| `ADMIN_USER` | (vide) | Utilisateur admin (Basic Auth). Vide = accès ouvert |
| `ADMIN_PASS` | (vide) | Mot de passe admin (Basic Auth) |
| `MONITORING_WINDOW` | 5m | Fenêtre de surveillance post-flash |
| `FAILURE_THRESHOLD` | 3 | Nombre de 404 pour détecter échec |
| `TELEMETRY_RETENTION` | 14d (336h) | Durée de conservation de la télémétrie |
| `TELEMETRY_PURGE_EVERY` | 6h | Fréquence de la purge de télémétrie |

HTTPS s'active automatiquement si `TLS_CERT_FILE` et `TLS_KEY_FILE` existent.

### Timeouts (dans le code)

| Timeout | Valeur | Description |
|---------|--------|-------------|
| ReadTimeout | 10s | Timeout lecture requête |
| WriteTimeout | 30min | Timeout écriture (gros fichiers) |
| IdleTimeout | 120s | Timeout connexion inactive |

---

## Endpoints HTTP

Le port HTTP (18743) ne sert QUE `boot.img` et `boot.sig`. Tout le reste
ci-dessous est sur HTTPS (18443). La racine `/` renvoie 404 (aucune info exposée).

| Endpoint | Port | Description |
|----------|------|-------------|
| `GET /boot.sig` | HTTP | Signature boot (**soumis à state machine**) |
| `GET /boot.img` | HTTP | Image boot (**soumis à state machine**) |
| `GET /health` | HTTPS | Health check (`{"status":"ok"}`) |
| `GET /images/final_image.img.xz` | HTTPS | Image système compressée |
| `GET /images/final_image.sig` | HTTPS | Signature de l'image |
| `GET /confirm/<MAC>?status=success` | HTTPS | Confirmation flash réussi |
| `GET /confirm/<MAC>?status=error&code=...` | HTTPS | Signalement erreur |
| `POST /api/v1/events` | HTTPS | Télémétrie de progression (whitelist MAC) |
| `POST /api/v1/logs` | HTTPS | Blob de logs (whitelist MAC) |
| `GET /dashboard` | HTTPS | Dashboard de suivi (Basic Auth) |
| `GET /api/v1/fleet` | HTTPS | État du parc (Basic Auth) |
| `POST /api/v1/action` | HTTPS | Actions manuelles reflash/block/reset (Basic Auth) |

### Codes d'erreur supportés

| Code | Description |
|------|-------------|
| `success` | Installation réussie |
| `signature_invalid` | Vérification RSA échouée |
| `download_failed` | Téléchargement échoué |
| `hash_mismatch` | Hash incorrect |
| `decompress_failed` | Décompression XZ échouée |
| `write_failed` | Écriture DD échouée |
| `network_error` | Erreur réseau |

---

## Structure du Projet

```
minilinux_buildroot/
├── README.md
├── data/                        # SERVE_DIRECTORY : uniquement les fichiers exposés
│   ├── boot/
│   │   ├── boot.img             # Image de boot (URL /boot.img)
│   │   └── boot.sig             # Signature RSA du boot (URL /boot.sig)
│   └── images/
│       ├── final_image.img.xz   # Image système (URL /images/...)
│       ├── final_image.sig      # Signature de l'image
│       └── hash.txt             # Hash (reste serveur)
├── private/                     # Secrets, JAMAIS servis
│   ├── certs/                   # server.crt (public), server.key (secret)
│   └── keys/                    # bootkey-private.pem, bootkey-public.pem
├── db/                          # Base SQLite (devices.db, créée auto)
├── config/
│   └── mac_whitelist.txt        # Liste MACs autorisées
├── scripts/
│   ├── sign-boot.sh             # Signature de boot.img
│   └── gen-server-cert.sh       # Génération du certificat TLS
├── deploy/
│   ├── minilinux-server-go.service
│   └── minilinux.env.example    # Modèle des secrets (ADMIN_USER/PASS)
├── docs/
│   └── auto-installer-api.md    # Contrat client auto-installer
└── go/                          # Code source Go
    ├── cmd/server/main.go
    ├── internal/{config,storage,state,handlers,server,middleware,arp,whitelist}/
    └── build/                   # Binaires compilés
```

Principe : `data/` contient exactement ce qui est exposé (les URLs `/boot.img`
et `/images/...` sont mappées en interne vers `data/boot/` et `data/images/`).
Les secrets (`private/`) et la base (`db/`) sont hors du dossier servi.

---

## Signature des Fichiers

Le serveur vérifie les signatures RSA pour garantir l'intégrité des fichiers.

### Clés RSA

Les clés RSA vivent dans `private/keys/` :
- `bootkey-private.pem` - Clé privée (pour signer, ne quitte pas le serveur)
- `bootkey-public.pem` - Clé publique (pour vérifier sur le Pi)

### Signer boot.img

```bash
# Le script utilise par défaut data/boot/boot.img et private/keys/bootkey-private.pem
./scripts/sign-boot.sh
```

**Fichier généré :** `data/boot/boot.sig` (servi via l'URL /boot.sig)

### Signer final_image.img.xz

```bash
cd data/images

# 1. Calculer le hash SHA256 de l'image
sha256sum final_image.img.xz | awk '{print $1}' > hash.txt

# 2. Signer le hash avec la clé privée
openssl dgst -sha256 -sign ../../private/keys/bootkey-private.pem \
  -out final_image.sig hash.txt
```

**Fichiers générés (dans data/images/) :** `hash.txt`, `final_image.sig`

### Re-signer après modification

À chaque modification de `boot.img` ou `final_image.img.xz`, re-signer :

```bash
./scripts/sign-boot.sh                       # boot

cd data/images                               # image système
sha256sum final_image.img.xz | awk '{print $1}' > hash.txt
openssl dgst -sha256 -sign ../../private/keys/bootkey-private.pem \
  -out final_image.sig hash.txt

sudo systemctl restart minilinux-server-go   # recharger
```

---

## Logs et Monitoring

### Voir les logs en temps réel

```bash
sudo journalctl -u minilinux-server-go -f
```

### Filtrer les logs

```bash
# Dernières 100 lignes
sudo journalctl -u minilinux-server-go -n 100

# Depuis aujourd'hui
sudo journalctl -u minilinux-server-go --since today

# Dernières 30 minutes
sudo journalctl -u minilinux-server-go --since "30 min ago"

# Chercher les erreurs
sudo journalctl -u minilinux-server-go | grep -i error

# Chercher un MAC spécifique
sudo journalctl -u minilinux-server-go | grep "2C:CF:67:87:2B:EC"
```

### Format des logs

```
time=2025-12-31T12:03:09+01:00 level=INFO msg="boot request" ip=172.16.1.228 mac=2C:CF:67:87:2B:EC file=boot.sig
time=2025-12-31T12:03:09+01:00 level=INFO msg="state check" mac=2C:CF:67:87:2B:EC serve=true reason=allowed
time=2025-12-31T12:03:09+01:00 level=INFO msg="transfer complete" file=boot.sig bytes=602
```

---

## Dépannage

### Le transfert s'arrête avant la fin

**Symptôme:** `i/o timeout` après quelques minutes

**Cause:** WriteTimeout trop court pour gros fichiers

**Solution:** Augmenter `DefaultWriteTimeout` dans `go/internal/config/config.go` et recompiler

### Device bloqué, impossible de re-flasher

```sql
-- Option 1: Activer flash_img (temporaire)
UPDATE devices SET flash_img = 1 WHERE mac = 'XX:XX:XX:XX:XX:XX';

-- Option 2: Réinitialiser complètement
UPDATE devices SET state = 0, block_start = NULL, error_404_count = 0, flash_complete = 0 WHERE mac = 'XX:XX:XX:XX:XX:XX';
```

### MAC non reconnue (UNKNOWN)

Le serveur utilise `/proc/net/arp` pour résoudre IP → MAC. Si le Pi n'a pas encore communiqué, la MAC peut être UNKNOWN.

**Solution:** Le Pi doit d'abord envoyer une requête ARP. Généralement résolu automatiquement.

### Base de données corrompue

```bash
# Sauvegarder
cp db/devices.db db/devices.db.bak

# Supprimer et laisser le serveur recréer
rm db/devices.db
sudo systemctl restart minilinux-server-go
```

### Vérifier que le serveur écoute

```bash
# Vérifier le port
sudo ss -tlnp | grep 8080

# Tester le health check
curl http://localhost:8080/health
```

---

## Commandes Make

```bash
cd go/
make build       # Compiler
make build-arm   # Compiler pour Pi 32-bit
make build-arm64 # Compiler pour Pi 64-bit
make run         # Compiler et lancer
make run-dev     # Lancer en mode dev
make clean       # Nettoyer
make test        # Lancer les tests
make fmt         # Formater le code
```
