# Design: a second backend on apple/container

Status: accepted direction, 2026-10-14; revised 2026-10-15 against main at
5aace09 (profiles, owner/slot, gc, tunnel, reboot restore, Lima resize). Based
on the spike measured on 2026-10-13 (apple/container 1.5.0, macOS 26.7.1, M4 Pro).

Decision: apple/container becomes the default backend once it covers the
feature set below, and the Lima/Firecracker backend is removed afterwards. The
backend interface is therefore a *migration seam*, not a long-lived plug-in
system: keep it as small as the migration needs, and prefer deleting Lima code
over generalizing it.

## Why

cracklet today runs Firecracker inside a Lima VM with nested virtualization.
The spike showed that the two costs of that nesting disappear when a microVM is
a Virtualization.framework VM driven by `container`:

| measurement (2 vCPU / 1 GiB)          | Mac     | Lima    | Firecracker nested | apple/container |
|---------------------------------------|---------|---------|--------------------|-----------------|
| first touch of fresh RAM, ms/MiB      | 0.18    | 0.39    | 0.39 (THP on)      | 0.18–0.23       |
| `CTR_EL0` read (JIT icache flushes)   | n/a     | 0.001 µs| 24 µs              | 0.001 µs        |
| Claude Code CPU in the first 3 s, JIT | 0.64–0.71 s | –   | 1.13–1.22 s        | 0.69–0.87 s     |
| cold boot to sshd                     | –       | –       | 1.5 s (+0.1 s ssh) | 0.5 s           |

The `CTR_EL0` trap cannot be removed in the Lima setup without a patched KVM
(see the 2026-10-07 analysis), so the only way to give JIT workloads native
speed is to drop the KVM layer.

What a container backend gives up: real Firecracker, golden snapshots and the
`cracklet-envd` identity path (not needed when a cold boot takes 0.5 s), and the
Lima-side firewall that today confines guests to "internet only".

## Non-goals

- Keeping both backends forever. Lima stays only until the container backend
  has shipped phases 1–4; it is then deleted together with `agent.sh`,
  `cracklet-envd`, the golden snapshots and the nested-virtualization preflight.
- Making the Lima path better in the meantime (no new Lima features).
- A Go client for the `container-apiserver` XPC API. We drive the `container`
  CLI (JSON output) exactly like we drive `limactl`.
- Compatibility code for VMs created by the other backend (see the project
  rule: no legacy migration code; users recreate VMs).

## Architecture

```
cli ──▶ app ──▶ backend.Backend ──┬──▶ backend/lima      (limactl + agent.sh, today's code)
                                  └──▶ backend/container (container CLI, guest image)
              app keeps: grants, broker, caps, secrets, ssh session, ssh_config
```

`internal/vm` stays the dependency-free domain package (`Spec`, `Forward`,
profiles, validation) and gains `Info`, the type that `app.VMInfo` is today
(name, index, ip, state incl. `creating`, vcpus, mem, profile, forwards,
owner, slot, created_at; grants stay an app-side decoration). The new
package `internal/backend` holds the interface; the two implementations live in
`internal/backend/lima` and `internal/backend/container`.

### The interface

