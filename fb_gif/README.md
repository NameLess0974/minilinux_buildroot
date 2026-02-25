# fb_gif_player

Un programme en C minimaliste permettant de jouer une animation GIF directement sur l'écran d'un système Linux, sans passer par une interface graphique lourde comme X11 ou Wayland.

## Fonctionnement

Le binaire communique directement avec le Framebuffer Linux (`/dev/fb0`). Au lancement :
1. L'écran texte de la console est basculé en mode graphique pur pour empecher la frappe clavier d'etre affichée.
2. L'espace de la mémoire vidéo est projeté dans l'espace mémoire du programme C via la fonction `mmap()`.
3. L'image est décodée, redimensionnée et copiée dans la mémoire écran à intervalles réguliers de la frame.

## Technologies utilisées

- **Linux API Base** : Utilisation de `<linux/fb.h>` et `/dev/tty0` pour lire les caractéristiques de l'écran, gérer les curseurs texte, et modifier le type de rendu de l'écran en un tableau de pixels.
- **stb_image.h** : Une bibliothèque de décodage d'image développée en un seul fichier (header-only). Elle est responsable de l'ouverture et du décodage du l'algorithme GIF pour extraire le délai et les images RVB, évitant ainsi de s'attacher à des dépendances systèmes massives de rendu.
- **stb_truetype.h** : Rendu de texte vectoriel anti-aliasé pour afficher le pourcentage d'avancement en police lisse.

## Affichage de la progression

Le programme lit à chaque frame le fichier `/tmp/progress` pour afficher le pourcentage d'avancement de l'installation.

Ce fichier est écrit par `auto-installer.sh` via la fonction `set_progress()` :

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

Si `/tmp/progress` n'existe pas ou est invalide, le dernier pourcentage connu est conservé (pas de crash).

La police TTF est chargée depuis `/usr/share/splash/font.ttf` (chemin absolu, indépendant du répertoire courant du service systemd).

## Portabilité & Compilation

Le Makefile fourni associe la compilation mathématique et lie le programme de manière statique avec l'indicateur `-static`. Le rendu produit est donc un fichier simple de moins d'1Mo auto-suffisant.

Le Makefile détecte automatiquement le cross-compilateur Buildroot (`aarch64-linux-gcc`) pour produire un binaire **ARM64** compatible Raspberry Pi. Si le cross-compilateur est absent, il utilise `gcc` natif (x86, pour tests locaux uniquement).

Pour compiler (ARM64) :
```bash
make
```

Pour compiler ET copier directement dans l'overlay Buildroot :
```bash
make install
```

## Utilisation

Le programme réécrivant la mémoire écran du système, il est nécessaire de le lancer en administrateur.

```bash
sudo ./fb_gif <chemin_vers_gif>
```

S'il est lancé sans paramètre, il lit `loading.gif` du dossier courant de la racine.
