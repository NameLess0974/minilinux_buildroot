# fb_video_player

Un programme en C minimaliste permettant de jouer une video **H.265 (HEVC)** directement
sur l'écran d'un système Linux, sans passer par une interface graphique lourde comme X11
ou Wayland, ni aucune dépendance runtime (le décodeur libde265 est linké en statique).

> Le dossier s'appelait historiquement `fb_gif/` (il jouait un GIF) ; il a été renommé
> `fb_video/` car il joue désormais une video HEVC via `fb_video.c`.

## Fonctionnement

Le binaire communique directement avec le Framebuffer Linux (`/dev/fb0`). Au lancement :
1. L'écran texte de la console est basculé en mode graphique pur pour empêcher la frappe clavier d'être affichée.
2. L'espace de la mémoire vidéo est projeté dans l'espace mémoire du programme C via `mmap()`.
3. Le flux HEVC brut (Annex-B) est chargé en RAM, puis **re-décodé en boucle** : chaque image est convertie de YUV420 vers RGB et copiée dans un back buffer (double-buffering), envoyé à l'écran à ~30 fps.

## Technologies utilisées

- **Linux API Base** : `<linux/fb.h>` et `/dev/tty0` pour lire les caractéristiques de l'écran, gérer le curseur texte, et basculer l'affichage en tableau de pixels.
- **libde265** : décodeur H.265 open source (paquet Buildroot, activé dans `.config`), linké **statiquement** depuis `output/staging`. Configuré en décodeur seul, sans SDL ni encodeur ; aucun artefact n'est laissé dans le rootfs (voir `package/libde265/libde265.mk`).
- **stb_truetype.h** : rendu de texte vectoriel anti-aliasé pour afficher le pourcentage d'avancement.

## Préparer la video — config d'encodage pour un rendu net & clean

Le décodeur lit du HEVC **brut (Annex-B)**, pas un conteneur MP4.

Le plus simple (extraction sans réencodage) est de recopier le flux HEVC :

```bash
ffmpeg -i source.mp4 -c:v copy -bsf:v hevc_mp4toannexb -f hevc loading.hevc
```

**MAIS** cette recopie garde tel quel le flux source. Si la source contient du **banding**
(dégradés sombres qui se cassent en anneaux/paliers) ou des **artefacts de compression sur
les dégradés en mouvement** (halos lumineux qui « grouillent »), ils resteront visibles à
l'écran. C'est le principal facteur de qualité — bien plus que le rendu du framebuffer.

### Config recommandée (réencodage propre)

Cible : **HEVC 8-bit, BT.709, débit élevé, anti-banding**. Réglages testés sur le splash :

```bash
ffmpeg -i source.mp4 -an \
  -vf "deband=1thr=0.02:2thr=0.02:3thr=0.02:4thr=0.02:range=16:blur=1,format=yuv420p" \
  -c:v libx265 -preset slow -crf 12 \
  -x265-params "colorprim=bt709:transfer=bt709:colormatrix=bt709:range=limited" \
  -color_primaries bt709 -color_trc bt709 -colorspace bt709 \
  -tag:v hvc1 -bsf:v hevc_mp4toannexb -f hevc \
  loading.hevc
```

Pourquoi chaque paramètre :

| Paramètre | Rôle |
|---|---|
| `deband=...` | Filtre **anti-banding vidéo** (temporel) : lisse les dégradés sombres sans « grouillement » entre frames. Préférer `deband` à `gradfun` : gradfun débande image par image et crée des taches mouvantes sur les zones en mouvement. |
| `-crf 12` | Quasi sans perte. Le débit monte, mais la taille finale reste modeste (splash court). Un CRF plus haut (18-23) réintroduit du banding sur les noirs. |
| `-preset slow` | Meilleure allocation des bits sur les dégradés que `fast`/`medium`. |
| `colorprim/transfer/colormatrix = bt709` | **Marque explicitement BT.709** (Rec.709). `fb_video.c` convertit en BT.709 ; une source non marquée ou marquée 601 décale les teintes (verts/rouges). |
| `range=limited` | Range TV (Y 16..235). Cohérent avec la conversion du décodeur. |
| `-an` | Pas d'audio (inutile pour un splash). |
| `hevc_mp4toannexb -f hevc` | Sort du **HEVC brut Annex-B** directement lisible par `fb_video`. |

### Si la source vient d'un moteur de rendu (Remotion, After Effects…)

**Le mieux est de corriger le banding À LA SOURCE**, avant l'encodage HEVC :
- Exporter la source en **sans perte** (ProRes 4444, PNG/EXR sequence, ou 10-bit) plutôt qu'un
  MP4 HEVC déjà compressé — le HEVC 8-bit à bas débit crée du banding dans les noirs qu'aucun
  filtre ne récupère parfaitement ensuite.
- Ajouter un léger **dither / grain** dans le compositing sur les dégradés sombres.
- Puis appliquer la commande d'encodage ci-dessus sur cette source propre.

> Note résolution : le splash est encodé en **1920×1080** et **centré** (pas d'upscale) sur
> l'écran. Sur un écran 4K, l'image 1080p est affichée à sa taille native au centre — la
> netteté perçue ne dépend donc pas de la résolution de l'écran mais de la qualité du flux.

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
