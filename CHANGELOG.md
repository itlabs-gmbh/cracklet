# Changelog

All notable changes to cracklet are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Apache-2.0 license, contributing guide, code of conduct, security policy,
  issue and pull request templates, and a CI workflow.
- `cracklet new` prints a ready-to-use `ssh NAME.cracklet` command.
- Golden snapshots: `cracklet new` restores a pre-booted VM in about 3 s
  instead of cold-booting; `--fresh` forces a cold boot.
- Port forwarding with `-p HOST:GUEST` on `new` and `cracklet forward` /
  `cracklet unforward`, backed by transient systemd socket units.
- `Include ~/.cracklet/ssh_config` lets plain `ssh NAME.cracklet` work via the
  Lima VM as jump host.

### Security

- Firewall chains are rebuilt atomically with `iptables-restore`; CGNAT
  (`100.64.0.0/10`) is blocked alongside the other private ranges.
- Guests are internet-only: tap-to-tap traffic, the Lima VM's own services,
  the Mac and the LAN are unreachable; anti-spoofing rules drop packets with
  a source outside the guest subnet; IPv6 is disabled on every tap.
- The guest CRNG is reseeded from the Lima VM's `/dev/urandom` after every
  snapshot restore so clones diverge immediately.

[Unreleased]: https://github.com/itlabs-gmbh/cracklet/commits/main
