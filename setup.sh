#!/bin/bash
# Setup script for minilinux Buildroot project

set -e

BUILDROOT_VERSION="2023.11.1"
BUILDROOT_URL="https://buildroot.org/downloads/buildroot-2023.11.1.tar.gz"

# Check if we're in the right directory
if [ ! -d "buildroot" ]; then
    echo "Error: buildroot/ directory not found. Run this from minilinux root."
    exit 1
fi

# Check if buildroot already has Makefile (already set up)
if [ -f "buildroot/Makefile" ]; then
    echo "Buildroot already set up."
    echo "Run: cd buildroot && make"
    exit 0
fi

# Backup existing configs
echo "Backing up existing configs..."
BACKUP_DIR=$(mktemp -d)
cp -r buildroot/board "$BACKUP_DIR/" 2>/dev/null || true
cp buildroot/.config "$BACKUP_DIR/" 2>/dev/null || true
cp buildroot/.gitignore "$BACKUP_DIR/" 2>/dev/null || true
mkdir -p "$BACKUP_DIR/output/host/bin/" 2>/dev/null || true
cp buildroot/output/host/bin/rpi-eeprom-digest "$BACKUP_DIR/output/host/bin/" 2>/dev/null || true

# Download Buildroot
echo "Downloading Buildroot ${BUILDROOT_VERSION}..."
wget -q --show-progress -O /tmp/buildroot.tar.gz "${BUILDROOT_URL}"

# Extract Buildroot
echo "Extracting Buildroot..."
tar -xzf /tmp/buildroot.tar.gz --strip-components=1 -C buildroot/

# Restore our custom configs (overwrite Buildroot defaults)
echo "Restoring custom configs..."
cp -r "$BACKUP_DIR/board/"* buildroot/board/ 2>/dev/null || true
cp "$BACKUP_DIR/.config" buildroot/.config 2>/dev/null || true
cp "$BACKUP_DIR/.gitignore" buildroot/.gitignore 2>/dev/null || true
mkdir -p buildroot/output/host/bin/ 2>/dev/null || true
cp "$BACKUP_DIR/output/host/bin/rpi-eeprom-digest" buildroot/output/host/bin/ 2>/dev/null || true

# Cleanup
rm -rf "$BACKUP_DIR" /tmp/buildroot.tar.gz

echo "Run: cd buildroot && make"
