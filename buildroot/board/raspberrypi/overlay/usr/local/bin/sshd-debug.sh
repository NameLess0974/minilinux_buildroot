#!/bin/bash
# SSHD auto-start - Silent background mode

# Fix permissions
chmod 600 /etc/ssh/ssh_host_*_key 2>/dev/null
chmod 644 /etc/ssh/ssh_host_*_key.pub 2>/dev/null
chmod 755 /etc/ssh 2>/dev/null
mkdir -p /var/empty 2>/dev/null
chmod 755 /var/empty 2>/dev/null
chown root:root /var/empty 2>/dev/null

# Start sshd silently
/usr/sbin/sshd >/dev/null 2>&1

exit 0
