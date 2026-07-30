# HTTP Boot Server (Go)

Serveur pour le boot réseau de Raspberry Pi avec détection automatique de fallback SD,
télémétrie d'installation et API de suivi du parc.

Le suivi du parc se fait depuis la **console** (`middleware-reborn/apps/console`,
menu Bootloader, réservé au rôle `sabsystem`). Ce serveur n'expose plus de page
web : il ne sert que des API.

Optimisé pour gérer 200-500 Raspberry Pi simultanément.

---

## Architecture actuelle (à jour)

Le serveur écoute sur DEUX ports avec des rôles distincts :

| Port | Protocole | Sert | Qui appelle |
|------|-----------|------|-------------|
| 18443 | HTTPS (TLS pin) | `boot.img`, `boot.sig`, images, confirm, health, ingestion télémétrie | Firmware EEPROM (`HTTP_CACERT_HASH` requis) + auto-installer.sh |
| 18543 | HTTP **interne** | API admin : fleet, images, sessions, logs, actions | backend middleware (même machine) |

Il n'y a plus de port HTTP en clair. Une box dont l'EEPROM n'a pas
`HTTP_CACERT_HASH` ne peut pas booter : il n'existe aucun repli.

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

### Télémétrie et API de suivi

- Ingestion (depuis les Pi, best-effort, whitelist MAC — **pas de token**) :
  - `POST /api/v1/events` — un événement par étape d'installation
  - `POST /api/v1/logs` — blob de logs complet en fin de run
- Lecture / action (admin, protégée par le service token) :
  - `GET /api/v1/fleet` — état par box (reboots, timing, installation)
  - `GET /api/v1/sessions`, `/api/v1/sessions/{boot_id}`, `/api/v1/logs/{boot_id}`
  - `GET /api/v1/images` — image servie : taille, hash, état de la signature
  - `POST /api/v1/images/upload` — publie une image (corps brut), la signe et verifie
  - `POST /api/v1/action` — actions manuelles (`reflash`, `block`, `reset`)

Le contrat client complet est dans `docs/auto-installer-api.md`.

### Surface exposée publiquement

Le serveur est joignable depuis une IP publique. Ce qui y est exposé est
volontairement réduit au strict nécessaire pour les Pi :

| Exposé publiquement | Protection |
|---------------------|------------|
| `boot.img` / `boot.sig` | IP + MAC declares dans `box` + signature RSA |
| `/images/` | IP + MAC declares dans `box` |
| `/confirm/<MAC>` | IP + MAC declares dans `box` |
| `POST /api/v1/events`, `/api/v1/logs` | IP + MAC declares dans `box` + corps borne |
| `/health` | Aucune (ne renvoie que `{"status":"ok"}`) |

**Tout le reste vit sur le port interne** et n'est pas atteignable depuis
l'extérieur : état du parc, sessions, lecture des logs, actions manuelles. Un
endpoint non exposé ne peut pas être attaqué, quel que soit l'état de son
authentification — c'est la protection principale, le token venant en second.

Points d'attention (corrigés) :

- Le MAC est **déclaré par le client** (URL ou corps JSON) : il ne prouve rien
  seul. Le controle part donc de l'IP de la connexion, qui doit etre declaree
  dans `box`, et le MAC doit correspondre a celui enregistre pour cette IP.
- `X-Forwarded-For` / `X-Real-IP` ne sont honorés que si la connexion vient d'un
  proxy listé dans `TRUSTED_PROXIES` (vide par défaut). Sinon n'importe qui
  pourrait maquiller son origine dans les logs et fausser la résolution ARP.

### Authentification admin (service token)

Les routes de lecture/action ne sont pas appelées par un humain : le seul client
admin est le **backend middleware**, qui relaie les requêtes de la console. La
chaîne est :

```
console (JWT utilisateur)  ->  backend middleware  ->  ce serveur
                               vérifie le rôle          vérifie SERVICE_TOKEN
                               sabsystem
```

