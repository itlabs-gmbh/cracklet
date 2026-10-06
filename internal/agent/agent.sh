#!/usr/bin/env bash
# cracklet agent: manages Firecracker microVMs inside the Lima VM. Runs as root and is
# invoked by the cracklet CLI on macOS through `limactl shell`. Diagnostics go to
# stderr, machine-readable results (JSON) go to stdout.
#
# The script stays bash-3.2 compatible in its pure helpers so they can be unit
# tested on macOS; everything that touches the system expects Ubuntu.
set -euo pipefail
export LC_ALL=C

CRACKLET_ROOT=${CRACKLET_ROOT:-/var/lib/cracklet}
FC_BIN=${FC_BIN:-/usr/local/bin/firecracker}
HOSTS_FILE=${HOSTS_FILE:-/etc/hosts}
SYSCTL_DROPIN=${SYSCTL_DROPIN:-/etc/sysctl.d/99-cracklet.conf}
readonly CRACKLET_ROOT FC_BIN HOSTS_FILE SYSCTL_DROPIN
readonly IMAGES_DIR=$CRACKLET_ROOT/images
readonly VMS_DIR=$CRACKLET_ROOT/vms
readonly KEY_FILE=$CRACKLET_ROOT/id_ed25519
readonly PUBKEY_FILE=$CRACKLET_ROOT/id_ed25519.pub
readonly LOCK_FILE=$CRACKLET_ROOT/.lock
readonly SUBNET_PREFIX=172.16          # every VM owns 172.16.<index>.0/30
readonly NET=$SUBNET_PREFIX.0.0/16
readonly TAP_PREFIX=cracklet
readonly UNIT_PREFIX=cracklet-vm-
readonly FWD_UNIT_PREFIX=cracklet-fwd-
readonly PROXYD=/usr/lib/systemd/systemd-socket-proxyd
readonly MIN_HOST_PORT=1024            # Lima exposes ports on the Mac as a normal user
readonly CHAIN=CRACKLET-FORWARD        # guest -> elsewhere (routed traffic)
readonly INPUT_CHAIN=CRACKLET-INPUT    # guest -> the Lima VM itself
# Guests get internet only: private, link-local and loopback destinations are
# rejected so they cannot reach the LAN or the Mac's own LAN address.
readonly PRIVATE_NETS=(10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16 127.0.0.0/8)
readonly BASE_DISK_SIZE=2G
readonly ROOTFS_REV=7                  # bump whenever customize_rootfs changes
readonly GOLDEN_REV=1                  # bump whenever build_golden changes
readonly ENVD_BIN=$CRACKLET_ROOT/cracklet-envd   # guest identity daemon, pushed by cracklet prepare
readonly GOLDEN_INDEX=0                # the golden VM boots as 172.16.0.2 on tap cracklet0
readonly VSOCK_CID=3
readonly VSOCK_PORT=52
readonly FC_API_WAIT_SECONDS=10
readonly HOSTS_MARKER='# cracklet-managed'
readonly SSH_WAIT_SECONDS=60
readonly STOP_WAIT_SECONDS=30
readonly LOCK_WAIT_SECONDS=300
readonly MAX_INDEX=254
readonly MAX_VCPUS=32
readonly MIN_MEM_MIB=128
readonly MAX_MEM_MIB=262144
readonly VMM_OVERHEAD_MIB=256
readonly CURL_OPTS=(--fail --silent --show-error --location --proto '=https' --retry 3 --connect-timeout 15 --max-time 900)

TMP_DIRS=()
NEW_VM_NAME=
NEW_VM_DONE=0

