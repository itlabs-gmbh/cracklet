# cracklet – Firecracker microVMs on your Mac

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
make install          # go install ./cmd/cracklet  → $(go env GOPATH)/bin/cracklet
cracklet prepare           # one-time setup, a few minutes
```

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

`--fresh` forces a cold boot; a `--disk` larger than the base image implies it.
Snapshots are keyed on the base image, kernel, Firecracker version and the
agent revision, so they rebuild automatically after `cracklet prepare` changes any
of them. `cracklet-envd` is cross-compiled for linux/arm64 and embedded into cracklet
(`make envd`).

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
- microVMs cannot reach services on the Mac (Lima's gateway, which forwards
  to the macOS loopback, is rejected),
- microVMs cannot reach services on the Lima VM, including the port-forward
  proxies of other microVMs (only replies to connections the Lima VM opened
  are let back in).

Both chains end in an explicit `DROP`, so isolation does not depend on the
default INPUT/FORWARD policy. If you want guests to reach a service on your
Mac, remove the `REJECT` rule in `setup_host_network` in
`internal/agent/agent.sh`.

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
`internal/vm` (spec validation), `internal/sshcfg` (ssh_config rendering).
