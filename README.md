# Configuration EEPROM — Raspberry Pi 5 — boot HTTPS

Configuration EEPROM permettant à un Pi 5 de démarrer en HTTP boot sur
`bootloader.sabsystem.com`, de télécharger `boot.img` en RAM et de lancer le
mode maintenance automatique.

Validée sur matériel le 30/07/2026 (Pi 5 Model B Rev 1.0).

---

## 1. Mise en route

Sur un Pi 5 sous Raspberry Pi OS, avec `rpi-eeprom` >= 28.9 :

```bash
# depuis la machine de dev, copier le dossier sur le Pi
scp -r . pi@<ip-du-pi>:~/eeprom

# sur le Pi
cd ~/eeprom
./scripts/verify.sh      # contrôles, ne flashe rien
./scripts/setup.sh       # demande confirmation avant de flasher
sudo reboot
```

Après redémarrage :

```bash
sudo rpi-eeprom-config   # doit correspondre à config/boot.conf
```

Options de `setup.sh` :

| Option | Effet |
|---|---|
| `-y` | pas de confirmation interactive |
| `--install-service` | installe `flash-eeprom.service`, qui rejoue la configuration à chaque démarrage |

`--install-service` sert au déploiement en série sur des Pi neufs. Sur un Pi
déjà configuré, il expose à un reflash accidentel.

---

## 2. Contenu

| Chemin | Rôle |
|---|---|
| `bin/pieeprom-base.bin` | Firmware 2025-11-27 non configuré, tel que fourni par `rpi-eeprom` |
| `bin/pieeprom-https.bin` | Binaire de référence, validé sur matériel |
| `config/boot.conf` | Configuration EEPROM, extraite d'un Pi en fonctionnement |
| `keys/bootkey-public.pem` | Clé publique RSA-2048 de signature d'image |
| `certs/server.der` | Certificat du serveur de boot, format DER |
| `scripts/setup.sh` | Génère le binaire et le flashe |
| `scripts/verify.sh` | 10 contrôles de cohérence, sans flash |
| `systemd/flash-eeprom.service` | Unité optionnelle, déploiement en série |
| `CHECKSUMS.sha256` | Empreintes de tous les fichiers |

Les empreintes de référence (binaires, certificat, clé publique) sont dans
`CHECKSUMS.sha256`, vérifiées par `verify.sh`.

---

## 3. Configuration appliquée

```ini
[all]
BOOT_UART=1
BOOT_ORDER=0xf17
HTTP_HOST=bootloader.sabsystem.com
HTTP_PORT=18443
HTTP_PATH=/
HTTP_CACERT_HASH=6163585b380cab5a28af59210f7a17acfc4924fd32bced05a11e7c2815f0d1d9
NET_INSTALL_ENABLED=0
SIGNED_BOOT=0
DISABLE_HDMI=0
```

`BOOT_ORDER=0xf17` se lit de droite à gauche : `7` USB, `1` SD, `f` boucle.

`HTTP_CACERT_HASH` est le sha256 de `certs/server.der`. Les deux vont
ensemble : si le certificat du serveur change, il faut mettre à jour le fichier
**et** cette valeur.

`NET_INSTALL_ENABLED=0` désactive l'installateur réseau interactif de
Raspberry Pi, sans rapport avec le HTTP boot utilisé ici.

Certificat : `CN=minilinux-server`, auto-signé, valide jusqu'au 20/07/2036,
SAN `IP:172.16.3.56, DNS:bootloader.sabsystem.com, DNS:sabsystem`.

---

## 4. Certificat embarqué

Le bootloader ne se contente pas du champ texte `HTTP_CACERT_HASH` : il stocke
le certificat DER complet dans l'EEPROM et vérifie qu'il correspond au hash
annoncé. C'est le rôle de l'option `--cacertder`, que `setup.sh` passe
automatiquement.

À garder en tête si un binaire est régénéré à la main : sans cette option, la
configuration texte reste correcte mais le certificat est absent, et le boot
échoue sur un mismatch de hash. `setup.sh` et `verify.sh` contrôlent tous deux
que le certificat est bien présent dans le binaire.

---

## 5. Reproductibilité

`pieeprom-base.bin` + `boot.conf` + `bootkey-public.pem` + `server.der`
redonnent `pieeprom-https.bin` octet pour octet. `setup.sh` régénère donc le
binaire au lieu de copier un fichier figé, et `verify.sh` vérifie l'égalité
quand `rpi-eeprom-config` est disponible.

### Version du firmware

`rpi-eeprom-update` affiche `CURRENT` (27 nov) plus récent que `LATEST`
(5 nov) : c'est normal. `LATEST` reflète le canal `default` (= `critical`),
volontairement conservateur. Le firmware utilisé ici est celui des canaux
`stable`, `latest` et `beta`, tous identiques. Rien à mettre à jour.

---

## 6. Signature des images

`boot.img` doit être signé avec la clé privée correspondant à
`keys/bootkey-public.pem`, sur la machine de build :

```bash
rpi-eeprom-digest -i boot.img -o boot.sig -k bootkey-private.pem
```

Pour vérifier qu'une clé privée correspond bien à la publique de ce dossier,
comparer les empreintes :

```bash
openssl rsa -in bootkey-private.pem -pubout 2>/dev/null \
  | openssl rsa -pubin -outform DER | sha256sum
openssl rsa -pubin -in keys/bootkey-public.pem -outform DER | sha256sum
```

---

## 7. Secure boot

`SIGNED_BOOT=0`, et aucun fusible OTP de sécurité n'est activé sur le Pi de
référence (registre `17` de révocation à zéro). La clé publique est donc
embarquée dans l'EEPROM mais pas verrouillée : `SIGNED_BOOT` reste
désactivable et la clé remplaçable.

Activer les fusibles OTP est **définitif**. Un Pi verrouillé n'accepte plus que
des images signées par la clé correspondante, sans retour possible. À ne faire
qu'après validation complète de la chaîne de signature, et jamais sur du
matériel de développement.

---

## 8. Diagnostic

```bash
./scripts/verify.sh                    # cohérence du dossier
sudo rpi-eeprom-config                 # configuration active
vcgencmd bootloader_version            # version du firmware
sudo rpi-eeprom-update                 # état des mises à jour
sudo vcgencmd otp_dump | grep '^17:'   # fusible de révocation
```

Certificat réellement présenté par le serveur :

```bash
openssl s_client -connect bootloader.sabsystem.com:18443 </dev/null 2>/dev/null \
  | openssl x509 -outform DER | sha256sum
# doit égaler HTTP_CACERT_HASH
```

Retour à l'état validé :

```bash
sudo rpi-eeprom-update -d -f bin/pieeprom-https.bin && sudo reboot
```

| Symptôme | Cause probable |
|---|---|
| Mismatch de hash au boot, valeur inconnue | binaire généré sans `--cacertder` |
| Mismatch avec le hash du serveur | certificat serveur régénéré : mettre à jour `server.der` et `HTTP_CACERT_HASH` ensemble |
| Le Pi ignore le HTTP boot | `BOOT_ORDER` incorrect, ou aucun `boot.img` servi |
| Configuration revenue en arrière après reboot | `flash-eeprom.service` installé et reflashe un ancien binaire |
