package handlers

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// HandleAPIImageUpload recoit une nouvelle image, la signe et la publie.
//
// Corps = octets bruts de l'image (pas de multipart : 10 Go en memoire ou en
// spool disque serait inutilement couteux). Ecriture en .tmp puis rename
// atomique : un Pi en cours de flash ne peut jamais lire une image partielle.
func (h *Handlers) HandleAPIImageUpload(w http.ResponseWriter, r *http.Request) {
	if !h.RequireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "POST only"})
		return
	}
	if h.cfg.BootPrivateKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "cle privee non configuree (BOOT_PRIVATE_KEY)"})
		return
	}

	dir := h.cfg.ImagesDir

	// Refuser avant d'ecrire plutot que de remplir le disque : la partition
	// heberge aussi la base et les logs. Marge de 1 Go conservee.
	if r.ContentLength > 0 {
		free, err := freeSpace(dir)
		if err == nil && r.ContentLength+(1<<30) > int64(free) {
			h.logger.Warn("upload image refuse: espace disque insuffisant",
				"requis", r.ContentLength, "libre", free)
			writeJSON(w, http.StatusInsufficientStorage, map[string]any{
				"ok": false, "error": "espace disque insuffisant"})
			return
		}
	}

	tmpPath := filepath.Join(dir, imageFile+".tmp")

	tmp, err := os.Create(tmpPath)
	if err != nil {
		h.logger.Error("upload image: creation du fichier temporaire", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	// En cas d'echec le .tmp ne doit pas rester : il serait re-uploade par-dessus
	// mais occuperait 10 Go entre-temps.
	defer func() {
		tmp.Close()
		os.Remove(tmpPath)
	}()

	// Hash calcule pendant la copie : relire 10 Go ensuite doublerait la duree.
	sum := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, sum), r.Body)
	if err != nil {
		h.logger.Error("upload image: transfert interrompu", "bytes", written, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "transfert interrompu"})
		return
	}
	if written == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "corps vide"})
		return
	}
	if err := tmp.Sync(); err != nil {
		h.logger.Error("upload image: sync", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	tmp.Close()

	hashHex := hex.EncodeToString(sum.Sum(nil))
	// Meme format que `sha256sum ... | awk '{print $1}' > hash.txt`.
	hashPayload := []byte(hashHex + "\n")

	sig, err := h.signPayload(hashPayload)
	if err != nil {
		h.logger.Error("upload image: signature", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "signature impossible"})
		return
	}

	// On refuse de publier une image dont on n'a pas verifie sa propre
	// signature : sinon tout le parc echouerait au flash.
	if err := h.verifyImageSignature(hashPayload, sig); err != nil {
		h.logger.Error("upload image: signature auto-verifiee invalide", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "signature invalide"})
		return
	}

	// L'image d'abord : ecrire hash.txt avant le rename laisserait, en cas
	// d'echec, une signature decrivant la nouvelle image alors que l'ancienne
	// est encore servie — tous les Pi la refuseraient.
	if err := os.Rename(tmpPath, filepath.Join(dir, imageFile)); err != nil {
		h.logger.Error("upload image: publication", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}

	// Entre ce point et l'ecriture de la signature, l'image ne correspond plus a
	// hash.txt. La fenetre est de quelques millisecondes (deux petits fichiers)
	// et un echec est trace explicitement : l'etat est visible dans la console
	// via sig_valid.
	if err := os.WriteFile(filepath.Join(dir, hashFile), hashPayload, 0o644); err != nil {
		h.logger.Error("upload image: image publiee mais hash.txt non ecrit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok": false, "error": "image publiee mais hash.txt non ecrit"})
		return
	}
	if err := os.WriteFile(filepath.Join(dir, sigFile), sig, 0o644); err != nil {
		h.logger.Error("upload image: image publiee mais signature non ecrite", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok": false, "error": "image publiee mais signature non ecrite"})
		return
	}

	h.logger.Warn("nouvelle image publiee", "bytes", written, "sha256", hashHex)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "size": written, "hash": hashHex, "sig_valid": true,
	})
}

// signPayload signe en RSA PKCS#1 v1.5 sur SHA-256, comme openssl dgst.
func (h *Handlers) signPayload(payload []byte) ([]byte, error) {
	pemBytes, err := os.ReadFile(h.cfg.BootPrivateKey)
	if err != nil {
		return nil, errors.New("cle privee illisible")
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("cle privee: PEM invalide")
	}

	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("cle privee illisible")
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("cle privee non RSA")
		}
		key = rsaKey
	}

	digest := sha256.Sum256(payload)
	return rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
}

// freeSpace renvoie l'espace disponible sur la partition du repertoire.
func freeSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
