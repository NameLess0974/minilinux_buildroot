#!/bin/bash
# Script auto-installer - TEST SSHD

exec > /dev/tty1 2>&1

clear
echo "=============================================="
echo "AUTO-INSTALLER - DEMARRAGE SSHD"
echo "=============================================="
date
echo ""

sleep 2

echo "--- FIX PERMISSIONS ---"
chmod 600 /etc/ssh/ssh_host_*_key
chmod 644 /etc/ssh/ssh_host_*_key.pub
chmod 755 /etc/ssh
mkdir -p /var/empty
chmod 755 /var/empty
chown root:root /var/empty
ls -la /etc/ssh/ssh_host_*key
echo ""

echo "--- TEST 1: LANCEMENT SSHD NORMAL ---"
echo "Commande: /usr/sbin/sshd"
/usr/sbin/sshd 2>&1 | head -10
RESULT1=$?
echo "Return code: $RESULT1"
sleep 2
echo ""

echo "--- PORT 22 (APRES TEST 1) ---"
if ss -tulpn | grep -q ":22 "; then
    echo "Port 22: OUVERT - SSHD DEMARRE!"
    ss -tulpn | grep ":22 "
else
    echo "Port 22: FERME - SSHD N'A PAS DEMARRE"
    echo ""
    echo "--- TEST 2: LANCEMENT SSHD MODE DEBUG ---"
    echo "Commande: /usr/sbin/sshd -D -e"
    /usr/sbin/sshd -D -e &
    SSHD_PID=$!
    echo "sshd PID: $SSHD_PID"
    sleep 3
    if ss -tulpn | grep -q ":22 "; then
        echo "Port 22: OUVERT avec mode debug"
        ss -tulpn | grep ":22 "
    else
        echo "Port 22: TOUJOURS FERME - ECHEC TOTAL"
    fi
fi
echo ""

echo "--- RESEAU ---"
ip addr show | grep -A 3 "eth0\|end0"
echo ""

echo "=============================================="
echo "User: sab (mot de passe configure)  ou  root par cle: keys/minilinux-root"
echo "=============================================="

exit 0
