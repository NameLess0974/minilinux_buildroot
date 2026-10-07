# minilinux - Buildroot Raspberry Pi System

## Security & Maintenance

MiniLinux is developed and maintained by
**Cristian Ursan ([@NameLess0974](https://github.com/NameLess0974))**.

The project focuses on secure Raspberry Pi provisioning and recovery,
including:

- signed boot images and integrity verification;
- HTTPS delivery with certificate pinning;
- secure automated recovery;
- SSH hardening.

Security architecture and vulnerability reporting are documented in
[SECURITY.md](SECURITY.md) and [THREAT_MODEL.md](THREAT_MODEL.md).
Maintainer responsibilities are documented in
[MAINTAINERS.md](MAINTAINERS.md).

## Quick Start

```bash
./setup.sh
cd buildroot
make
```

## Output Images

Après compilation, les images sont dans :
```
buildroot/output/images/
├── boot.img.gz          # Image boot signée (compressée)
├── boot.sig             # Signature de l'image
├── sdcard.img           # Image SD complète
├── rootfs.cpio.zst      # Rootfs compressé
└── kernel8.img          # Kernel ARM64
```

### HTTP Boot

Utilise `boot.img` (décompressé) et `boot.sig` pour le démarrage HTTP :
```bash
# Décompresser l'image
gunzip -k buildroot/output/images/boot.img.gz

# Resigner l'image si modifiée
rpi-eeprom-digest -i boot.img -o boot.sig -k buildroot/board/raspberrypi-system-update/bootkey-private.pem
```

### Configuration EEPROM

La configuration EEPROM des Pi (boot HTTPS, port 18443) est figée sur la
branche **`eeprom`**, avec les binaires, le certificat serveur, les scripts de
flash et de contrôle, et la procédure complète :

```bash
git fetch origin eeprom
git worktree add ../minilinux-eeprom eeprom
# puis suivre ../minilinux-eeprom/README.md
```

## Configuration

### Service auto-installer
```
buildroot/board/raspberrypi/overlay/etc/systemd/system/auto-installer.service
buildroot/board/raspberrypi/overlay/usr/local/bin/auto-installer.sh
```

L'auto-installer telecharge, flashe et verifie l'image, avec :
- **Deux canaux serveur** : HTTP (18743, boot.img/boot.sig uniquement, pour le firmware
  EEPROM) et **HTTPS (18443)** pour tout le reste (images, telemetrie, confirmation).
  L'EEPROM des Pi est aujourd'hui configuree en HTTPS sur 18443 avec certificat
  epingle (`HTTP_CACERT_HASH`) : voir la branche `eeprom`.
- **TLS pinne** : toutes les requetes HTTPS utilisent `--cacert /etc/minilinux/server.crt`
  (cert public du serveur, embarque dans l'overlay). Refuse tout MITM.
- **Robustesse reseau** : sonde `/health` avant le flux (`wait_server_ready`) + retry curl
  `--retry-all-errors` pour absorber l'instabilite TLS au demarrage sans gacher une des 3
  tentatives d'installation. Le pipeline de streaming reste volontairement sans `--retry`
  (un retry sur flux pipe corromprait l'image ; une coupure relance une install propre).

### Telemetrie d'installation

L'installer remonte au serveur, en best-effort (jamais bloquant) :
- **`POST /api/v1/events`** : un evenement par etape (boot_start, wait_device, device_ready,
  network_check, download_signature, stream_flash, flash_done, verify_signature,
  install_success, reboot) avec status/progress/attempt/message + `details` JSON
  (ex. codes de sortie curl/xz/dd sur echec de flash).
- **`POST /api/v1/logs`** : le journal complet (`/tmp/install.log`) envoye juste avant
  reboot, pour le post-mortem sans ecran.
- **`GET /confirm/{mac}`** : confirmation finale pour la machine a etats serveur.

Identite : `mac` (interface principale) + `boot_id` = MAC-<uptime>, stable par session.
L'horloge du Pi etant fausse au boot, le serveur horodate a reception.

### Certificat serveur (a fournir avant build)

Le cert public du serveur doit etre place a :
```
buildroot/board/raspberrypi/overlay/etc/minilinux/server.crt
```
Le recuperer depuis le serveur (`server.crt`, jamais la cle privee) et le copier a cet
emplacement. Son SAN doit correspondre au domaine/IP utilise dans les URL (ici
`bootloader.sabsystem.com`).

### Splash screen (video H.265 via framebuffer)

Le splash screen est un binaire C statique (fb_video) qui decode une video H.265
(HEVC brut, Annex-B) via libde265 et l'affiche directement sur /dev/fb0, sans
aucune dependance graphique ni runtime (libde265 est linkee en statique).

Fichiers concernes :
```
buildroot/board/raspberrypi/overlay/usr/local/bin/fb_video           # binaire (statique)
buildroot/board/raspberrypi/overlay/usr/share/splash/loading.hevc    # video HEVC (Annex-B)
buildroot/board/raspberrypi/overlay/usr/share/splash/font.ttf        # police du %
buildroot/board/raspberrypi/overlay/etc/systemd/system/splash-screen.service  # service
```

La progression d'installation (lue depuis /tmp/progress, ecrite par auto-installer.sh)
s'affiche au centre de l'ecran sous forme d'une **barre de progression a coins arrondis
(cyan)** avec le **pourcentage a droite**, composites par-dessus la video (fond
transparent). L'animation boucle en continu (30 fps).

**Mode debug** : dans fb_video.c, `#define DEBUG 1` desactive le splash et bascule
l'ecran sur la console des logs (tty3) en affichant les touches clavier percues (utile
pour diagnostiquer). `DEBUG 0` = splash normal. En mode normal, la combo **Menu x5 +
Entree** bascule aussi vers les logs a la volee.

**Preparer la video depuis un MP4 HEVC** (sur la machine de dev, avec ffmpeg) :
```bash
# Extraire le flux HEVC brut en Annex-B (aucun reencodage, copie du flux)
ffmpeg -i source.mp4 -c:v copy -bsf:v hevc_mp4toannexb -f hevc fb_video/loading.hevc
```
> Pour un rendu net & clean (anti-banding, BT.709), voir la config d'encodage complete
> dans `fb_video/README.md`.

**Recompiler + installer le binaire et la video dans l'overlay** :
```bash
cd fb_video
make video            # compile fb_video (cross-compile aarch64)
make install-video    # copie fb_video + loading.hevc dans l'overlay Buildroot
```

Source du binaire : dossier fb_video/ a la racine du projet (fb_video.c + stb_truetype.h).
Depend de libde265 (paquet Buildroot, active dans .config, linke statiquement depuis
output/staging). Le paquet est configure pour ne rien laisser dans le rootfs au runtime
(voir package/libde265/libde265.mk : lib statique, sans SDL ni encodeur, artefacts purges).

### Kernel & Boot
```
buildroot/.config                                                    # Config Buildroot
buildroot/board/raspberrypi-system-update/cmdline-rpi-system-update.txt  # Paramètres kernel
buildroot/board/raspberrypi-system-update/config-rpi-system-update.txt   # Config firmware
```

### Affichage des logs au boot (cmdline)

Le fichier a modifier est :
```
buildroot/board/raspberrypi-system-update/cmdline-rpi-system-update.txt
```

Apres modification, forcer la regeneration du paquet rpi-firmware avant de rebuilder :
```bash
cd buildroot
make rpi-firmware-reinstall && make
```

**Logs kernel caches (ecran noir puis GIF) :**
```
rootwait console=tty3 quiet splash loglevel=0 logo.nologo vt.global_cursor_default=0 root=/dev/ram0
```

**Logs kernel visibles (mode debug) :**
```
rootwait console=tty0 console=serial0,115200 root=/dev/ram0
```

Description des parametres :
```
console=tty3          redirige les logs vers tty3 (invisible a l'ecran)
quiet                 supprime les messages non critiques du kernel
splash                active le mode splash (ecran propre)
loglevel=0            desactive tous les messages kernel
logo.nologo           supprime les icones de framboise en haut a gauche
vt.global_cursor_default=0   cache le curseur clignotant
console=tty0          affiche les logs sur l'ecran principal (mode debug)
console=serial0,115200       affiche les logs sur le port serie UART
```

## Accès SSH

Le serveur sshd **natif** (systemd `sshd.service`) est utilise. Les permissions des
cles hote et de `/root/.ssh` sont forcees dans `post-build.sh` (sshd refuse de demarrer
avec des cles trop ouvertes) — plus besoin de l'ancien contournement `sshd-debug`
(present mais desactive).

Deux methodes de connexion :
```bash
# root par cle (cle privee NON versionnee, generee dans keys/ hors git)
ssh -i keys/minilinux-root root@<IP_DU_PI>

# utilisateur sab par mot de passe (defini via le users table, hash uniquement)
ssh sab@<IP_DU_PI>
```

L'utilisateur applicatif est **sab** (uid 1000, groupe sudo), defini dans
`buildroot/board/raspberrypi-system-update/users`. Seul le **hash** du mot de passe est
versionne (jamais le mot de passe en clair). Pour changer le mot de passe, regenerer le
hash (`openssl passwd -6 ...`) et l'inscrire dans le users table.

### Clés SSH hôte
```
buildroot/board/raspberrypi/overlay/etc/ssh/
├── ssh_host_ecdsa_key      # Clé privée ECDSA
├── ssh_host_ed25519_key    # Clé privée ED25519
└── ssh_host_rsa_key        # Clé privée RSA
```

### Clé de signature boot
```
buildroot/board/raspberrypi-system-update/bootkey-private.pem  # Clé RSA pour signer boot.img
```

## Dépendances système

**Debian/Ubuntu :**
```bash
sudo apt-get install -y build-essential wget cpio unzip rsync bc \
    libncurses5-dev git python3 file perl patch gawk tar bzip2 \
    gzip xz-utils genimage mtools dosfstools e2fsprogs zstd
```

**Arch Linux :**
```bash
sudo pacman -S --needed base-devel wget cpio unzip rsync bc ncurses \
    git python file perl patch gawk tar bzip2 gzip xz genimage mtools \
    dosfstools e2fsprogs zstd
```

## Build personnalisé

### Modifier la config kernel
```bash
cd buildroot
make linux-menuconfig
make
```

### Modifier la config Buildroot
```bash
cd buildroot
make menuconfig
make
```
