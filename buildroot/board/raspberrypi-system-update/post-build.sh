#!/bin/sh

set -e

mkdir -p "${TARGET_DIR}/boot/auto"
mkdir -p "${TARGET_DIR}/boot/boota"
mkdir -p "${TARGET_DIR}/boot/bootb"

# Les cles hote ne sont plus dans l'overlay : sshd.service les genere au boot
# via "ssh-keygen -A" (identite unique par machine).
rm -f "${TARGET_DIR}"/etc/ssh/ssh_host_*_key "${TARGET_DIR}"/etc/ssh/ssh_host_*_key.pub
chmod 755 "${TARGET_DIR}/etc/ssh" 2>/dev/null || true

# sshd refuse la cle si les permissions sont trop ouvertes.
if [ -d "${TARGET_DIR}/root/.ssh" ]; then
    chmod 700 "${TARGET_DIR}/root/.ssh"
    chmod 600 "${TARGET_DIR}/root/.ssh/authorized_keys" 2>/dev/null || true
fi

# /var/empty : requis par sshd pour le privilege separation (doit exister, root:root, 755)
mkdir -p "${TARGET_DIR}/var/empty"
chmod 755 "${TARGET_DIR}/var/empty"
rm -f "${TARGET_DIR}/var/empty/.gitkeep"  # artefact de versionnage, inutile au runtime
