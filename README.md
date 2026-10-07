# cracklet – Firecracker microVMs on your Mac

[![CI](https://github.com/itlabs-gmbh/cracklet/actions/workflows/ci.yml/badge.svg)](https://github.com/itlabs-gmbh/cracklet/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/itlabs-gmbh/cracklet.svg)](https://pkg.go.dev/github.com/itlabs-gmbh/cracklet)

`cracklet` gives you the "ssh into a fresh microVM in a second" feeling on Apple
Silicon. It runs **real, unmodified Firecracker** by placing a single Lima VM
with nested virtualization between macOS and the microVMs:

```
macOS (cracklet)
 └── Lima VM "cracklet"  (Ubuntu 24.04, vz driver, nestedVirtualization: true → /dev/kvm)
      ├── firecracker v1.17.0 + guest kernel + Ubuntu 24.04 base image
      ├── microVM vm1  (tap cracklet1, 172.16.1.2, systemd unit cracklet-vm-vm1)
      ├── microVM vm2  (tap cracklet2, 172.16.2.2, systemd unit cracklet-vm-vm2)
      └── ...
```

Every microVM gets its own kernel, root disk, IP address and SSH access. The
host side (`cracklet`) is a small Go CLI; the guest side is a bash agent that cracklet
installs into the Lima VM and keeps up to date automatically.

## Requirements

- Apple M3 or newer (nested virtualization is a hardware feature of M3+)
- macOS 15 or newer
- Homebrew (cracklet installs Lima through it if needed)
- Go 1.26+ to build cracklet

Check with `cracklet doctor`.

## Install

```sh
go install github.com/itlabs-gmbh/cracklet/cmd/cracklet@latest
cracklet prepare           # one-time setup, a few minutes
```

Or from a checkout: `make install` (go install ./cmd/cracklet → `$(go env GOPATH)/bin/cracklet`).

`cracklet prepare` is idempotent. It:

1. verifies the host (chip, macOS version, Hypervisor framework, Lima),
2. generates an SSH key pair in `~/.cracklet/`,
3. creates and boots the Lima VM from an embedded template
   (`--cpus`, `--memory`, `--disk` size it; defaults 4 / 8 GiB / 40 GiB),
4. installs Firecracker, downloads the guest kernel and the Ubuntu rootfs
   (all three verified against SHA-256 digests pinned in `internal/config`),
   injects your public key and builds a sparse ext4 base image,
5. writes `~/.cracklet/ssh_config`.

The Lima VM mounts nothing from the Mac; files travel over `limactl shell`.

## Usage

```sh
cracklet new                    # boots vm1 (2 vCPUs, 1 GiB RAM, 2G disk)
cracklet new dev --vcpus 4 --mem 2048 --disk 8G
cracklet ls
cracklet ls --json              # also: new --json, inspect --json
cracklet inspect dev            # details of one VM
cracklet ssh dev                # interactive root shell
cracklet ssh dev uname -a       # run a command
cracklet stop dev               # keep the disk
cracklet start dev
cracklet rm dev                 # stop and delete
```

### Golden snapshots

`cracklet new` does not boot: it restores a *golden snapshot*, a fully booted VM
(sshd and the guest daemon `cracklet-envd` running) frozen together with its
memory. The first `cracklet new` for a given `--vcpus`/`--mem` combination builds
that snapshot (about 15 s), every later one restores it in roughly 3 s.

After the restore the clone still carries the snapshot's IP, hostname and
clock. The agent sends the real identity over vsock (Firecracker's Unix socket
with the `CONNECT 52` handshake) and `cracklet-envd` applies it with netlink and
syscalls, without spawning a process: right after a restore every page the
guest touches is a nested page fault, so even `ip addr add` costs hundreds of
milliseconds there.

`--fresh` forces a cold boot; a `--disk` other than the profile's image size
implies it. Snapshots are keyed on the profile image, kernel, Firecracker version and the
agent revision, so they rebuild automatically after `cracklet prepare` changes any
of them. `cracklet-envd` is cross-compiled for linux/arm64 and embedded into cracklet
(`make envd`). A cracklet installed with `go install ...@latest` has no embedded copy;
`cracklet prepare` then cross-compiles the daemon from the module cache with your Go
toolchain, at the same module version as the CLI.

### Image profiles

`--profile` picks the guest image. `base` is the plain Ubuntu 24.04 image (curl
and python3, 2G). `paseo` adds Node 22, `@getpaseo/cli`, Claude Code (native
installer), git, gh and rsync on an 8G image. Build a profile once, then use it:

```sh
cracklet prepare --profile paseo   # chroot install from apt/npm/claude.ai, a few minutes
cracklet new agent --profile paseo # restores the paseo golden snapshot
```

A profile image is built at its full size because a golden snapshot can only
be restored onto a disk of exactly that size; `--disk` defaults to it.
`paseo.service` (`paseo daemon run --home /root/.paseo`) is installed but not
enabled, so the snapshot carries no daemon keypair; start it per VM with
`systemctl enable --now paseo`. Package versions are the newest available when
the image is built; a new base image or agent revision rebuilds the profile.

### Port forwarding

```sh
cracklet new web -p 8080:80 -p 3000    # forwards at creation
cracklet forward web 5432              # localhost:5432 -> web:5432
cracklet forward web                   # list
cracklet unforward web 5432
```

Forwards appear as `localhost:PORT` on the Mac, persist across `stop`/`start`
and are removed with the VM. Host ports must be 1024 or higher because Lima
exposes them as your user. Under the hood a transient systemd socket on the
Lima VM activates `systemd-socket-proxyd` towards the microVM, and Lima's
dynamic port forwarding brings the socket to `127.0.0.1` on the Mac.

To use plain `ssh`, add one line to `~/.ssh/config`:

```
Include ~/.cracklet/ssh_config
```

Then `ssh dev.cracklet` works from anywhere, including `scp`, `rsync` and your IDE.
The alias uses the Lima VM as an SSH jump host (`ProxyCommand ssh -W`), so no
port forwarding or `sudo` is needed on macOS.

### Capabilities: credentials and MCPs without copying secrets

A microVM is isolated from your Mac on purpose. When an agent inside needs
GitHub, Claude Code or an MCP server that only runs on the Mac, cracklet opens
one controlled hole: a **broker** that hands out *connections, never secrets*.

```sh
cracklet secret set claude-token          # paste the output of `claude setup-token`
cracklet secret set github-token          # a fine-grained token for the repos you grant
cracklet grant agent1 claude github:itlabs-gmbh/cracklet ssh-agent
cracklet ssh agent1                       # the broker lives as long as this session
cracklet grants agent1
cracklet revoke agent1 github:itlabs-gmbh/cracklet
```

While `cracklet ssh` is open, the broker listens on a Unix socket on the Mac and
ssh forwards it to `127.0.0.1:7777` inside the guest (`-R`). Tools in the guest
talk to that address with a per-VM placeholder token; the broker swaps it for
the real credential from the macOS Keychain. The socket identifies the VM, so
there is nothing in the guest worth stealing, and the hole closes with the
terminal. `ssh-agent` is a built-in grant that adds `-A`; pair it with
`ssh-add -c` to confirm every signature.

When something other than `cracklet ssh` drives the guest, such as an editor or
an agent runner with its own ssh session, keep the broker up with
`cracklet tunnel agent1`. It holds only the forward, reconnects when the
connection drops, logs `tunnel=open|closed` to the audit log and ends with
Ctrl-C or when the VM stops. `cracklet ssh agent1` keeps working alongside it
and uses the running broker instead of forwarding a second time; if the session
or tunnel that owns the broker ends first, the remaining session takes it over
within a few seconds.

Grants are the only policy: default deny, one file per VM in `~/.cracklet/vms/`,
checked on every request and logged to `~/.cracklet/audit.log`. A denied request
answers with the exact `cracklet grant` command that would allow it.

The broker is harness-neutral. It knows three primitives, and everything
specific to Claude Code, GitHub or any other tool is a **capability file**:

| Primitive | What the broker does                                                        |
|-----------|-----------------------------------------------------------------------------|
| `proxy`   | reverse-proxies `/<cap>/...` to an upstream and injects headers from secrets |
| `mcp`     | runs a stdio MCP server on the Mac and serves it as an HTTP MCP endpoint     |
| `exec`    | pipes the request through an external program (`cracklet-cap-<name>`)        |

```sh
cracklet cap ls                           # embedded: claude, github; yours in ~/.cracklet/caps
cracklet cap show claude                  # the TOML file
cracklet cap show github --render --vm agent1   # what the guest receives
cracklet cap init codex                   # commented skeleton, then: cracklet cap lint codex
cracklet cap add https://example.com/gemini.toml   # shown before it is installed
```

A capability declares the primitive plus a `[guest]` section with environment
variables, files, managed blocks inside shared files (such as `/etc/gitconfig`)
and JSON merges, all templated with `.BrokerURL`, `.PseudoToken` and `.VM`. Secrets (`keychain:`, `env:`, `cmd:`, `file:`) are only valid on the
broker side; `cracklet cap lint` rejects them in guest sections. A user file in
`~/.cracklet/caps/<name>.toml` replaces an embedded capability of the same name.
See `examples/caps/` for an MCP bridge and an exec plugin.

The embedded `claude` capability points `ANTHROPIC_BASE_URL` at the broker and
sets `CLAUDE_CODE_OAUTH_TOKEN` to the placeholder, so the unmodified Claude Code
binary in the guest sends exactly the headers of a normal subscription login.
Usage draws on your own subscription like any other session. Anthropic's terms
allow signing in to the unmodified binary with your own subscription, including
in sandboxes you run; they do not allow routing other people's requests through
your account, so keep the broker personal.

## How a microVM is wired

| Piece            | Value                                                                 |
|------------------|-----------------------------------------------------------------------|
| Kernel           | Firecracker CI `vmlinux-6.1.186` (aarch64)                            |
| Root disk        | copy of the Ubuntu 24.04 base image, grown to `--disk` with resize2fs |
| Network          | tap `cracklet<N>` on the Lima VM, guest IP `172.16.<N>.2/30`, NAT to the internet |
| Guest IP config  | kernel `ip=` boot argument, hostname via `systemd.hostname=`          |
| Process          | transient systemd unit `cracklet-vm-<name>`, console in `console.log`      |
| Port forwards    | `forwards` file per VM, socket units `cracklet-fwd-<name>-<port>`          |
| State            | `/var/lib/cracklet/vms/<name>/` inside the Lima VM                         |

Stopping uses a guest `reboot` over SSH (Firecracker on aarch64 has no ACPI
power button) and falls back to SIGTERM after 30 seconds.

Each Firecracker process runs in a transient systemd unit with
`NoNewPrivileges`, `PrivateTmp`, `ProtectHome`, a device allow-list
(`/dev/kvm`, `/dev/net/tun`) and a `MemoryMax` of guest memory plus 256 MiB.
Agent commands that change state are serialised with `flock`, and a `cracklet new`
that fails half-way rolls back its disk, tap, hosts entry and unit.

### Network isolation

Two dedicated iptables chains (`CRACKLET-FORWARD` for routed traffic,
`CRACKLET-INPUT` for traffic addressed to the Lima VM itself) let microVMs reach
the internet through NAT but:

- microVMs cannot talk to each other (tap-to-tap traffic is dropped),
- microVMs cannot reach the Mac or your LAN: Lima's gateway (which forwards
  to the macOS loopback) and all private, link-local and loopback
  destinations are rejected, so the Mac's own LAN address is out of reach too,
- microVMs cannot reach services on the Lima VM, including the port-forward
  proxies of other microVMs (only replies to connections the Lima VM opened
  are let back in),
- packets whose source is not in the guest subnet are dropped, and IPv6 is
  disabled on every tap, so the IPv4 rules cannot be side-stepped.

Both chains end in an explicit `DROP`, so isolation does not depend on the
default INPUT/FORWARD policy, and they are rebuilt through a single
`iptables-restore` batch so running guests never observe an empty chain. If you want guests to reach a service on your
Mac or LAN, edit `PRIVATE_NETS` and the `REJECT` rules in `setup_host_network`
in `internal/agent/agent.sh`.

### Entropy after restore

Every microVM restored from the golden snapshot starts with the snapshot's
kernel random state. The host agent sends 32 bytes from the Lima VM's
`/dev/urandom` with the identity message; `cracklet-envd` credits them with
`RNDADDENTROPY` and forces a reseed with `RNDRESEEDCRNG`, so clones diverge
immediately instead of at the kernel's next scheduled reseed.

## Troubleshooting

- `cracklet doctor` explains what is missing on the host.
- Console output of a VM: `limactl shell cracklet sudo cat /var/lib/cracklet/vms/<name>/console.log`
- Agent logs: `limactl shell cracklet sudo journalctl -u cracklet-vm-<name>`
- After a reboot of the Lima VM, microVMs are stopped; `cracklet start <name>` brings them back.
- `cracklet ls` shows `broken` for a VM whose metadata is damaged; `cracklet rm <name>` cleans it up.
- `cracklet ssh` mirrors the remote exit status; exit 255 means the connection failed.
- Ctrl-C during `cracklet new NAME` removes the half-created VM again (the agent inside
  the Lima VM finishes or rolls back on its own, then cracklet runs `rm`). For an
  auto-named VM, check `cracklet ls` afterwards.
- Reset everything: `limactl delete -f cracklet && rm -rf ~/.cracklet`

## Why not Firecracker on the Virtualization framework directly?

There is a proof of concept that ports Firecracker to Apple's Virtualization
framework (`drink7036290/firecracker`, branch `macos_avf`). It has been
untouched since February 2025, disables most of Firecracker (no API server,
no event loop, console on stdio) and needs a custom-built kernel. Nested
virtualization keeps the upstream binary, API, snapshots and kernels intact,
which is why cracklet builds on it.

## Development

```sh
make envd     # cross-compile the guest daemon (needed before go build)
make test     # unit tests (with -race); pure agent helpers run under bash too
make cover    # coverage summary
make lint     # gofmt, go vet, shellcheck
make e2e      # boots a real microVM (requires cracklet prepare), cleans up afterwards
```

Layout: `cmd/cracklet` (entry point), `internal/cli` (cobra commands),
`internal/app` (workflows), `internal/lima` (limactl wrapper + template),
`internal/agent` (embedded guest script), `internal/host` (preflight),
`internal/broker` (per-VM capability broker), `internal/cap` (capability files,
embedded defaults), `internal/grant` (per-VM policy), `internal/guest`
(guest provisioning scripts), `internal/secret` (Keychain/env/cmd/file resolver),
`internal/vm` (spec validation), `internal/sshcfg` (ssh_config rendering).

## Contributing

Bug reports and pull requests are welcome. Please read
[CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, coding
guidelines and PR process, and [SECURITY.md](SECURITY.md) for how to report
vulnerabilities privately. This project follows the
[Contributor Covenant](CODE_OF_CONDUCT.md).

## License

cracklet is licensed under the [Apache License 2.0](LICENSE).
Copyright 2026 IT-Labs GmbH. Firecracker, the guest kernel, the Ubuntu image
and Lima are downloaded at `cracklet prepare` time and keep their own
licenses; see [NOTICE](NOTICE).
