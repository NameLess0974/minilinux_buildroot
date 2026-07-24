# Signature de l'image finale (final_image.img.xz)

## Comment ça marche

L'auto-installer vérifie que l'image téléchargée provient bien de vous et n'a pas
été modifiée.

Processus :
1. Le serveur calcule le hash de l'image et le signe avec la clé privée.
2. Le Pi télécharge l'image en streaming et calcule le hash pendant le flash.
3. Le Pi vérifie que le hash correspond à la signature (avec la clé publique).

Sécurité : impossible de flasher une image modifiée, même en cas d'attaque
Man-in-the-Middle. L'image transite par HTTPS (pin du certificat) et la signature
RSA garantit l'intégrité.

## Signer une nouvelle image

Depuis la racine du projet :

```bash
cd data/images

# 1. Calculer le hash de l'image
sha256sum final_image.img.xz | awk '{print $1}' > hash.txt

# 2. Signer le hash avec la clé privée
openssl dgst -sha256 -sign ../../private/keys/bootkey-private.pem \
  -out final_image.sig hash.txt
```

## Vérifier la signature (optionnel)

```bash
openssl dgst -sha256 -verify ../../private/keys/bootkey-public.pem \
  -signature final_image.sig hash.txt
# Résultat attendu : Verified OK
```

## Fichiers dans data/images/

```
data/images/
├── final_image.img.xz    # Image à flasher (URL /images/final_image.img.xz)
├── final_image.sig       # Signature du hash (requis)
├── hash.txt              # Hash de l'image (utilisé pour signer)
└── readme.md             # Ce fichier
```

Le Pi télécharge seulement final_image.img.xz et final_image.sig (via HTTPS).
hash.txt et readme.md restent sur le serveur.
