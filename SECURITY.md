# Security Policy

## Reporting a vulnerability

Please **do not** report security issues through public GitHub issues.

Use one of these private channels:

1. **GitHub private vulnerability reporting** (preferred):
   https://github.com/itlabs-gmbh/cracklet/security/advisories/new
2. **E-mail:** info@it-labs.de with the subject `[cracklet security]`

Include what you found, how to reproduce it, the cracklet version
(`go version -m $(command -v cracklet) | grep -E "mod|vcs.revision"`, or the
commit hash you built from) and your macOS and Lima versions.

You will get an acknowledgement within 5 working days. We will keep you
informed about the fix and credit you in the release notes unless you prefer
to stay anonymous.

## Supported versions

Only the latest release and `main` receive security fixes.

## What counts as a security issue

cracklet makes a few isolation promises (see "Network isolation" in the
README). A report is in scope if it shows that a microVM can:

- reach another microVM, the Lima VM's own services, the Mac, or the LAN,
- spoof a source address outside its `/30` guest subnet or use IPv6 on a tap,
- escape its systemd sandbox or gain privileges on the Lima VM,
- make a `cracklet` command on the Mac execute attacker-controlled input
  (for example through a crafted VM name, forward spec or guest output),
- influence another clone's state through a shared golden snapshot
  (for example predictable randomness after restore).

Also in scope: supply-chain issues such as the SHA-256 pins in
`internal/config` being bypassable, or downloads happening without
verification.

Out of scope: vulnerabilities in Firecracker, the Linux kernel, Ubuntu or
Lima themselves. Please report those upstream; we are happy to bump pinned
versions once a fix is released.
