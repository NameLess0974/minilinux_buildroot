#!/bin/sh

set -e

mkdir -p "${TARGET_DIR}/boot/auto"
mkdir -p "${TARGET_DIR}/boot/boota"
mkdir -p "${TARGET_DIR}/boot/bootb"

# --- SSH : corriger les permissions des cles hote ---
# L'overlay fournit les cles en 600, mais Buildroot les recopie en 644 dans le
# rootfs. OpenSSH REFUSE de demarrer avec des cles privees lisibles par tous, ce
# qui faisait echouer sshd.service (d'ou l'ancien contournement sshd-debug.sh).
# On force ici les bonnes permissions pour un sshd natif fonctionnel.
if [ -d "${TARGET_DIR}/etc/ssh" ]; then
    chmod 600 "${TARGET_DIR}"/etc/ssh/ssh_host_*_key 2>/dev/null || true
    chmod 644 "${TARGET_DIR}"/etc/ssh/ssh_host_*_key.pub 2>/dev/null || true
    chmod 755 "${TARGET_DIR}/etc/ssh"
fi

# --- SSH : permissions du repertoire .ssh et authorized_keys ---
# sshd refuse une cle si .ssh n'est pas 700 ou authorized_keys trop ouvert.
# On force les bonnes permissions pour l'auth par cle de root.
# NB : pas de chown ici (la machine de build n'est pas root ; Buildroot applique
# root:root au rootfs final). Seules les permissions comptent.
if [ -d "${TARGET_DIR}/root/.ssh" ]; then
    chmod 700 "${TARGET_DIR}/root/.ssh"
    chmod 600 "${TARGET_DIR}/root/.ssh/authorized_keys" 2>/dev/null || true
fi

# /var/empty : requis par sshd pour le privilege separation (doit exister, root:root, 755)
mkdir -p "${TARGET_DIR}/var/empty"
chmod 755 "${TARGET_DIR}/var/empty"
rm -f "${TARGET_DIR}/var/empty/.gitkeep"  # artefact de versionnage, inutile au runtime