log() { printf 'cracklet-agent: %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

# on_exit rolls back a VM whose creation did not finish and removes temp dirs.
on_exit() {
  local status=$?
  set +e
  if [[ -n $NEW_VM_NAME && $NEW_VM_DONE -ne 1 ]]; then
    discard_vm "$NEW_VM_NAME"
  fi
  if ((${#TMP_DIRS[@]} > 0)); then
    rm -rf "${TMP_DIRS[@]}"
  fi
  exit "$status"
}

# --- helpers -----------------------------------------------------------------

vm_dir()   { echo "$VMS_DIR/$1"; }
vm_index() { cat "$(vm_dir "$1")/index" 2>/dev/null || true; }
vm_unit()  { echo "${UNIT_PREFIX}$1"; }
idx_ip()   { echo "${SUBNET_PREFIX}.$1.2"; }
idx_gw()   { echo "${SUBNET_PREFIX}.$1.1"; }
idx_tap()  { echo "${TAP_PREFIX}$1"; }
idx_mac()  { printf '06:00:AC:10:%02X:02' "$1"; }
to_upper() { tr '[:lower:]' '[:upper:]' <<<"$1"; }
is_index() { [[ $1 =~ ^[0-9]{1,3}$ ]]; }

validate_name() {
  [[ $1 =~ ^[a-z][a-z0-9-]{0,30}$ ]] || die "invalid name '$1' (lowercase letters, digits, dashes)"
}

validate_int_range() { # value min max label
  if ! [[ $1 =~ ^[0-9]{1,9}$ ]] || (($1 < $2 || $1 > $3)); then
    die "$4 must be an integer between $2 and $3, got '$1'"
  fi
}

validate_disk() { [[ $1 =~ ^[0-9]{1,6}[MG]$ ]] || die "invalid disk size '$1' (use e.g. 512M or 4G)"; }
validate_url()  { [[ $1 == https://* ]] || die "URL must use https: $1"; }
validate_sha()  { [[ $1 =~ ^[0-9a-f]{64}$ ]] || die "invalid sha256 '$1'"; }

require_vm() {
  validate_name "$1"
  [[ -d $(vm_dir "$1") ]] || die "VM '$1' does not exist"
}

unit_active() { systemctl is-active --quiet "$(vm_unit "$1")"; }

fwd_unit()   { echo "${FWD_UNIT_PREFIX}$1-$2"; }            # name hostport
fwd_file()   { echo "$(vm_dir "$1")/forwards"; }            # one HOST:GUEST per line
fwd_active() { systemctl is-active --quiet "$(fwd_unit "$1" "$2").socket"; }

validate_port() { # value min label
  if ! [[ $1 =~ ^[0-9]{1,5}$ ]] || (($1 < $2 || $1 > 65535)); then
    die "$3 port must be between $2 and 65535, got '$1'"
  fi
}

parse_forward() { # [HOST:]GUEST -> "HOST GUEST"
  local spec=$1 host guest
  if [[ $spec == *:* ]]; then host=${spec%%:*}; guest=${spec#*:}; else host=$spec; guest=$spec; fi
  validate_port "$host" "$MIN_HOST_PORT" host
  validate_port "$guest" 1 guest
  echo "$host $guest"
}

ssh_guest() { # ip command...
  local ip=$1; shift
  timeout 10 ssh -i "$KEY_FILE" -o BatchMode=yes -o ConnectTimeout=2 \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    "root@$ip" "$@"
}

wait_for_ssh() { # name ip
  local name=$1 ip=$2 deadline=$((SECONDS + SSH_WAIT_SECONDS))
  while ((SECONDS < deadline)); do
    ssh_guest "$ip" true >/dev/null 2>&1 && return 0
    unit_active "$name" || { log "firecracker for $name exited"; return 1; }
    sleep 0.5
  done
  return 1
}

add_host_entry() { # name ip
  remove_host_entry "$1"
  echo "$2 $1.cracklet $1 $HOSTS_MARKER:$1" >> "$HOSTS_FILE"
}

remove_host_entry() { # name
  [[ -f $HOSTS_FILE ]] || return 0
  sed -i "\| ${HOSTS_MARKER}:$1\$|d" "$HOSTS_FILE"
}

atomic_write() { # path content
  printf '%s\n' "$2" > "$1.tmp" && mv -f "$1.tmp" "$1"
}

verify_sha256() { # file expected
  local actual
  actual=$(sha256sum "$1" | cut -d' ' -f1)
  [[ $actual == "$2" ]] || die "checksum mismatch for $(basename "$1"): expected $2, got $actual"
}

fetch() { # url dest
  log "downloading $(basename "$1")"
  curl "${CURL_OPTS[@]}" -o "$2" "$1"
}

# vm_json never fails: a VM with missing metadata is reported as "broken" so
# that one damaged directory cannot hide the healthy ones from `cracklet ls`.
vm_json() { # name
  local name=$1 dir idx state config
  dir=$(vm_dir "$name"); idx=$(vm_index "$name"); config=$dir/config.json
  if unit_active "$name"; then state=running; else state=stopped; fi
  if ! is_index "$idx" || [[ ! -s $config ]]; then
    jq -cn --arg name "$name" \
      '{name: $name, index: null, ip: null, state: "broken", vcpus: null, mem_mib: null, forwards: []}'
    return 0
  fi
  jq -c --arg name "$name" --argjson index "$idx" --arg ip "$(idx_ip "$idx")" --arg state "$state" \
    --argjson forwards "$(forwards_json "$name")" \
    '{name: $name, index: $index, ip: $ip, state: $state,
      vcpus: ."machine-config".vcpu_count, mem_mib: ."machine-config".mem_size_mib, forwards: $forwards}' "$config"
}

forwards_json() { # name
  local name=$1 file out="[]" host guest state
  file=$(fwd_file "$name")
  [[ -f $file ]] || { echo "$out"; return 0; }
  while IFS=: read -r host guest; do
    [[ -n $host ]] || continue
    if fwd_active "$name" "$host"; then state=active; else state=inactive; fi
    out=$(jq -c --argjson host "$host" --argjson guest "$guest" --arg state "$state" \
      '. + [{host: $host, guest: $guest, state: $state}]' <<<"$out")
  done < "$file"
  echo "$out"
}

# --- prepare -----------------------------------------------------------------

cmd_prepare() { # FC_VERSION FC_SHA256 KERNEL_URL KERNEL_SHA256 ROOTFS_URL ROOTFS_SHA256 VCPUS MEM_MIB
  [[ $# -eq 8 ]] || die "usage: prepare FC_VERSION FC_SHA256 KERNEL_URL KERNEL_SHA256 ROOTFS_URL ROOTFS_SHA256 VCPUS MEM_MIB"
  local fc_version=$1 fc_sha=$2 kernel_url=$3 kernel_sha=$4 rootfs_url=$5 rootfs_sha=$6 vcpus=$7 mem=$8
  validate_int_range "$vcpus" 1 "$MAX_VCPUS" vcpus
  validate_int_range "$mem" "$MIN_MEM_MIB" "$MAX_MEM_MIB" memory
  [[ $fc_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid firecracker version '$fc_version'"
  validate_url "$kernel_url"; validate_url "$rootfs_url"
  validate_sha "$fc_sha"; validate_sha "$kernel_sha"; validate_sha "$rootfs_sha"
  [[ -e /dev/kvm ]] || die "/dev/kvm is missing: nested virtualization is not active in this Lima VM"
  [[ -f $PUBKEY_FILE && -f $KEY_FILE ]] || die "SSH key pair missing under $CRACKLET_ROOT"
  [[ -x $ENVD_BIN ]] || die "guest daemon $ENVD_BIN missing (cracklet prepare pushes it)"
  install -d -m 0700 "$IMAGES_DIR" "$VMS_DIR"
  install_packages
  install_firecracker "$fc_version" "$fc_sha"
  download_kernel "$kernel_url" "$kernel_sha"
  build_base_rootfs "$rootfs_url" "$rootfs_sha"
  setup_host_network
  # the default VM size gets its golden snapshot now, so the first `cracklet new` is fast
  ensure_golden "$vcpus" "$mem"
  log "prepare complete"
}

install_packages() {
  local pkg missing=()
  for pkg in squashfs-tools e2fsprogs iptables jq curl; do
    dpkg -s "$pkg" >/dev/null 2>&1 || missing+=("$pkg")
  done
  ((${#missing[@]} == 0)) && return 0
  log "installing packages: ${missing[*]}"
  export DEBIAN_FRONTEND=noninteractive
  apt-get -o DPkg::Lock::Timeout=300 update -qq
  apt-get -o DPkg::Lock::Timeout=300 install -y -qq --no-install-recommends "${missing[@]}"
}

install_firecracker() { # version sha256
  local version=$1 sha=$2 tmp tgz
  if [[ -x $FC_BIN ]] && "$FC_BIN" --version 2>/dev/null | head -1 | grep -qFx "Firecracker $version"; then
    log "firecracker $version already installed"
    return 0
  fi
  tmp=$(mktemp -d); TMP_DIRS+=("$tmp")
  tgz="firecracker-${version}-aarch64.tgz"
  fetch "https://github.com/firecracker-microvm/firecracker/releases/download/${version}/${tgz}" "$tmp/$tgz"
  verify_sha256 "$tmp/$tgz" "$sha"
  tar -xzf "$tmp/$tgz" -C "$tmp"
  install -m 0755 "$tmp/release-${version}-aarch64/firecracker-${version}-aarch64" "$FC_BIN"
  log "installed $("$FC_BIN" --version | head -1)"
}

download_kernel() { # url sha256
  local stamp="$1|$2"
  if [[ -f $IMAGES_DIR/vmlinux && $(cat "$IMAGES_DIR/vmlinux.stamp" 2>/dev/null) == "$stamp" ]]; then
    log "kernel already present"
    return 0
  fi
  fetch "$1" "$IMAGES_DIR/vmlinux.tmp"
  verify_sha256 "$IMAGES_DIR/vmlinux.tmp" "$2"
  mv -f "$IMAGES_DIR/vmlinux.tmp" "$IMAGES_DIR/vmlinux"
  atomic_write "$IMAGES_DIR/vmlinux.stamp" "$stamp"
}

build_base_rootfs() { # url sha256
  local base=$IMAGES_DIR/rootfs-base.ext4 stamp work
  stamp="$1|$2|rev$ROOTFS_REV|$(sha256sum "$ENVD_BIN" | cut -d' ' -f1)|$(cat "$PUBKEY_FILE")"
  if [[ -f $base && $(cat "$IMAGES_DIR/rootfs-base.stamp" 2>/dev/null) == "$stamp" ]]; then
    log "base rootfs already present"
    return 0
  fi
  rm -rf "$IMAGES_DIR"/build.*
  work=$(mktemp -d "$IMAGES_DIR/build.XXXXXX"); TMP_DIRS+=("$work")
  fetch "$1" "$work/rootfs.squashfs"
  verify_sha256 "$work/rootfs.squashfs" "$2"
  log "extracting rootfs"
  unsquashfs -n -q -d "$work/root" "$work/rootfs.squashfs" >/dev/null
  customize_rootfs "$work/root"
  log "building ext4 base image ($BASE_DISK_SIZE, sparse)"
  truncate -s "$BASE_DISK_SIZE" "$work/rootfs.ext4"
  mkfs.ext4 -q -F -d "$work/root" "$work/rootfs.ext4"
  chmod 0600 "$work/rootfs.ext4"
  mv -f "$work/rootfs.ext4" "$base"
  atomic_write "$IMAGES_DIR/rootfs-base.stamp" "$stamp"
}

customize_rootfs() { # root
  local root=$1
  install -d -m 0700 "$root/root/.ssh"
  install -m 0600 "$PUBKEY_FILE" "$root/root/.ssh/authorized_keys"
  rm -f "$root/etc/resolv.conf"
  printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > "$root/etc/resolv.conf"
  # The kernel configures networking from the ip= boot argument; the upstream
  # fcnet helper would only fight over the same address.
  rm -f "$root"/etc/systemd/system/*/fcnet.service "$root"/etc/systemd/system/fcnet.service
  trim_boot "$root"
  install_envd "$root"
  install_motd "$root"
}

# install_motd replaces Ubuntu's login banner (Landscape and Pro adverts, the
# "system has been minimized" notice) with a short cracklet greeting. pam_motd
# runs the script on every login, so it can show the IP and uptime that a VM
# restored from the golden snapshot only receives from cracklet-envd.
install_motd() { # root
  local root=$1
  rm -f "$root"/etc/update-motd.d/* "$root/etc/motd"
  : > "$root/etc/legal"
  install -d -m 0755 "$root/etc/update-motd.d"
  cat > "$root/etc/update-motd.d/00-cracklet" <<'MOTD'
#!/bin/sh
# cracklet login banner. Every value is best effort so a broken guest still
# logs in; pam_motd captures the output, so colours are emitted unconditionally.
O=$(printf '\033[38;5;208m') D=$(printf '\033[2m') B=$(printf '\033[1m') R=$(printf '\033[0m')
host=$(hostname 2>/dev/null || echo '?')
ip=$(hostname -I 2>/dev/null | cut -d' ' -f1)
cpus=$(nproc 2>/dev/null || echo '?')
mem=$(awk '/^MemTotal:/ { printf "%d MiB", $2 / 1024 }' /proc/meminfo 2>/dev/null)
up=$(awk '{ s = int($1); d = int(s / 86400); h = int(s % 86400 / 3600); m = int(s % 3600 / 60)
  if (d) printf "%dd %dh", d, h; else if (h) printf "%dh %dm", h, m
  else if (m) printf "%dm %ds", m, s % 60; else printf "%ds", s }' /proc/uptime 2>/dev/null)
disk=$(df -h / 2>/dev/null | awk 'NR == 2 { print $3 " of " $2 }')
load=$(cut -d' ' -f1 /proc/loadavg 2>/dev/null)
kernel=$(uname -r 2>/dev/null)
printf '%s' "$O"
cat <<'LOGO'
                    __    __    __
  ___________ _____/ /__ / /__ / /_
 / __/ __/ _ `/ __/  '_// / -_) __/
 \__/_/  \_,_/\__/_/\_\/_/\__/\__/
LOGO
printf '%s  %s⚡ cracklet · Firecracker microVM on macOS%s\n\n' "$R" "$D" "$R"
row() { printf "  ${D}%-7s${R} ${B}%-18s${R} ${D}%-7s${R} ${B}%s${R}\n" "$1" "$2" "$3" "$4"; }
row host   "$host"      ip     "${ip:-?}"
row vcpus  "$cpus"      memory "${mem:-?}"
row uptime "${up:-?}"   kernel "${kernel:-?}"
row disk   "${disk:-?}" load   "${load:-?}"
printf '\n  %soutbound internet only · no LAN · Ctrl-D to leave%s\n\n' "$D" "$R"
MOTD
  chmod 0755 "$root/etc/update-motd.d/00-cracklet"
}

# install_envd adds the guest identity daemon that applies IP, hostname and
# clock after a snapshot restore (see build_golden / restore_vm).
install_envd() { # root
  local root=$1
  install -m 0755 "$ENVD_BIN" "$root/usr/local/bin/cracklet-envd"
  cat > "$root/etc/systemd/system/cracklet-envd.service" <<'UNIT'
[Unit]
Description=cracklet guest identity service
DefaultDependencies=no
After=sysinit.target

[Service]
ExecStart=/usr/local/bin/cracklet-envd
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
UNIT
  ln -sfn /etc/systemd/system/cracklet-envd.service "$root/etc/systemd/system/multi-user.target.wants/cracklet-envd.service"
}

# trim_boot removes everything from the first boot that a headless microVM
# does not need, so sshd is reachable a few seconds earlier.
trim_boot() { # root
  local root=$1 unit
  # multi-user instead of graphical, no virtual-console gettys
  ln -sfn /lib/systemd/system/multi-user.target "$root/etc/systemd/system/default.target"
  for unit in getty-static.service getty@tty1.service systemd-binfmt.service ldconfig.service \
      dev-hugepages.mount dev-mqueue.mount sys-kernel-debug.mount sys-kernel-tracing.mount \
      sys-kernel-config.mount systemd-journal-flush.service e2scrub_reap.service \
      apt-daily.timer apt-daily-upgrade.timer motd-news.timer fstrim.timer; do
    ln -sfn /dev/null "$root/etc/systemd/system/$unit"
  done
  # sshd as a plain service (socket activation delays the first login by a full
  # sshd start) with host keys generated now instead of on every first boot.
  # Ubuntu ties ssh.service to ssh.socket via a .requires directory, so that
  # has to go as well or the service never starts.
  rm -f "$root"/etc/systemd/system/sockets.target.wants/ssh.socket
  rm -rf "$root"/etc/systemd/system/ssh.service.requires
  ln -sfn /dev/null "$root/etc/systemd/system/ssh.socket"
  mkdir -p "$root/etc/systemd/system/multi-user.target.wants"
  ln -sfn /lib/systemd/system/ssh.service "$root/etc/systemd/system/multi-user.target.wants/ssh.service"
  ssh-keygen -A -f "$root" >/dev/null
  # the dynamic linker cache the masked ldconfig.service would have rebuilt
  chroot "$root" ldconfig 2>/dev/null || true
}

# render_firewall_batch prints the filter-table rules for iptables-restore.
# A ":CHAIN" line creates the chain or, with --noflush, flushes an existing
# one; the explicit -F keeps that behaviour independent of the backend.
render_firewall_batch() { # iface gateway
  local iface=$1 gw=$2 tap="${TAP_PREFIX}+" net
  echo '*filter'
  echo ":$CHAIN - [0:0]"
  echo ":$INPUT_CHAIN - [0:0]"
  echo "-F $CHAIN"
  echo "-F $INPUT_CHAIN"
  # Guests never reach each other, spoof their source, or leave the NAT
  # towards private destinations (the Mac's LAN address included).
  echo "-A $CHAIN -i $tap ! -s $NET -j DROP"
  echo "-A $CHAIN -i $tap -o $tap -j DROP"
  [[ -n $gw ]] && echo "-A $CHAIN -i $tap -d $gw -j REJECT"
  for net in "${PRIVATE_NETS[@]}"; do
    echo "-A $CHAIN -i $tap -d $net -j REJECT"
  done
  echo "-A $CHAIN -i $tap -o $iface -j ACCEPT"
  echo "-A $CHAIN -o $tap -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"
  echo "-A $CHAIN -i $tap -j DROP"
  # Guests never need to talk to the Lima VM; only replies to connections the
  # Lima VM opened (ssh from the agent, systemd-socket-proxyd) may come back in.
  echo "-A $INPUT_CHAIN -i $tap -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"
  echo "-A $INPUT_CHAIN -i $tap -j DROP"
  echo 'COMMIT'
}

# setup_host_network is idempotent and rebuilt on every start: microVMs reach
# the internet through NAT but neither each other, nor the LAN or the Mac
# (Lima's gateway forwards to the macOS loopback; the Mac's LAN address is a
# private destination), nor services on the Lima VM itself (port-forward
# proxies listen on 0.0.0.0 and would otherwise let one guest reach another
# guest's forwarded ports via the tap gateway address). Source addresses are
# pinned to the guest subnet so a guest cannot spoof its way past the NAT.
# Both chains end in an explicit DROP so isolation never depends on the
# default policy of INPUT/FORWARD, which is ACCEPT on Ubuntu. IPv6 is disabled
# per tap (see create_tap), so these IPv4 rules are the whole story.
setup_host_network() {
  sysctl -q -w net.ipv4.ip_forward=1
  echo 'net.ipv4.ip_forward=1' > "$SYSCTL_DROPIN"
  local iface gw
  iface=$(ip -j route list default | jq -r '.[0].dev // empty')
  gw=$(ip -j route list default | jq -r '.[0].gateway // empty')
  [[ -n $iface ]] || die "cannot determine the default network interface"

  # Both chains are rebuilt in one iptables-restore batch, which the kernel
  # commits atomically: running guests never see a flushed, empty chain
  # (FORWARD/INPUT default to ACCEPT, so even a millisecond would be a hole).
  # The jumps from INPUT/FORWARD are installed once, afterwards, so the chains
  # already carry their rules when traffic first hits them.
  render_firewall_batch "$iface" "$gw" | iptables-restore --noflush -w
  iptables -w -C FORWARD -j "$CHAIN" 2>/dev/null || iptables -w -I FORWARD -j "$CHAIN"
  iptables -w -C INPUT -j "$INPUT_CHAIN" 2>/dev/null || iptables -w -I INPUT -j "$INPUT_CHAIN"
  iptables -w -t nat -C POSTROUTING -s "$NET" ! -d "$NET" -j MASQUERADE 2>/dev/null \
    || iptables -w -t nat -A POSTROUTING -s "$NET" ! -d "$NET" -j MASQUERADE
}

# --- lifecycle ---------------------------------------------------------------

allocate_index() {
  local used i
  used=$(cat "$VMS_DIR"/*/index 2>/dev/null || true)
  for ((i = 1; i <= MAX_INDEX; i++)); do
    grep -qx "$i" <<<"$used" || { echo "$i"; return 0; }
  done
  die "no free VM slots (max $MAX_INDEX)"
}

free_auto_name() {
  local n
  for ((n = 1; n <= MAX_INDEX; n++)); do
    [[ -d $(vm_dir "vm$n") ]] || { echo "vm$n"; return 0; }
  done
  die "no free VM names"
}

write_config() { # name idx vcpus mem_mib [dir]
  local name=$1 idx=$2 vcpus=$3 mem=$4 dir=${5:-} ip gw boot_args
  [[ -n $dir ]] || dir=$(vm_dir "$name")
  ip=$(idx_ip "$idx"); gw=$(idx_gw "$idx")
  # quiet/loglevel: every kernel line on the emulated serial console costs
  # real time; the console stays attached for panics and the serial getty.
  boot_args="console=ttyS0 reboot=k panic=1 quiet loglevel=3 systemd.show_status=0"
  boot_args+=" ip=${ip}::${gw}:255.255.255.252:${name}:eth0:off systemd.hostname=${name}"
  # Disk and vsock paths are relative: Firecracker runs with the VM directory as
  # working directory, and a snapshot restored in another directory then picks
  # up that directory's copies.
  jq -n --arg kernel "$IMAGES_DIR/vmlinux" --arg boot_args "$boot_args" \
    --arg mac "$(idx_mac "$idx")" --arg tap "$(idx_tap "$idx")" \
    --argjson vcpus "$vcpus" --argjson mem "$mem" --argjson cid "$VSOCK_CID" '{
      "boot-source": {kernel_image_path: $kernel, boot_args: $boot_args},
      "drives": [{drive_id: "rootfs", path_on_host: "./rootfs.ext4", is_root_device: true, is_read_only: false}],
      "network-interfaces": [{iface_id: "eth0", guest_mac: $mac, host_dev_name: $tap}],
      "vsock": {guest_cid: $cid, uds_path: "./v.sock"},
      "machine-config": {vcpu_count: $vcpus, mem_size_mib: $mem}
    }' > "$dir/config.json.tmp"
  mv -f "$dir/config.json.tmp" "$dir/config.json"
}

grow_disk() { # path size (validated, uppercase)
  local path=$1 size=$2 before after out
  before=$(stat -c %s "$path")
  truncate -s ">$size" "$path"               # grow only, never shrink
  after=$(stat -c %s "$path")
  if [[ $after == "$before" ]]; then
    return 0
  fi
  log "growing root disk to $size"
  e2fsck -fp "$path" >/dev/null 2>&1 || (($? < 4)) || die "e2fsck failed on $path"
  out=$(resize2fs "$path" 2>&1) || die "resize2fs failed: $out"
}

cmd_new() { # NAME|- VCPUS MEM_MIB DISK [snapshot|fresh]
  [[ $# -eq 4 || $# -eq 5 ]] || die "usage: new NAME|- VCPUS MEM_MIB DISK [snapshot|fresh]"
  local name=$1 vcpus=$2 mem=$3 disk mode=${5:-snapshot} idx dir
  disk=$(to_upper "$4")
  [[ $mode == snapshot || $mode == fresh ]] || die "mode must be snapshot or fresh"
  # a grown disk cannot be restored from a snapshot taken with the base size
  [[ $disk == "$BASE_DISK_SIZE" ]] || mode=fresh
  validate_int_range "$vcpus" 1 "$MAX_VCPUS" vcpus
  validate_int_range "$mem" "$MIN_MEM_MIB" "$MAX_MEM_MIB" memory
  validate_disk "$disk"
  [[ -f $IMAGES_DIR/rootfs-base.ext4 && -f $IMAGES_DIR/vmlinux ]] || die "images missing, run 'cracklet prepare' first"
  install -d -m 0700 "$VMS_DIR"
  idx=$(allocate_index)
  if [[ $name == - ]]; then name=$(free_auto_name); fi
  validate_name "$name"
  dir=$(vm_dir "$name")
  if [[ $mode == snapshot ]]; then
    ensure_golden "$vcpus" "$mem"
  fi
  mkdir "$dir" 2>/dev/null || die "VM '$name' already exists"
  NEW_VM_NAME=$name                           # from here on, failures roll back
  atomic_write "$dir/index" "$idx"
  write_config "$name" "$idx" "$vcpus" "$mem"
  if [[ $mode == snapshot ]]; then
    cp --sparse=always "$(golden_dir "$vcpus" "$mem")/rootfs.ext4" "$dir/rootfs.ext4"
    atomic_write "$dir/golden" "$(golden_dir "$vcpus" "$mem")"
    restore_vm "$name"
  else
    log "creating root disk for $name"
    cp --sparse=always "$IMAGES_DIR/rootfs-base.ext4" "$dir/rootfs.ext4"
    grow_disk "$dir/rootfs.ext4" "$disk"
    start_vm "$name"
  fi
  NEW_VM_DONE=1
  vm_json "$name"
}

create_tap() { # idx
  local tap; tap=$(idx_tap "$1")
  setup_host_network
  ip link del "$tap" 2>/dev/null || true
  ip tuntap add dev "$tap" mode tap
  # The firewall is IPv4-only; without this a guest could reach the Lima VM
  # over IPv6 link-local addresses.
  sysctl -q -w "net.ipv6.conf.$tap.disable_ipv6=1"
  ip addr add "$(idx_gw "$1")/30" dev "$tap"
  ip link set dev "$tap" up
}

# run_firecracker starts a sandboxed, transient unit with the VM directory as
# working directory; extra arguments are passed to firecracker.
run_firecracker() { # name dir mem_mib firecracker-args...
  local name=$1 dir=$2 mem=$3 unit; shift 3
  unit=$(vm_unit "$name")
  rm -f "$dir/fc.sock" "$dir/v.sock"
  systemctl reset-failed "$unit" 2>/dev/null || true
  systemd-run --quiet --unit "$unit" --description "cracklet microVM $name" \
    --working-directory "$dir" \
    --property StandardInput=null \
    --property "StandardOutput=truncate:$dir/console.log" \
    --property StandardError=inherit \
    --property KillSignal=SIGTERM \
    --property "TimeoutStopSec=$STOP_WAIT_SECONDS" \
    --property NoNewPrivileges=yes \
    --property PrivateTmp=yes \
    --property ProtectHome=yes \
    --property DevicePolicy=closed \
    --property "DeviceAllow=/dev/kvm rw" \
    --property "DeviceAllow=/dev/net/tun rw" \
    --property "MemoryMax=$((mem + VMM_OVERHEAD_MIB))M" \
    "$FC_BIN" --api-sock ./fc.sock "$@"
}

fc_api() { # dir method path [json]
  curl -s --fail-with-body --unix-socket "$1/fc.sock" -X "$2" -H 'Content-Type: application/json' \
    ${4:+-d "$4"} "http://localhost$3"
}

wait_for_api() { # dir
  local deadline=$((SECONDS + FC_API_WAIT_SECONDS))
  while ((SECONDS < deadline)); do
    [[ -S $1/fc.sock ]] && fc_api "$1" GET / >/dev/null 2>&1 && return 0
    sleep 0.05
  done
  return 1
}

# --- golden snapshot ---------------------------------------------------------
# A golden snapshot is a fully booted VM (sshd and cracklet-envd running) frozen with
# its memory. `cracklet new` restores it in a fraction of the cold-boot time and then
# hands the VM its real identity over vsock.

golden_dir() { echo "$IMAGES_DIR/golden-$1-$2"; }   # vcpus mem

golden_stamp() {
  echo "rev$GOLDEN_REV|$(cat "$IMAGES_DIR/rootfs-base.stamp")|$(cat "$IMAGES_DIR/vmlinux.stamp")|$("$FC_BIN" --version | head -1)"
}

ensure_golden() { # vcpus mem_mib
  local dir; dir=$(golden_dir "$1" "$2")
  if [[ -f $dir/vmstate && -f $dir/mem && -f $dir/rootfs.ext4 && $(cat "$dir/stamp" 2>/dev/null) == "$(golden_stamp)" ]]; then
    return 0
  fi
  build_golden "$1" "$2"
}

build_golden() { # vcpus mem_mib
  local vcpus=$1 mem=$2 dir work ip
  dir=$(golden_dir "$vcpus" "$mem"); ip=$(idx_ip "$GOLDEN_INDEX")
  log "building golden snapshot ($vcpus vCPU, $mem MiB); this happens once per size"
  rm -rf "$dir" "$IMAGES_DIR"/golden-build.*
  work=$(mktemp -d "$IMAGES_DIR/golden-build.XXXXXX"); TMP_DIRS+=("$work")
  cp --sparse=always "$IMAGES_DIR/rootfs-base.ext4" "$work/rootfs.ext4"
  write_config golden "$GOLDEN_INDEX" "$vcpus" "$mem" "$work"
  create_tap "$GOLDEN_INDEX"
  run_firecracker golden "$work" "$mem" --config-file ./config.json
  if ! wait_for_ssh golden "$ip"; then
    tail -n 20 "$work/console.log" >&2 || true
    systemctl stop "$(vm_unit golden)" 2>/dev/null || true
    die "golden VM did not come up"
  fi
  # let first-boot work settle and flush the disk before freezing
  sleep 1
  ssh_guest "$ip" 'sync' >/dev/null 2>&1 || true
  fc_api "$work" PATCH /vm '{"state":"Paused"}' >/dev/null
  fc_api "$work" PUT /snapshot/create '{"snapshot_type":"Full","snapshot_path":"./vmstate","mem_file_path":"./mem"}' >/dev/null
  systemctl stop "$(vm_unit golden)"
  systemctl reset-failed "$(vm_unit golden)" 2>/dev/null || true
  ip link del "$(idx_tap "$GOLDEN_INDEX")" 2>/dev/null || true
  rm -f "$work"/fc.sock "$work"/v.sock* "$work"/console.log
  chmod 0600 "$work"/mem "$work"/vmstate "$work"/rootfs.ext4
  golden_stamp > "$work/stamp"
  mv "$work" "$dir"
  TMP_DIRS=("${TMP_DIRS[@]/$work}")
  log "golden snapshot ready ($(du -sh "$dir/mem" | cut -f1) memory image)"
}

identity_json() { # name idx
  jq -cn --arg ip "$(idx_ip "$2")" --arg gw "$(idx_gw "$2")" --arg host "$1" \
    --argjson now "$(date +%s)" --arg seed "$(head -c 32 /dev/urandom | base64 -w0)" \
    '{ip: $ip, prefix: 30, gateway: $gw, hostname: $host, iface: "eth0", unix_time: $now, seed: $seed}'
}

# send_identity talks to cracklet-envd through Firecracker's vsock Unix socket
# (CONNECT <port> handshake) and expects an "OK" reply.
send_identity() { # dir json
  python3 - "$1/v.sock" "$VSOCK_PORT" "$2" <<'PYEOF'
import socket, sys, time
path, port, payload = sys.argv[1], sys.argv[2], sys.argv[3]
deadline = time.time() + 10
last = ""
while time.time() < deadline:
    try:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(3)
        s.connect(path)
        s.sendall(("CONNECT %s\n" % port).encode())
        if not s.recv(64).startswith(b"OK"):
            raise OSError("vsock port not accepting yet")
        s.sendall(payload.encode() + b"\n")
        answer = s.recv(512).decode().strip()
        if answer.startswith("OK"):
            sys.exit(0)
        sys.exit("cracklet-envd rejected identity: " + answer)
    except OSError as e:
        last = str(e)
        time.sleep(0.1)
sys.exit("could not deliver identity: " + last)
PYEOF
}

restore_vm() { # name
  local name=$1 dir idx ip golden mem
  dir=$(vm_dir "$name"); idx=$(vm_index "$name"); ip=$(idx_ip "$idx"); golden=$(cat "$dir/golden")
  mem=$(jq -r '."machine-config".mem_size_mib' "$dir/config.json")
  create_tap "$idx"
  run_firecracker "$name" "$dir" "$mem"
  wait_for_api "$dir" || die "firecracker API did not come up for $name"
  fc_api "$dir" PUT /snapshot/load "$(jq -cn --arg st "$golden/vmstate" --arg mem "$golden/mem" --arg tap "$(idx_tap "$idx")" \
    '{snapshot_path: $st, mem_backend: {backend_type: "File", backend_path: $mem}, resume_vm: true,
      network_overrides: [{iface_id: "eth0", host_dev_name: $tap}], vsock_override: {uds_path: "./v.sock"}}')" >/dev/null \
    || die "snapshot load failed for $name (see $dir/console.log)"
  send_identity "$dir" "$(identity_json "$name" "$idx")"
  add_host_entry "$name" "$ip"
  if ! wait_for_ssh "$name" "$ip"; then
    tail -n 20 "$dir/console.log" >&2 || true
    stop_vm "$name" force >/dev/null 2>&1 || true
    die "$name was restored but SSH did not answer"
  fi
  apply_forwards "$name"
}

discard_vm() { # name
  log "rolling back incomplete VM '$1'"
  stop_vm "$1" force >/dev/null 2>&1 || true
  remove_host_entry "$1" || true
  rm -rf "$(vm_dir "$1")"
}

start_vm() { # name
  local name=$1 dir idx tap unit ip mem
  dir=$(vm_dir "$name"); idx=$(vm_index "$name"); unit=$(vm_unit "$name")
  if ! is_index "$idx" || [[ ! -s $dir/config.json ]]; then
    die "VM '$name' is incomplete; remove it with 'cracklet rm $name'"
  fi
  tap=$(idx_tap "$idx"); ip=$(idx_ip "$idx")
  if unit_active "$name"; then
    add_host_entry "$name" "$ip"
    log "$name is already running"
    return 0
  fi
  mem=$(jq -r '."machine-config".mem_size_mib' "$dir/config.json")
  create_tap "$idx"
  log "booting $name (console: $dir/console.log)"
  run_firecracker "$name" "$dir" "$mem" --config-file ./config.json
  add_host_entry "$name" "$ip"
  if ! wait_for_ssh "$name" "$ip"; then
    tail -n 20 "$dir/console.log" >&2 || true
    stop_vm "$name" force >/dev/null 2>&1 || true
    die "$name did not answer on SSH within ${SSH_WAIT_SECONDS}s (console tail above)"
  fi
  apply_forwards "$name"
}

stop_vm() { # name [force]
  local name=$1 force=${2:-} idx unit deadline
  idx=$(vm_index "$name"); unit=$(vm_unit "$name")
  if unit_active "$name"; then
    # aarch64 Firecracker has no ACPI power button; a guest reboot makes it exit.
    if [[ $force != force ]] && is_index "$idx"; then
      ssh_guest "$(idx_ip "$idx")" reboot >/dev/null 2>&1 || true
      deadline=$((SECONDS + STOP_WAIT_SECONDS))
      while ((SECONDS < deadline)) && unit_active "$name"; do sleep 0.25; done
    fi
    unit_active "$name" && systemctl stop "$unit"
  fi
  systemctl reset-failed "$unit" 2>/dev/null || true
  remove_forward_units "$name"
  if is_index "$idx"; then
    ip link del "$(idx_tap "$idx")" 2>/dev/null || true
  fi
  rm -f "$(vm_dir "$name")/fc.sock" "$(vm_dir "$name")/v.sock"
}

cmd_start() { require_vm "$1"; start_vm "$1"; vm_json "$1"; }
cmd_stop()  { require_vm "$1"; stop_vm "$1"; vm_json "$1"; }

cmd_rm() {
  require_vm "$1"
  stop_vm "$1" force
  remove_host_entry "$1"
  rm -rf "$(vm_dir "$1")"
}

cmd_ls() {
  local out="[]" dir name
  for dir in "$VMS_DIR"/*/; do
    [[ -d $dir ]] || continue
    name=$(basename "$dir")
    out=$(jq -c --argjson vm "$(vm_json "$name")" '. + [$vm]' <<<"$out")
  done
  echo "$out"
}

# --- port forwarding ---------------------------------------------------------
# A transient systemd socket on the Lima VM (0.0.0.0:HOST) activates
# systemd-socket-proxyd towards GUEST_IP:GUEST. Lima notices the listening
# socket and exposes it on the Mac as 127.0.0.1:HOST.

forward_owner() { # hostport -> name of the VM that owns it, if any
  local f
  for f in "$VMS_DIR"/*/forwards; do
    [[ -f $f ]] || continue
    if grep -qx "$1:[0-9]*" "$f"; then basename "$(dirname "$f")"; return 0; fi
  done
  return 0
}

apply_forward() { # name host guest
  local name=$1 host=$2 guest=$3 idx unit
  idx=$(vm_index "$name"); unit=$(fwd_unit "$name" "$host")
  systemctl stop "$unit.socket" "$unit.service" 2>/dev/null || true
  systemctl reset-failed "$unit.socket" "$unit.service" 2>/dev/null || true
  systemd-run --quiet --unit "$unit" --description "cracklet forward $name :$host -> :$guest" \
    --socket-property "ListenStream=0.0.0.0:$host" \
    --socket-property NoDelay=yes \
    --property NoNewPrivileges=yes \
    --property PrivateTmp=yes \
    --property ProtectHome=yes \
    "$PROXYD" "$(idx_ip "$idx"):$guest" </dev/null
}

apply_forwards() { # name (VM must be running); failures are reported, not fatal
  local name=$1 file host guest
  file=$(fwd_file "$name")
  [[ -f $file ]] || return 0
  while IFS=: read -r host guest; do
    [[ -n $host ]] || continue
    apply_forward "$name" "$host" "$guest" || log "warning: could not forward :$host -> $name:$guest"
  done < "$file"
}

remove_forward_units() { # name [hostport] (all of the VM's forwards when omitted)
  local name=$1 host=${2:-} file h
  if [[ -n $host ]]; then
    systemctl stop "$(fwd_unit "$name" "$host").socket" "$(fwd_unit "$name" "$host").service" 2>/dev/null || true
    return 0
  fi
  file=$(fwd_file "$name")
  [[ -f $file ]] || return 0
  while IFS=: read -r h _; do
    if [[ -n $h ]]; then remove_forward_units "$name" "$h"; fi
  done < "$file"
}

save_forward() { # name host guest (replaces an existing entry for host)
  local file; file=$(fwd_file "$1")
  if [[ -f $file ]]; then grep -v "^$2:" "$file" > "$file.tmp" || true; else : > "$file.tmp"; fi
  echo "$2:$3" >> "$file.tmp"
  mv -f "$file.tmp" "$file"
}

drop_forward() { # name host
  local file; file=$(fwd_file "$1")
  [[ -f $file ]] || return 0
  grep -v "^$2:" "$file" > "$file.tmp" || true
  mv -f "$file.tmp" "$file"
}

cmd_forward() { # NAME [HOST:]GUEST...
  local name=$1; shift
  require_vm "$name"
  (($# >= 1)) || die "usage: forward NAME [HOST:]GUEST..."
  local spec host guest owner
  for spec in "$@"; do
    read -r host guest <<<"$(parse_forward "$spec")"
    owner=$(forward_owner "$host")
    if [[ -n $owner && $owner != "$name" ]]; then
      die "host port $host is already forwarded to VM '$owner'"
    fi
    if unit_active "$name"; then
      apply_forward "$name" "$host" "$guest"
    fi
    save_forward "$name" "$host" "$guest"
  done
  vm_json "$name"
}

cmd_unforward() { # NAME HOSTPORT...
  local name=$1; shift
  require_vm "$name"
  (($# >= 1)) || die "usage: unforward NAME HOSTPORT..."
  local host
  for host in "$@"; do
    validate_port "$host" 1 host
    remove_forward_units "$name" "$host"
    drop_forward "$name" "$host"
  done
  vm_json "$name"
}

# --- main --------------------------------------------------------------------

acquire_lock() {
  exec 9>"$LOCK_FILE"
  flock -w "$LOCK_WAIT_SECONDS" 9 || die "another cracklet operation is still running"
}

main() {
  [[ $EUID -eq 0 ]] || die "the agent must run as root"
  install -d -m 0700 "$CRACKLET_ROOT"
  local cmd=${1:-}
  if [[ -n $cmd ]]; then shift; fi
  trap on_exit EXIT
  case $cmd in
    prepare|new|start|stop|rm|forward|unforward) acquire_lock ;;
  esac
  case $cmd in
    prepare) cmd_prepare "$@" ;;
    new)     cmd_new "$@" ;;
    start)   [[ $# -eq 1 ]] || die "usage: start NAME"; cmd_start "$1" ;;
    stop)    [[ $# -eq 1 ]] || die "usage: stop NAME"; cmd_stop "$1" ;;
    rm)      [[ $# -eq 1 ]] || die "usage: rm NAME"; cmd_rm "$1" ;;
    ls)      cmd_ls ;;
    forward)   [[ $# -ge 2 ]] || die "usage: forward NAME [HOST:]GUEST..."; cmd_forward "$@" ;;
    unforward) [[ $# -ge 2 ]] || die "usage: unforward NAME HOSTPORT..."; cmd_unforward "$@" ;;
    *)       die "usage: agent.sh {prepare|new|start|stop|rm|ls|forward|unforward} ..." ;;
  esac
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
  exit $?
fi