```go
package backend

// Backend creates and drives microVMs. Everything that is the same for all
// backends (grants, broker, caps) stays in app and talks to the guest over
// ssh, which both backends must provide.
type Backend interface {
    // Name is "lima" or "container"; shown by doctor and ls.
    Name() string
    // Doctor reports host problems specific to this backend without changing anything.
    Doctor(ctx context.Context) ([]host.Problem, error)
    // Prepare makes the host ready (idempotent): runtime, images, keys.
    Prepare(ctx context.Context, o PrepareOptions) error
    // Ready brings the runtime up for a foreground command: Lima starts a
    // stopped instance and restores its microVMs (app/boot.go today), the
    // container backend starts the apiserver and the machines that ran before
    // the Mac rebooted. It fails with a hint when Prepare has not run.
    Ready(ctx context.Context) error
    // Alive is the non-starting probe used by background polling
    // (`cracklet tunnel`), which must never undo a deliberate stop.
    Alive(ctx context.Context) error

    New(ctx context.Context, spec vm.Spec) (vm.Info, error)
    Start(ctx context.Context, name string) (vm.Info, error)
    Stop(ctx context.Context, name string) (vm.Info, error)
    // Remove deletes a VM. With a Guard it only removes the VM if owner and
    // creation time still match (gc's compare-and-swap against a reused name)
    // and reports whether it was gone.
    Remove(ctx context.Context, name string, guard *RemoveGuard) (gone bool, err error)
    List(ctx context.Context) ([]vm.Info, error)

    Forward(ctx context.Context, name string, fw []vm.Forward) (vm.Info, error)
    Unforward(ctx context.Context, name string, hostPorts []int) (vm.Info, error)

    // Dial opens a TCP connection to port inside the named VM. It backs the
    // ssh ProxyCommand (`cracklet _dial`) and the forward relay. The Lima
    // backend keeps its own ProxyCommand (ssh -W through the Lima VM) until it
    // is removed, so only the container backend implements Dial for real.
    Dial(ctx context.Context, name string, port int) (net.Conn, error)
    // SSHProxyCommand is the ProxyCommand line rendered into ssh_config.
    SSHProxyCommand() string
    // Capabilities tells app which optional features exist, so unsupported
    // flags fail early with a clear message instead of deep in a backend.
    Capabilities() Capabilities
}

type Capabilities struct {
    Snapshots     bool // --fresh has a meaning
    Profiles      bool // profiles size the root disk (container: sparse 512 GiB, always)
    Forwards      bool // cracklet forward works
    GuestFirewall bool // guests are confined to internet-only
    Resize        bool // prepare --cpus/--memory/--disk resize a shared VM
}
```

`PrepareOptions` carries the Lima sizing (`CPUs`, `MemoryGiB`, `DiskGiB`,
`Explicit`) and the profiles to pre-build; the container backend rejects
explicit sizing (there is no shared VM to size) and ignores profiles.

What stays in `app` and why it is backend-neutral:

- grants, caps, secrets, provisioning over `ssh … bash -s`, the broker and its
  guest relay (`/run/cracklet/broker.sock` via systemd socket units: the
  container guest runs systemd too);
- `cracklet tunnel` (needs `Alive` + `List` for its running check);
- `gc` (needs `List` and guarded `Remove`; host-state pruning is app-side);
- the two host flocks in `limalock.go` are Lima-only (boot and resize) and
  move with the backend; the container backend serializes API calls with its
  own lock (see below).

Why `Dial` instead of exposing the guest IP: it decouples `ssh_config` from the
address plan (vmnet hands out 192.168.64.x and may renumber on reboot), and it
is the same primitive the forward relay needs. `cracklet _dial %h %p` resolves
the name through `machine ls` on every connection, so stale IPs are never a
user-visible problem. Lima is not migrated to `_dial`: its ProxyCommand goes
away with the backend.

### What moves where (pure refactor, no behavior change)

| today                                              | after                               |
|----------------------------------------------------|-------------------------------------|
| `app/agent.go` (ensureAgent, agentOutput, agentRun) | `backend/lima/agent.go`             |
| `app/vm.go` agent calls, `parseVM`, `agentVMs`, `orDash` | `backend/lima/vm.go`          |
| `app/forward.go` agent calls                       | `backend/lima/forward.go`           |
| `app/prepare.go` ensureInstance/createInstance, pushKeys, pushEnvd, agent prepare + profiles | `backend/lima/prepare.go` |
| `app/resize.go`, `app/limalock.go`                 | `backend/lima/resize.go`, `lock.go` |
| `app/boot.go` requireRunning, checkRunning, bootLima, restoreMicroVMs | `backend/lima.Ready` / `.Alive` |
| `app/gc.go` removeListed (`rm NAME OWNER CREATED`) | `backend/lima.Remove` with guard    |
| `app/tunnelhold.go` vmRunning                      | app, on `Alive` + `List`            |
| `app/vm.go` VMInfo                                 | `vm.Info`                           |
| `host.Check` (chip ≥ M3, macOS ≥ 15, Lima)         | `backend/lima.Doctor`; `host` keeps the shared checks (Apple Silicon, Hypervisor) |
| `config.Instance`, Lima defaults, `Paths.Lima*`    | `backend/lima`                      |

