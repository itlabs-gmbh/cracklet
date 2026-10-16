# Security Policy

## Reporting a vulnerability

Please **do not** report security issues through public GitHub issues.

Use one of these private channels:

1. **GitHub private vulnerability reporting** (preferred):
   https://github.com/itlabs-gmbh/skiff/security/advisories/new
2. **E-mail:** info@it-labs.de with the subject `[skiff security]`

Include what you found, how to reproduce it, the skiff version
(`go version -m $(command -v skiff) | grep -E "mod|vcs.revision"`, or the
commit hash you built from), your macOS version and `container --version`
(or the Lima version on the deprecated Lima backend).

You will get an acknowledgement within 5 working days. We will keep you
informed about the fix and credit you in the release notes unless you prefer
to stay anonymous.

## Supported versions

Only the latest release and `main` receive security fixes.

## What counts as a security issue

skiff makes a few isolation promises (see "Network isolation" in the
README). A report is in scope if it shows that a VM can:

- pose as another VM towards `skiff ssh`, grants or the broker tunnel
  despite the host key pinned at creation,
- reach the Mac or the LAN past the in-guest firewall without being root in
  the VM (root can lift that guard rail by design),
- receive another VM's forwarded connections or broker tunnel,
- make a `skiff` command on the Mac execute attacker-controlled input
  (for example through a crafted VM name, forward spec, host key line or
  guest output),
- on the deprecated Lima backend: reach another microVM, the Lima VM's own
  services, the Mac or the LAN, spoof a source address, escape its systemd
  sandbox, or influence another clone through a shared golden snapshot.

Also in scope: supply-chain issues such as the SHA-256 pins in
`internal/config` being bypassable, or downloads happening without
verification.

Out of scope: vulnerabilities in Apple's `container`, the Virtualization
framework, the Linux kernel, Ubuntu, Firecracker or Lima themselves. Please
report those upstream; we are happy to bump pinned versions once a fix is
released.
