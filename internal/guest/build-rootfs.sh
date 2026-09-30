#!/bin/sh
# Runs inside the builder VM (the only VM that ever gets a network device).
# Installs Alpine + tooling + Claude Code onto the blank disk at /dev/vda.
set -e
log() { echo "llok build: $*"; }

BR=$(cat /etc/llok/alpine-branch 2>/dev/null || echo v3.22)
MIRROR=https://dl-cdn.alpinelinux.org/alpine

log "bringing up network"
ip link set lo up
ip link set eth0 up
udhcpc -i eth0 -q -n -t 15 >/dev/null 2>&1 || { echo "llok build: dhcp failed" >&2; exit 2; }
log "address $(ip -4 -o addr show eth0 | awk '{print $4}') · dns $(awk '/nameserver/{print $2; exit}' /etc/resolv.conf)"

log "formatting root disk"
apk add --no-cache -q --no-progress e2fsprogs
mkfs.ext4 -q -F -L llok-root /dev/vda
mkdir -p /mnt
mount /dev/vda /mnt

log "installing alpine $BR base and toolchain (this downloads ~150 MB)"
mkdir -p /mnt/etc/apk/keys /mnt/etc/llok
cp /etc/apk/keys/* /mnt/etc/apk/keys/
printf '%s/%s/main\n%s/%s/community\n' "$MIRROR" "$BR" "$MIRROR" "$BR" > /mnt/etc/apk/repositories
apk -p /mnt --initdb -U --no-cache -q --no-progress add \
  alpine-base bash coreutils findutils grep sed less procps \
  git curl wget ca-certificates openssh-client \
  ripgrep jq tar gzip unzip xz \
  nodejs npm python3
echo llok > /mnt/etc/hostname
cp /etc/resolv.conf /mnt/etc/resolv.conf

log "installing Claude Code"
mount -t proc proc /mnt/proc
mount -t sysfs sys /mnt/sys
mount --bind /dev /mnt/dev
if ! chroot /mnt /bin/bash -lc 'curl -fsSL https://claude.ai/install.sh | bash' >/tmp/claude-install.log 2>&1; then
  log "native installer failed, falling back to npm"; tail -5 /tmp/claude-install.log
  chroot /mnt /bin/bash -lc 'npm install -g @anthropic-ai/claude-code' >/tmp/claude-install.log 2>&1 \
    || { tail -20 /tmp/claude-install.log; exit 3; }
fi
# The native installer puts a symlink in /root/.local/bin (absolute target, so
# it only resolves inside the chroot). Expose it on the default PATH.
mkdir -p /mnt/usr/local/bin
if [ -e /mnt/root/.local/bin/claude ]; then
  ln -sf /root/.local/bin/claude /mnt/usr/local/bin/claude
fi
CLAUDE_VER=$(chroot /mnt /bin/bash -c 'PATH=/root/.local/bin:/usr/local/bin:$PATH claude --version' 2>&1 | head -1 | tr -d '\r')
[ -n "$CLAUDE_VER" ] || { echo "llok build: claude not runnable after install" >&2; tail -20 /tmp/claude-install.log; exit 4; }
log "claude code: $CLAUDE_VER"
umount /mnt/dev /mnt/sys /mnt/proc

echo "alpine $(cat /mnt/etc/alpine-release) · claude-code $CLAUDE_VER" > /mnt/etc/llok/rootfs-info
log "root disk uses $(du -sh /mnt 2>/dev/null | cut -f1)"
umount /mnt
sync
log "done"
