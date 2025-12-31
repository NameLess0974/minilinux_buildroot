# Signature de l'image finale (final_image.img.xz)

  ## Comment ça marche

  L'auto-installer vérifie que l'image téléchargée provient bien de vous et n'a pas été modifiée.

  **Processus :**
  1. Le serveur calcule le **hash** de l'image et le **signe** avec votre clé privée
  2. Le Pi télécharge l'image en **streaming** et calcule le hash pendant le flash
  3. Le Pi **vérifie** que le hash correspond à la signature (avec votre clé publique)

  **Sécurité :** Impossible de flasher une image modifiée, même en cas d'attaque Man-in-the-Middle.

  ---

  ## Signer une nouvelle image

  Sur le serveur HTTP :

  ```bash
  # 1. Calculer le hash de l'image
  sha256sum images/final_image.img.xz | awk '{print $1}' > images/hash.txt

  # 2. Signer le hash avec votre clé privée
  openssl dgst -sha256 -sign bootkey-private.pem -out images/final_image.sig images/hash.txt

  ---
  Vérifier la signature (optionnel)

  Pour tester que la signature est valide :

  openssl dgst -sha256 -verify bootkey-public.pem -signature images/final_image.sig images/hash.txt

  Résultat attendu : Verified OK

  ---
  Fichiers nécessaires sur le serveur

  images/
  ├── final_image.img.xz    # Image à flasher
  ├── final_image.sig        # Signature du hash (requis)
  └── hash.txt               # Hash de l'image (utilisé pour signer)

  Note : Le Pi télécharge seulement final_image.img.xz et final_image.sig. Le fichier hash.txt reste sur le serveur.
  ```