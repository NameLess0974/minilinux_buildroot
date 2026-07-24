package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"minilinux-server/internal/config"
)

const projectRoot = "/home/sabuser/minilinux_buildroot"

// La signature porte sur le CONTENU de hash.txt (et non sur les octets de
// l'image) : c'est le schema documente dans data/images/readme.md et produit par
// openssl. Une erreur ici ferait refuser l'image par tous les Pi, donc on la
// verrouille par un test contre les vrais fichiers.
func TestVerifyImageSignature(t *testing.T) {
	pub := filepath.Join(projectRoot, "private/keys/bootkey-public.pem")
	if _, err := os.Stat(pub); err != nil {
		t.Skip("cle publique absente dans cet environnement")
	}
	h := &Handlers{cfg: &config.Config{BootPublicKey: pub}}

	payload, err := os.ReadFile(filepath.Join(projectRoot, "data/images/hash.txt"))
	if err != nil {
		t.Skip("hash.txt absent")
	}
	sig, err := os.ReadFile(filepath.Join(projectRoot, "data/images/final_image.sig"))
	if err != nil {
		t.Skip("final_image.sig absent")
	}

	if err := h.verifyImageSignature(payload, sig); err != nil {
		t.Fatalf("signature reelle rejetee (openssl la valide): %v", err)
	}

	// Hash altere : doit etre rejete.
	tampered := append([]byte{}, payload...)
	tampered[0] ^= 0xff
	if err := h.verifyImageSignature(tampered, sig); err == nil {
		t.Error("hash altere accepte")
	}

	// Signature tronquee : doit etre rejetee, sans paniquer.
	if err := h.verifyImageSignature(payload, sig[:len(sig)-1]); err == nil {
		t.Error("signature tronquee acceptee")
	}

	// Signature vide : doit etre rejetee, sans paniquer.
	if err := h.verifyImageSignature(payload, nil); err == nil {
		t.Error("signature vide acceptee")
	}
}
