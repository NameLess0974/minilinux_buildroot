# HTTP Boot Server (Go)

Serveur HTTP pour le boot réseau PXE de Raspberry Pi avec détection automatique de fallback SD.

Optimisé pour gérer 200-500 Raspberry Pi simultanément.

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
9. [Logs et Monitoring](#logs-et-monitoring)
10. [Dépannage](#dépannage)

---

## Logique du Serveur (Machine à États)

Le serveur gère automatiquement le cycle de vie de chaque Raspberry Pi via une machine à 3 états.

### Les 3 États

| État | Description | Réponse HTTP pour boot.sig/boot.img |
|------|-------------|-------------------------------------|
| `ALLOWED` (0) | Device autorisé à télécharger | **200 OK** - Sert les fichiers |
| `BLOCKED_MONITORING` (1) | Post-flash, surveillance 5 min | **404** - Force boot SD |
| `BLOCKED_PERMANENT` (2) | Flash confirmé réussi | **404** - Permanent |

### Exemple Concret avec MAC `2C:CF:67:87:2B:EC`

**Scénario 1 : Premier flash réussi**
```
1. Pi démarre en boot réseau → GET /boot.sig
2. MAC inconnue → Création état ALLOWED
3. Serveur répond 200 OK, sert boot.sig puis boot.img
4. Pi télécharge l'image, vérifie signature, flash la SD
5. Pi envoie → GET /confirm/2C:CF:67:87:2B:EC?status=success
6. Serveur → État passe à BLOCKED_MONITORING (timer 5 min)
7. Pi reboot sur la SD card
8. ... 5 minutes passent sans requête HTTP ...
9. Serveur → État passe à BLOCKED_PERMANENT (succès confirmé)
```

**Scénario 2 : Échec du boot SD (watchdog)**
```
1. Pi est en BLOCKED_MONITORING après flash
2. Boot SD échoue → Watchdog → Reboot → Boot réseau
3. Pi demande → GET /boot.sig → Reçoit 404 (1ère)
4. Pi reboot → GET /boot.sig → 404 (2ème)
5. Pi reboot → GET /boot.sig → 404 (3ème)
6. Serveur détecte 3x 404 en 5 min → ÉCHEC SD
7. État revient à ALLOWED
8. Prochain boot → Re-flash automatique
```

**Scénario 3 : Crash après période stable**
```
1. Pi est en BLOCKED_PERMANENT (stable depuis des jours)
2. Crash ou corruption SD → Reboot → Boot réseau
3. Pi demande → GET /boot.sig → 404
4. Serveur détecte nouvelle requête après période stable
5. État passe à BLOCKED_MONITORING (nouvelle surveillance)
6. Si 3x 404 en 5 min → Retour ALLOWED → Re-flash
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
cat /home/pi/minilinux-server-go/mac_whitelist.txt

# Ajouter une MAC
echo "AA:BB:CC:DD:EE:FF" >> /home/pi/minilinux-server-go/mac_whitelist.txt

# La whitelist est rechargée automatiquement toutes les 60 secondes
# Pas besoin de redémarrer le serveur
```

---

## Base de Données SQLite

Le serveur utilise SQLite pour stocker l'état de chaque device.

### Accéder à la BDD

```bash
sqlite3 /home/pi/minilinux-server-go/devices.db
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

Pour forcer le re-flash d'un device même s'il est bloqué :

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
sudo cp minilinux-server-go.service /etc/systemd/system/
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
| `SERVER_PORT` | 8080 | Port d'écoute HTTP |
| `SERVE_DIRECTORY` | /home/pi/minilinux-server-go | Répertoire des fichiers |
| `DATABASE_PATH` | {SERVE_DIRECTORY}/devices.db | Chemin base SQLite |
| `WHITELIST_FILE` | {SERVE_DIRECTORY}/mac_whitelist.txt | Fichier whitelist |
| `MONITORING_WINDOW` | 5m | Fenêtre de surveillance post-flash |
| `FAILURE_THRESHOLD` | 3 | Nombre de 404 pour détecter échec |

### Timeouts (dans le code)

| Timeout | Valeur | Description |
|---------|--------|-------------|
| ReadTimeout | 10s | Timeout lecture requête |
| WriteTimeout | 30min | Timeout écriture (gros fichiers) |
| IdleTimeout | 120s | Timeout connexion inactive |

---

## Endpoints HTTP

| Endpoint | Description |
|----------|-------------|
| `GET /` | Info serveur et fichiers disponibles |
| `GET /health` | Health check (`{"status":"ok"}`) |
| `GET /boot.sig` | Signature boot (**soumis à state machine**) |
| `GET /boot.img` | Image boot (**soumis à state machine**) |
| `GET /images/final_image.img.xz` | Image système compressée |
| `GET /images/final_image.sig` | Signature de l'image |
| `GET /confirm/<MAC>?status=success` | Confirmation flash réussi |
| `GET /confirm/<MAC>?status=error&code=...` | Signalement erreur |

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
/home/pi/minilinux-server-go/
├── boot.img                    # Image de boot (~100 MB)
├── boot.sig                    # Signature RSA du boot
├── mac_whitelist.txt           # Liste MACs autorisées
├── devices.db                  # Base SQLite (créée auto)
├── README.md                   # Cette documentation
├── minilinux-server-go.service # Service systemd
├── images/
│   ├── final_image.img.xz      # Image système (~6.7 GB)
│   └── final_image.sig         # Signature de l'image
└── go/                         # Code source Go
    ├── cmd/server/main.go      # Point d'entrée
    ├── internal/
    │   ├── config/             # Configuration
    │   ├── storage/            # SQLite storage
    │   ├── state/              # Machine à états
    │   ├── handlers/           # HTTP handlers
    │   ├── server/             # HTTP server
    │   ├── middleware/         # Logging, recovery
    │   ├── arp/                # Cache ARP
    │   └── whitelist/          # Gestion whitelist
    ├── build/                  # Binaires compilés
    ├── go.mod
    ├── go.sum
    └── Makefile
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
cp /home/pi/minilinux-server-go/devices.db /home/pi/minilinux-server-go/devices.db.bak

# Supprimer et laisser le serveur recréer
rm /home/pi/minilinux-server-go/devices.db
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
