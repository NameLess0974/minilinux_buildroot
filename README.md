# minilinux - Buildroot Raspberry Pi System

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

```ini
[all]
BOOT_UART=1
BOOT_ORDER=0xf17            # 7=HTTP Boot, 1=SD card, f=Restart
HTTP_HOST=172.16.1.226
HTTP_PORT=8080
HTTP_PATH=/
```

### Flasher l'EEPROM

```bash
# 1. Créer un nouveau pieeprom.bin
sudo rpi-eeprom-config --config boot.conf --pubkey bootkey-public.pem --out pieeprom-modifier.bin pieeprom-base.bin

# 2. Sur le Raspberry Pi, flasher l'EEPROM
sudo rpi-eeprom-update -d -f pieeprom-modifier.bin

# 3. Redémarrer
sudo reboot
```

## Configuration

### Service auto-installer
```
buildroot/board/raspberrypi/overlay/etc/systemd/system/auto-installer.service
buildroot/board/raspberrypi/overlay/usr/local/bin/auto-installer.sh
```

### Splash screen (GIF via framebuffer)

Le splash screen est un binaire C statique (fb_gif) qui lit un GIF et l'affiche
directement sur /dev/fb0 sans aucune dependance graphique.

Fichiers concernes :
```
buildroot/board/raspberrypi/overlay/usr/local/bin/fb_gif       # binaire
buildroot/board/raspberrypi/overlay/usr/share/splash/loading.gif  # animation
buildroot/board/raspberrypi/overlay/etc/systemd/system/splash-screen.service  # service
```

Pour mettre a jour le GIF ou le binaire :
```bash
# Copier le GIF
cp fb_gif/loading.gif buildroot/board/raspberrypi/overlay/usr/share/splash/loading.gif

# Copier le binaire (si recompile)
cp fb_gif/fb_gif buildroot/board/raspberrypi/overlay/usr/local/bin/fb_gif
chmod +x buildroot/board/raspberrypi/overlay/usr/local/bin/fb_gif
```

Source du binaire : dossier fb_gif/ a la racine du projet (main.c + stb_image.h).
Pour recompiler : cd fb_gif && make (necessite un cross-compilateur aarch64).

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

## Certificats & Clés

### Clés SSH
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