`app` keeps: key pair, grants, provisioning over ssh (`bash -s`), the broker
tunnel (`ssh -R 127.0.0.1:7777:<socket>`), `cracklet ssh`, ssh_config, caps,
secrets. None of that knows about Lima once `requireRunning` is behind
`Backend.Ready`.

### Selecting a backend

- `cracklet prepare --backend lima|container` writes `~/.cracklet/backend`
  (one line). Default is `lima` until phase 4 ships, then `container`; the
  flag and the Lima backend are removed in phase 5.
- `CRACKLET_BACKEND` overrides for a single invocation (tests, CI).
- A `CRACKLET_HOME` has exactly one backend; `prepare --backend` with a
  different value than the stored one refuses unless `cracklet ls` is empty.
- `cracklet doctor` prints the backend and its problems.

## The container backend

### Host requirements (Doctor)

- Apple Silicon (any generation: no nested virtualization needed).
- macOS 26 or newer (`container` only supports 26; 15 has no multi-network
  vmnet). This is stricter than Lima's macOS 15.
- `container` ≥ 1.5.0 on PATH; `brew install container` provides the formula
  (no cask). Prepare installs it like it installs Lima today.
- `container system status` running; Prepare runs
  `container system start --enable-kernel-install`.

### Prepare

1. Preflight as above; generate the key pair (shared code).
2. Start the system service.
3. Build the guest image `cracklet/guest:<rev>` with `container build` from an
   embedded Dockerfile rendered into a temp context:

   ```dockerfile
   FROM ubuntu:24.04
   ENV DEBIAN_FRONTEND=noninteractive
   RUN apt-get update && apt-get install -y --no-install-recommends \
         systemd systemd-sysv systemd-resolved openssh-server sudo curl ca-certificates iproute2 \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl enable ssh systemd-networkd systemd-resolved
   # networkd DHCP on e*: the guest gets the IP that `machine ls` reports
   RUN printf '[Match]\nName=e*\n\n[Network]\nDHCP=yes\n' > /etc/systemd/network/10-eth.network
   RUN install -d -m 0700 /root/.ssh && echo '<pubkey>' > /root/.ssh/authorized_keys \
    && chmod 0600 /root/.ssh/authorized_keys \
    && printf 'PermitRootLogin prohibit-password\n' > /etc/ssh/sshd_config.d/10-root.conf
   LABEL cracklet.rev=<rev> cracklet.key=<sha256 of pubkey>
   ```

   Idempotent: skip when `container image inspect cracklet/guest:<rev>` shows
   both labels with the expected values. The public key is baked in because a
   fresh machine has no other channel before sshd is up; `machine run -i` could
   stream it later, but that path is the one with the first-boot bug (below).
   Guest customizations that `agent.sh` does today (motd, hostname, trimmed
   boot) move into this Dockerfile; `cracklet-envd` is not installed.
4. Write `~/.cracklet/ssh_config` with the `_dial` ProxyCommand.

### Lifecycle mapping

