# MiniLinux Threat Model

## Security Goals

MiniLinux is designed to provide a secure recovery and provisioning
environment for Raspberry Pi systems.

Primary security goals include:

1. Prevent unauthorized modification of boot images.
2. Detect corrupted or tampered system images.
3. Authenticate the deployment server.
4. Limit unauthorized remote administrative access.
5. Maintain a trusted recovery path following system failure.

## Threats

### Modified boot image

An attacker may attempt to replace or modify a boot image.

Mitigation:

- Images are cryptographically signed.
- Signatures are verified before the card is made bootable.

### Malicious deployment server / MITM

An attacker may attempt to impersonate the deployment infrastructure.

Mitigation:

- HTTPS is used for protected resources.
- The expected server certificate is pinned on the client.

### Unauthorized SSH access

Mitigation:

- Root access by key only; password authentication disabled.
- Shared host keys removed from the image and regenerated at boot.
- Hardened file permissions.

### Interrupted installation

A network or power failure may interrupt image deployment.

Mitigation:

- The boot signature is neutralized during the write and restored only
  after successful signature validation.
- Failed streaming operations restart cleanly rather than resuming
  potentially corrupted streams.

## Trust Boundaries

Trusted components include:

- Build environment
- Signing infrastructure
- Raspberry Pi firmware configuration
- Deployment server
- Embedded trusted certificate

## Out of Scope

The project does not claim to protect against compromise of the trusted
build or signing infrastructure, nor against theft of a deployed device
(storage encryption is not provided).
