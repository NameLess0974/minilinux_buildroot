package handlers

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Le Pi telecharge imageFile + sigFile. hashFile reste sur le serveur : c'est
// lui qui est signe (voir data/images/readme.md).
const (
	imageFile = "final_image.img.xz"
	sigFile   = "final_image.sig"
	hashFile  = "hash.txt"
)

// imageInfo decrit l'image servie au parc. L'etat de signature est explicite :
// une image remplacee sans re-signature se verrait sinon seulement quand tous
// les Pi refusent de flasher.
type imageInfo struct {
	Name     string `json:"name"`
	Present  bool   `json:"present"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"` // RFC3339, empty if absent

	// Hash lu dans hash.txt. Non recalcule depuis l'image : hasher 10 Go prend
	// ~1 min et cet endpoint est poll.
	Hash string `json:"hash"`

	SigPresent  bool `json:"sig_present"`
	HashPresent bool `json:"hash_present"`

	// SigValid : la signature verifie hash.txt avec la cle publique. Prouve que
	// hash.txt est authentique, pas que l'image correspond a hash.txt.
	SigValid bool   `json:"sig_valid"`
	SigError string `json:"sig_error,omitempty"`
}

// HandleAPIImages renvoie l'identite et l'etat de signature de l'image servie.
// Lecture seule : publier une image reste une operation serveur deliberee.
func (h *Handlers) HandleAPIImages(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}

	dir := h.cfg.ImagesDir
	info := imageInfo{Name: imageFile}

	if stat, err := os.Stat(filepath.Join(dir, imageFile)); err == nil {
		info.Present = true
		info.Size = stat.Size()
		info.Modified = stat.ModTime().UTC().Format(time.RFC3339)
	}

	hashBytes, hashErr := os.ReadFile(filepath.Join(dir, hashFile))
	if hashErr == nil {
		info.HashPresent = true
		info.Hash = strings.TrimSpace(string(hashBytes))
	}

	sigBytes, sigErr := os.ReadFile(filepath.Join(dir, sigFile))
	if sigErr == nil {
		info.SigPresent = true
	}

	switch {
	case !info.HashPresent:
		info.SigError = "hash.txt missing"
	case !info.SigPresent:
		info.SigError = "final_image.sig missing"
	default:
		// Verification sur les octets bruts de hash.txt (newline incluse), comme
		// openssl dgst -sha256 -sign ... -out final_image.sig hash.txt
		if err := h.verifyImageSignature(hashBytes, sigBytes); err != nil {
			info.SigError = err.Error()
		} else {
			info.SigValid = true
		}
	}

	if info.SigError != "" {
		h.logger.Warn("image signature not valid", "reason", info.SigError)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "image": info})
}

// verifyImageSignature verifie sig contre le contenu de hash.txt. Seule la cle
// publique est chargee : la signature reste hors ligne.
func (h *Handlers) verifyImageSignature(payload, sig []byte) error {
	pubPEM, err := os.ReadFile(h.cfg.BootPublicKey)
	if err != nil {
		return errors.New("public key unreadable")
	}
	block, _ := pem.Decode(pubPEM)
	if block == nil {
		return errors.New("public key is not valid PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.New("public key unparseable")
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return errors.New("public key is not RSA")
	}

	digest := sha256.Sum256(payload)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return errors.New("signature does not match hash.txt")
	}
	return nil
}
