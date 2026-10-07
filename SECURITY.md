# Security Policy

MiniLinux is a Buildroot-based recovery and provisioning environment
for Raspberry Pi systems with an emphasis on boot integrity, secure
deployment, and controlled remote access.

## Security Architecture

The project includes security mechanisms such as:

- Signed boot images
- Boot image signature verification before the card is made bootable
- HTTPS-based image and update delivery
- TLS certificate pinning
- Secure recovery and provisioning workflows
- SSH hardening (key-only root access, no password authentication)
- Integrity validation during installation and recovery

## Reporting a Vulnerability

If you discover a security vulnerability in MiniLinux, please do not
publish sensitive details immediately.

Contact the maintainer through the contact information provided on the
maintainer's GitHub profile.

Please include:

- A description of the issue
- Affected component
- Steps to reproduce
- Security impact
- Suggested remediation, if available

Reports will be reviewed and acknowledged as soon as possible.

## Maintainer

Security issues are handled by:

**Cristian Ursan (@NameLess0974)**

See [MAINTAINERS.md](MAINTAINERS.md) for maintainer responsibilities.

## Scope

Security-sensitive components include:

- Boot image generation and signing
- Boot verification
- Firmware and EEPROM configuration
- Image distribution
- TLS configuration
- Installer and recovery scripts
- SSH configuration
- Key handling