| cracklet          | container                                                                                   |
|-------------------|---------------------------------------------------------------------------------------------|
| `new NAME`        | `container machine create cracklet/guest:<rev> --name NAME --cpus N --memory <MiB>M --home-mount none`, then poll `machine ls --format json` for `ipAddress` and TCP 22 (see first-boot bug) |
| `new` (auto name) | pick the first free `vm<N>` from `List`, as `agent.sh` does                                 |
| `start NAME`      | `container machine run --name NAME --root -- true` (boots, 0.5 s), then poll sshd           |
| `stop NAME`       | `container machine stop NAME`                                                                |
| `rm NAME`         | `container machine rm NAME` + grant store removal (shared)                                   |
| `ls`              | `machine ls --format json`, filtered to machines whose `inspect` `image.reference` starts with `cracklet/guest`; `cpus`, `memory`, `ipAddress`, `status` map onto `vm.Info` |
| `ssh NAME`        | unchanged: `ssh -F ~/.cracklet/ssh_config NAME.cracklet`; ProxyCommand `cracklet _dial` → `Dial(name, 22)` → TCP to `ipAddress:22` |
| grants/broker     | unchanged: `bash -s` over ssh, `ssh -R` for the broker socket                               |
| `--fresh`         | accepted and ignored (every boot is cold); `Capabilities.Snapshots=false`                   |
| `--profile`       | accepted and ignored: the machine root disk is a sparse 512 GiB volume (`/dev/vdb`, 2.2 GiB used after boot), so no profile is ever too small; `ls` shows real usage |
| `--owner/--slot`, `created_at` | host-side metadata in `~/.cracklet/vms/<vm>/meta.json` (the Lima agent stores it in the guest); the guarded `Remove` compares against it under the per-VM flock |
| reboot of the Mac | `Ready` runs `container system start` when the apiserver is down and boots the machines marked running in `meta.json` (the Lima backend's `agent restore` equivalent), under the boot lock so parallel commands boot once |
| `prepare --cpus/--memory/--disk` | rejected (`Capabilities.Resize=false`); `container machine set cpus=/memory=` could later back a per-VM resize instead |
| `forward`         | phase 1: rejected with the direct address as hint (`192.168.64.x:PORT` is reachable from the Mac); phase 2: relay below |

The VM name is the machine name, unprefixed, so `container machine ls` reads
naturally; the image reference is what marks a machine as cracklet's. The
default machine (`--set-default`) is never touched.

Operations against the `container` API are serialized with a host-side flock
(`~/.cracklet/backend.lock`, same pattern as the grant locks): concurrent
`machine run` calls on different machines failed repeatedly in the spike.
`List` is exempt (read-only, used by polling).

Verified facts about a machine (1.5.0): root is `/dev/vdb`, a sparse 512 GiB
ext4 that grows on demand (writing 1.5 GiB worked, `ls` DISK shows usage);
`/sbin.machine` is a read-only virtiofs share with the init shim; `machine set`
changes `cpus`, `memory`, `home-mount` of a stopped machine.

### Known runtime bugs and the workarounds

1. **First `machine run` after `machine create` fails** after ~11 s with a
   macOS errno text ("Operation not supported by device", "Inappropriate ioctl
   for device", "Operation not supported on socket") and the machine reboots
   with a new IP. `--no-boot` does not help. Workaround: `New` never calls
   `machine run`; it waits for `ipAddress` in `machine ls` and for sshd to
   accept. `Start` calls `machine run --root -- true` only on a stopped machine,
   which worked reliably. Worth an upstream issue; none found on 2026-10-13.
2. `machine create` succeeds for images without `/sbin/init` and the boot fails
   with a misleading error (apple/container #2024). Not our case, but Doctor
   should verify the built image boots once during Prepare.
3. `machine run -i` is needed for stdin; `-t` without a real tty is ENODEV.
   Only relevant if we ever use `machine run` for provisioning (we do not).

### Port forwards (phase 2)

`container machine` has no `--publish`. Options:

- **Relay in cracklet** (preferred): `cracklet forward` records the forward in
  `~/.cracklet/vms/<vm>/forwards` and `Dial` makes it a plain TCP relay
  `127.0.0.1:HOST → <ip>:GUEST`. It needs a process that outlives the CLI call:
  a launchd user agent `com.cracklet.relay` started on demand, reading the
  forward files, like the broker socket is started on demand today. Forwards
  then survive stop/start exactly like on Lima (the relay retries while the
  guest is down).
- `ssh -L` in a detached ssh: simpler but a second process model to manage.
- Document the direct address only: zero code, but `-p` from `cracklet new`
  would silently differ between backends.

### Network isolation: the one real regression

Lima guests are firewalled by `agent.sh` (`render_firewall_batch`): internet
only, no LAN, no Mac. vmnet NAT gives container guests the LAN and the Mac
(`192.168.64.1`). `container network create --internal` is host-only (the
opposite), and `machine create` has no `--network` option in 1.5 anyway.

The broker threat model does not depend on this (the guest only ever gets a
loopback tunnel), but "a guest cannot see your LAN" is a promise the website
makes. Options, in order of preference:

1. Ask upstream for an egress policy on vmnet networks or a `--network` for
   machines; until then document the difference (`Capabilities.GuestFirewall`
   shows up in `doctor`).
2. Best-effort nftables inside the guest image (`-d 10/8,172.16/12,192.168/16,
   100.64/10,169.254/16 reject` on output, except the gateway for DNS). Root in
   the guest can remove it, so it is a guard rail, not a boundary. Still worth
   shipping because the common failure is a careless agent, not a hostile one.
3. A pf anchor on the Mac for `bridge100` traffic: real enforcement, but needs
   root on the Mac and fights with `container`'s own vmnet rules. Not for v1.

## Phasing

1. **Refactor** (no behavior change): `internal/backend` interface, Lima
   implementation moved out of `app` (agent, vm, forward, prepare, resize,
   locks, boot/restore, guarded rm), `vm.Info`, backend selection with only
   `lima` accepted. All existing tests pass unchanged except import paths; the
   `runner.Fake` based tests move with the code. `cmd/`, `cli/` and the broker
   do not change.
2. **`cracklet _dial`** and the container `ssh_config`; Lima keeps its
   ProxyCommand (not worth migrating code that is scheduled for deletion).
3. **Container backend**: Doctor, Prepare (image build), New/Start/Stop/Remove/
   List, Dial; `--disk`/`forward` rejected via Capabilities; website gets a
   "Backends" section with the measured numbers.
4. **Forwards relay** via launchd agent; in-guest nftables guard rail.
5. **Flip and delete**: default becomes `container`; a release later remove
   `internal/lima`, `internal/agent`, `internal/envd*`, `cmd/cracklet-envd`,
   the golden-snapshot docs, the M3/nested-virt preflight and the Lima section
   of the website. Users recreate their VMs (no migration code, as agreed).
6. **Rename to `skiff`** (decided 2026-10-15): binary, module path
   `github.com/itlabs-gmbh/skiff`, `~/.skiff`, `SKIFF_HOME`, `ssh <vm>.skiff`,
   guest image `skiff/guest`, website and README. New tagline: "A fresh Linux VM
   on your Mac in half a second." The rename ships with phase 5 so users see one
   break, not two. Before it: check trademark residue of Skiff Mail (Notion).

## Testing

- Unit: table-driven tests on `runner.Fake` for every `container` invocation
  (argument lists are the contract) and JSON fixtures recorded from 1.5.0
  (`machine ls --format json`, `machine inspect`, `image inspect`).
- Contract test shared by both backends (`backendtest` package): New → List
  shows it running with an IP → Stop → Start → Remove. A separate test checks
  that Dial(22) connects on the container backend only (Lima returns
  `ErrNoDial`). Runs in CI only against a fake; locally `make e2e BACKEND=container`.
- Spike harness (`ccbench`, `tui_ready.py`) becomes `examples/bench/` so the
  numbers on the website can be reproduced.

## Open questions

- Guest image base: keep Ubuntu 24.04 (same as the Firecracker rootfs) or move
  to the smaller Debian/Ubuntu-minimal with systemd? Boot is already 0.5 s.
- Do machines keep running when the user logs out / the apiserver launch agent
  stops? Decides whether `meta.json` running markers are enough for restore.
- Memory accounting: VZ does not return freed guest memory to macOS (same as
  Lima); with many 1 GiB machines this is a visible difference from
  Firecracker's balloon-less but per-VM footprint. Measure before promising.
