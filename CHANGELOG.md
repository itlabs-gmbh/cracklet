# Changelog

All notable changes to cracklet are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- VM metadata: `cracklet new --owner NAME [--slot N]` labels a VM managed by a
  tool, and every new VM records its creation time. `ls` shows OWNER and AGE
  columns, `inspect` and `--json` report `owner`, `slot` and `created_at`.
- `cracklet gc` removes orphaned owned VMs, selected by `--owner` and
  `--older-than` with `--keep` exemptions and `--dry-run`, and deletes the
  grants and tokens left on the Mac for VMs that no longer exist. VMs without an
  owner are never collected, nor are VMs `ls` reports as `creating`; the agent
  re-checks owner and creation time under its lock before removing, so a VM
  recreated under the same name survives.
- `cracklet prepare --cpus/--memory/--disk` resizes an existing Lima VM in
  place (stop, `limactl edit`, start) instead of only applying at creation.
  Only explicitly given flags count, running microVMs block the restart, a
  host-side lock keeps `new`/`start` from racing it, and the disk can only grow.
- `cracklet exec NAME -- command [arg...]` runs a command with every argument
  shell-quoted, so the guest receives exactly that argv. `cracklet ssh NAME cmd`
  keeps plain-ssh semantics and passes a shell command line.
- Image profiles: `cracklet prepare --profile paseo` builds an 8G guest image
  with Node 22, `@getpaseo/cli`, Claude Code, git, gh and rsync (with
  `paseo.service` installed but disabled), and `cracklet new --profile paseo`
  restores its own golden snapshot. `--disk` now defaults to the profile's
  image size.
- Capability broker: `cracklet grant NAME CAP[:SCOPE]`, `revoke`, `grants` and
  `new --grant` let a microVM use GitHub, Claude Code or host-side MCP servers
  through a per-VM Unix socket that `cracklet ssh` forwards into the guest.
  Credentials stay on the Mac; the guest gets a placeholder token and a
  provisioned `/etc/environment`, files and JSON merges. Decisions are logged
  to `~/.cracklet/audit.log`.
- Capability files (`cracklet cap ls|show|lint|init|add`): TOML definitions
  with three primitives (`proxy`, `mcp`, `exec`), embedded defaults for
  `claude` and `github`, user overrides in `~/.cracklet/caps/`.
- `cracklet secret set|rm` stores credentials in the macOS Keychain for
  capabilities to reference as `keychain:cracklet/<name>`.
- `cracklet ls` shows a GRANTS column.
- `cracklet tunnel NAME` serves a VM's broker without an interactive session,
  reconnecting until interrupted or the VM stops. `cracklet ssh` reuses a
  running broker instead of failing on the already forwarded guest port, and
  takes the broker over when the session or tunnel that owned it ends.
- `cracklet inspect NAME` shows one microVM; `ls`, `new` and `inspect` take
  `--json` for scripting (progress messages then go to stderr).
- Apache-2.0 license, contributing guide, code of conduct, security policy,
  issue and pull request templates, and a CI workflow.
- `cracklet new` prints a ready-to-use `ssh NAME.cracklet` command.
- Golden snapshots: `cracklet new` restores a pre-booted VM in about 3 s
  instead of cold-booting; `--fresh` forces a cold boot.
- Port forwarding with `-p HOST:GUEST` on `new` and `cracklet forward` /
  `cracklet unforward`, backed by transient systemd socket units.
- `Include ~/.cracklet/ssh_config` lets plain `ssh NAME.cracklet` work via the
  Lima VM as jump host.

### Fixed

- The `github` capability sends `Basic x-access-token:<token>` instead of
  `Bearer`, which GitHub rejects for git over HTTPS. Capability templates gain
  a `basicauth` function for this: `{{ secret "ref" | basicauth "user" }}`.

### Security

- A new microVM never inherits the grants or placeholder token left on the Mac
  by an earlier VM of the same name; the broker authorises by name, so such
  leftovers would have handed the new VM the old one's capabilities.
- Firewall chains are rebuilt atomically with `iptables-restore`; CGNAT
  (`100.64.0.0/10`) is blocked alongside the other private ranges.
- Guests are internet-only: tap-to-tap traffic, the Lima VM's own services,
  the Mac and the LAN are unreachable; anti-spoofing rules drop packets with
  a source outside the guest subnet; IPv6 is disabled on every tap.
- The guest CRNG is reseeded from the Lima VM's `/dev/urandom` after every
  snapshot restore so clones diverge immediately.

[Unreleased]: https://github.com/itlabs-gmbh/cracklet/commits/main