Le backend authentifie l'utilisateur (JWT) et vérifie qu'il a le rôle
`sabsystem` **avant** de relayer. Ce serveur ne vérifie donc qu'une chose : que
l'appelant est bien le backend. Le token est présenté en `X-Service-Token:
<token>` (ou `Authorization: Bearer <token>`) et comparé en temps constant.

La console ne parle jamais directement à ce serveur : elle n'a pas le token, et
n'a donc pas besoin d'accepter le certificat auto-signé.

Les routes d'ingestion (les Pi) ne sont PAS concernées — elles restent protégées
par la liste blanche (IP + MAC declares dans `box`). Si `SERVICE_TOKEN` est vide, l'accès admin est ouvert
(déploiement interne uniquement).

Générer un token : `openssl rand -hex 32`, puis le renseigner des deux côtés
(`SERVICE_TOKEN` ici, `BOOTLOADER_SERVICE_TOKEN` côté backend).

### Rétention télémétrie

Les événements et logs de télémétrie plus vieux que `TELEMETRY_RETENTION`
(défaut 14 jours) sont purgés automatiquement toutes les `TELEMETRY_PURGE_EVERY`
(défaut 6 h).

---

## Table des matières

1. [Logique du Serveur (Machine à États)](#logique-du-serveur-machine-à-états)
2. [Liste blanche (table box)](#liste-blanche-table-box)
3. [Base de donnees (PostgreSQL)](#base-de-donnees-postgresql)
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

## Liste blanche (table `box`)

Un boitier ne peut telecharger boot.img / l'image que s'il est **declare dans la
console** (menu Gestion des Boxs). Il n'y a plus de fichier a editer : la table
`box` du middleware fait office de liste blanche.

### Regle : MAC **ET** IP

Le controle part de l'**IP** (seule donnee certaine au moment du boot : c'est
l'adresse de la connexion), puis verifie le MAC via ARP :

| Situation | Resultat |
|-----------|----------|
| IP absente de `box`, ou `boot_enabled = false` | refuse |
| IP presente, ARP resout un MAC different | refuse (usurpation) |
| IP presente, ARP ne resout rien | autorise — l'IP declaree suffit |
| IP presente, MAC ARP identique | autorise |

Les IP des Pi sont fixes et choisies : elles doivent etre saisies dans la console
**avant** le premier branchement, sinon le boitier est refuse.

### Ajouter / retirer un boitier

Depuis la console, menu Gestion des Boxs. La colonne `boot_enabled` permet de
desactiver un boitier sans supprimer sa fiche :

```sql
UPDATE box SET boot_enabled = false WHERE mac_address = '2c:cf:67:87:2b:ec';
```

La liste est rechargee automatiquement toutes les 60 secondes, sans redemarrage.
Si PostgreSQL est injoignable, le serveur **conserve la derniere liste connue**
plutot que de refuser tout le parc.

---

## Base de donnees (PostgreSQL)

Le serveur partage la base du middleware. Il possede ses propres tables et
n'ecrit **jamais** dans `box` (dont `ip_address` sert a l'authentification du
pilot cote middleware).

| Table | Contenu | Ecrite par |
|-------|---------|-----------|
| `boot_devices` | etat de la state machine par boitier | ce serveur |
| `boot_404_events` | horodatage des reboots reseau | ce serveur |
| `boot_telemetry_events` | progression d'installation | ce serveur |
| `boot_telemetry_logs` | blob de logs par `boot_id` | ce serveur |
| `box` | fiches boitiers + liste blanche | middleware (lue seule ici) |

Le schema est gere par les migrations Drizzle du middleware
(`apps/backend/drizzle/`), pas par ce serveur.

```bash
psql -h localhost -U postgres -d sab -c "SELECT mac, state, flash_complete FROM boot_devices;"
```

---------|------|-------------|
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

Le plus simple est le bouton Reflash de la console (menu Bootloader). En SQL
(équivalent) :

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
- PostgreSQL via pgx (pas de CGO requis)

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
DB_PASSWORD=xxx DB_NAME=sab \
HTTPS_PORT=8443 \
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
| `HTTPS_PORT` | 18443 | Port d'écoute HTTPS (seul port public) |
| `SERVE_DIRECTORY` | {PROJECT_ROOT}/data | Répertoire des fichiers servis |
| `DB_HOST` | localhost | Hote PostgreSQL (base partagee avec le middleware) |
| `DB_PORT` | 5432 | Port PostgreSQL |
| `DB_USER` | postgres | Utilisateur PostgreSQL |
| `DB_PASSWORD` | (vide) | Mot de passe PostgreSQL |
| `DB_NAME` | sab | Nom de la base |
| `TLS_CERT_FILE` | {PROJECT_ROOT}/private/certs/server.crt | Certificat TLS (active HTTPS si présent) |
| `TLS_KEY_FILE` | {PROJECT_ROOT}/private/certs/server.key | Clé privée TLS |
| `SERVICE_TOKEN` | (vide) | Token partagé avec le backend middleware. Vide = accès admin ouvert |
| `IMAGES_DIR` | {SERVE_DIRECTORY}/images | Répertoire de l'image système |
| `BOOT_PUBLIC_KEY` | {PROJECT_ROOT}/private/keys/bootkey-public.pem | Clé publique (vérification de signature) |
| `BOOT_PRIVATE_KEY` | {PROJECT_ROOT}/private/keys/bootkey-private.pem | Clé privée (signature des images uploadées) |
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

Tout est servi sur HTTPS (18443), boot compris. La racine `/` renvoie 404
(aucune info exposée).

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
| `GET /api/v1/fleet` | **interne** | État du parc (service token) |
| `GET /api/v1/images` | **interne** | Image servie : taille, hash, signature (service token) |
| `POST /api/v1/images/upload` | **interne** | Publie une nouvelle image, la signe et la verifie (service token) |
| `GET /api/v1/sessions`, `/api/v1/sessions/{boot_id}` | **interne** | Sessions d'installation (service token) |
| `GET /api/v1/logs/{boot_id}` | **interne** | Logs d'une session (service token) |
| `POST /api/v1/action` | **interne** | Actions manuelles reflash/block/reset (service token) |

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
├── (base : PostgreSQL partagee avec le middleware)
├── config/
│   └── (liste blanche : table box, cote middleware)
├── scripts/
│   ├── sign-boot.sh             # Signature de boot.img
│   └── gen-server-cert.sh       # Génération du certificat TLS
├── deploy/
│   ├── minilinux-server-go.service
│   └── minilinux.env.example    # Modèle des secrets (SERVICE_TOKEN)
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

### Repartir d'un etat propre

Les tables boot_* peuvent etre videes sans risque : elles se reconstruisent au
fil des boots. La table `box` (liste blanche) ne doit PAS etre touchee.

```bash
psql -h localhost -U postgres -d sab -c "TRUNCATE boot_devices, boot_404_events, boot_telemetry_events, boot_telemetry_logs;"
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
