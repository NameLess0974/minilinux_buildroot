# fb_video_player

Un programme en C minimaliste permettant de jouer une video **H.265 (HEVC)** directement
sur l'écran d'un système Linux, sans passer par une interface graphique lourde comme X11
ou Wayland, ni aucune dépendance runtime (le décodeur libde265 est linké en statique).

> Le dossier s'appelle historiquement `fb_gif/` : il jouait auparavant un GIF. Il joue
> désormais une video HEVC via `fb_video.c`. Le nom du dossier est conservé pour ne pas
> casser les chemins du projet.

## Fonctionnement

Le binaire communique directement avec le Framebuffer Linux (`/dev/fb0`). Au lancement :
1. L'écran texte de la console est basculé en mode graphique pur pour empêcher la frappe clavier d'être affichée.
2. L'espace de la mémoire vidéo est projeté dans l'espace mémoire du programme C via `mmap()`.
3. Le flux HEVC brut (Annex-B) est chargé en RAM, puis **re-décodé en boucle** : chaque image est convertie de YUV420 vers RGB et copiée dans un back buffer (double-buffering), envoyé à l'écran à ~30 fps.

## Technologies utilisées

- **Linux API Base** : `<linux/fb.h>` et `/dev/tty0` pour lire les caractéristiques de l'écran, gérer le curseur texte, et basculer l'affichage en tableau de pixels.
- **libde265** : décodeur H.265 open source (paquet Buildroot, activé dans `.config`), linké **statiquement** depuis `output/staging`. Configuré en décodeur seul, sans SDL ni encodeur ; aucun artefact n'est laissé dans le rootfs (voir `package/libde265/libde265.mk`).
- **stb_truetype.h** : rendu de texte vectoriel anti-aliasé pour afficher le pourcentage d'avancement.

## Préparer la video

Le décodeur lit du HEVC **brut (Annex-B)**, pas un conteneur MP4. On extrait le flux depuis
un MP4 HEVC (sur la machine de dev, avec ffmpeg — aucun réencodage) :

```bash
ffmpeg -i source.mp4 -c:v copy -bsf:v hevc_mp4toannexb -f hevc loading.hevc
```

## Affichage de la progression

Le programme lit à chaque frame le fichier `/tmp/progress` pour afficher le pourcentage
d'avancement de l'installation, **pile au centre de l'écran**.

Ce fichier est écrit par `auto-installer.sh` via `set_progress()` :

| Étape de l'installation | % affiché |
|---|---|
| Démarrage | 0% |
| Carte SD détectée | 5% |
| Réseau OK | 10% |
| Signature téléchargée | 20% |
| Flash en cours | 25% → 65% (+1%/15s) |
| Flash terminé | 90% |
| Signature vérifiée | 92% |
| Serveur confirmé | 95% |
| Reboot | 100% |

Si `/tmp/progress` n'existe pas ou est invalide, le dernier pourcentage connu est conservé
(pas de crash). La police TTF est chargée depuis `/usr/share/splash/font.ttf` (chemin absolu).

## Portabilité & Compilation

Le `.c` est compilé avec `gcc` puis linké avec `g++` (pour tirer libstdc++ requis par
libde265), en `-static`. Le binaire produit est **auto-suffisant** (~1,5 Mo strippé),
sans aucune `.so` dans le rootfs.

Le Makefile détecte le cross-compilateur Buildroot (`aarch64-linux-gcc/g++`) pour produire
un binaire **ARM64** compatible Raspberry Pi. Prérequis : le paquet `libde265` doit avoir
été construit une fois (`cd ../buildroot && make libde265`) pour fournir `libde265.a` +
headers dans `output/staging`.

Pour compiler (ARM64) :
```bash
make
```

Pour compiler, strip, ET copier binaire + video dans l'overlay Buildroot :
```bash
make install-video
```

## Utilisation

Le programme réécrivant la mémoire écran, il doit être lancé en administrateur.

```bash
sudo ./fb_video <chemin_vers_video.hevc>
```

S'il est lancé sans paramètre, il lit `loading.hevc` dans le dossier courant.
