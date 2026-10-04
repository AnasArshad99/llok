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

# The agent runs as this unprivileged user; root is reserved for llok's own
# agent so a compromised coding agent cannot tamper with it.
chroot /mnt /usr/sbin/adduser -D -u 1000 -s /bin/bash -h /home/agent agent

mount -t proc proc /mnt/proc
mount -t sysfs sys /mnt/sys
mount --bind /dev /mnt/dev

# Install every agent llok knows (name|binary|install line), inside the
# chroot as root, then expose each binary on the default PATH for the agent
# user: installers tend to drop files under /root.
: > /mnt/etc/llok/agents-info
# (Every command in the loop gets </dev/null: installers like npm read
# stdin, which would otherwise be the agent list itself.)
while IFS='|' read -r NAME BIN INSTALL <&3; do
  [ -n "$NAME" ] || continue
  log "installing $NAME"
  if ! chroot /mnt /bin/bash -lc "$INSTALL" >"/tmp/install-$NAME.log" 2>&1 </dev/null; then
    log "$NAME: install failed (continuing without it)"; tail -3 "/tmp/install-$NAME.log"
    echo "$NAME failed" >> /mnt/etc/llok/agents-info
    continue
  fi
  SRC=$(chroot /mnt /bin/bash -lc "PATH=/root/.local/bin:/root/.opencode/bin:/root/.bun/bin:\$PATH command -v $BIN" 2>/dev/null | tail -1)
  if [ -n "$SRC" ] && [ "$SRC" != "/usr/local/bin/$BIN" ] && [ "$SRC" != "/usr/bin/$BIN" ]; then
    chroot /mnt /bin/sh -c "cp -L '$SRC' '/usr/local/bin/$BIN.new' && mv '/usr/local/bin/$BIN.new' '/usr/local/bin/$BIN'" 2>/dev/null \
      || chroot /mnt /bin/sh -c "ln -sf '$SRC' '/usr/local/bin/$BIN'"
  fi
  chmod 0755 "/mnt/usr/local/bin/$BIN" 2>/dev/null || true
  VER=$(chroot /mnt /bin/sh -c "su agent -s /bin/sh -c '$BIN --version 2>/dev/null | head -1'" 2>/dev/null | tr -d '\r' | cut -c1-60)
  SHA=$(chroot /mnt /bin/sh -c "sha256sum \$(readlink -f /usr/local/bin/$BIN 2>/dev/null || command -v $BIN) 2>/dev/null" | cut -d' ' -f1)
  if [ -n "$VER" ]; then
    log "$NAME: $VER (sha256 ${SHA:0:16}…)"
    echo "$NAME $VER sha256:$SHA" >> /mnt/etc/llok/agents-info
  else
    log "$NAME: installed but not runnable as the agent user"
    echo "$NAME unverified" >> /mnt/etc/llok/agents-info
  fi
done 3< /etc/llok/agents.list
umount /mnt/dev /mnt/sys /mnt/proc

AGENTS_OK=$(grep -v 'failed\|unverified' /mnt/etc/llok/agents-info | cut -d' ' -f1 | tr '\n' ' ' | sed 's/ $//')
echo "alpine $(cat /mnt/etc/alpine-release) · agents: ${AGENTS_OK:-none}" > /mnt/etc/llok/rootfs-info
# Tell the host what went in, out of band (the host records it in the manifest).
printf '\033]7777;LLOK_AGENTS %s\033\\' "$(base64 < /mnt/etc/llok/agents-info | tr -d '\n')"
log "root disk uses $(du -sh /mnt 2>/dev/null | cut -f1)"
umount /mnt
sync
log "done"
