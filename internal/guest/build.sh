#!/bin/bash
# Builds the llok guest: an uncompressed arm64 Linux kernel (Alpine linux-virt)
# and an initramfs made from Alpine's minirootfs plus our /init.
# Outputs: $LLOK_HOME/guest/Image and $LLOK_HOME/guest/initrd.cpio.gz
set -euo pipefail

LLOK_HOME=${LLOK_HOME:-$HOME/.llok}
ALPINE_BRANCH=${ALPINE_BRANCH:-v3.22}
ARCH=aarch64
MIRROR=${ALPINE_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}

CACHE=$LLOK_HOME/cache
OUT=$LLOK_HOME/guest
WORK=$LLOK_HOME/build
INIT_SRC=$WORK/init
mkdir -p "$CACHE" "$OUT" "$WORK"

log() { echo "llok guest: $*"; }
fetch() { # url dest
  if [ ! -s "$2" ]; then log "downloading $(basename "$2")"; curl -fsSL -o "$2.part" "$1" && mv "$2.part" "$2"; fi
}

# Resolve versions.
fetch "$MIRROR/$ALPINE_BRANCH/releases/$ARCH/latest-releases.yaml" "$CACHE/latest-releases.yaml"
ROOTFS_FILE=$(awk '/minirootfs/{f=1} f&&/file:/{print $2; exit}' "$CACHE/latest-releases.yaml")
fetch "$MIRROR/$ALPINE_BRANCH/main/$ARCH/APKINDEX.tar.gz" "$CACHE/APKINDEX.tar.gz"
KVER=$(tar -xOzf "$CACHE/APKINDEX.tar.gz" APKINDEX 2>/dev/null | awk '/^P:linux-virt$/{f=1} f&&/^V:/{sub("V:","");v=$0;f=0} END{print v}')
[ -n "$ROOTFS_FILE" ] && [ -n "$KVER" ] || { echo "could not resolve Alpine versions" >&2; exit 1; }
log "alpine rootfs $ROOTFS_FILE, kernel linux-virt $KVER"

fetch "$MIRROR/$ALPINE_BRANCH/releases/$ARCH/$ROOTFS_FILE" "$CACHE/$ROOTFS_FILE"
fetch "$MIRROR/$ALPINE_BRANCH/main/$ARCH/linux-virt-$KVER.apk" "$CACHE/linux-virt-$KVER.apk"

# Unpack the kernel package (an apk is a tar.gz with extra headers bsdtar warns about).
KDIR=$WORK/kernel
rm -rf "$KDIR"; mkdir -p "$KDIR"
tar -xzf "$CACHE/linux-virt-$KVER.apk" -C "$KDIR" 2>/dev/null || true
KIMG=$(ls "$KDIR"/boot/vmlinuz-virt)
RAW=$WORK/kernel.raw
if gzip -t "$KIMG" 2>/dev/null; then gunzip -c "$KIMG" > "$RAW"; else cp "$KIMG" "$RAW"; fi
# Alpine wraps the kernel in an EFI zboot stub (magic "zimg" at offset 4).
# The Virtualization framework needs the raw arm64 Image inside it.
if [ "$(dd if="$RAW" bs=1 skip=4 count=4 2>/dev/null)" = "zimg" ]; then
  read -r ZOFF ZLEN < <(dd if="$RAW" bs=1 skip=8 count=8 2>/dev/null | od -An -tu4 | tr -s ' ')
  ZCOMP=$(dd if="$RAW" bs=1 skip=24 count=8 2>/dev/null | tr -d '\0')
  log "unwrapping zboot ($ZCOMP payload, $ZLEN bytes at $ZOFF)"
  case "$ZCOMP" in
    gzip) dd if="$RAW" bs=1 skip="$ZOFF" count="$ZLEN" 2>/dev/null | gunzip -c > "$OUT/Image" ;;
    *) echo "unsupported zboot compression: $ZCOMP" >&2; exit 1 ;;
  esac
else
  cp "$RAW" "$OUT/Image"
fi
[ "$(dd if="$OUT/Image" bs=1 skip=56 count=4 2>/dev/null)" = "ARMd" ] || { echo "kernel is not a raw arm64 Image" >&2; exit 1; }
log "kernel -> $OUT/Image ($(du -h "$OUT/Image" | cut -f1))"
basename "$(ls -d "$KDIR"/lib/modules/*)" > "$OUT/kernel.version"

# Assemble the rootfs.
ROOT=$WORK/rootfs
rm -rf "$ROOT"; mkdir -p "$ROOT"
tar -xzf "$CACHE/$ROOTFS_FILE" -C "$ROOT"
install -m 0755 "$INIT_SRC" "$ROOT/init"
mkdir -p "$ROOT/etc/llok" "$ROOT/lib/modules/llok" "$ROOT/repo"
install -m 0755 "$WORK/build-rootfs.sh" "$ROOT/etc/llok/build-rootfs.sh"
echo "$ALPINE_BRANCH" > "$ROOT/etc/llok/alpine-branch"

# Only the modules we need, decompressed so busybox insmod takes them by path.
MODDIR=$(ls -d "$KDIR"/lib/modules/*)
: > "$ROOT/etc/llok/modules"
for name in fuse virtiofs vsock vmw_vsock_virtio_transport_common vmw_vsock_virtio_transport \
            virtio_blk crc16 mbcache jbd2 crc32c_generic ext4 overlay \
            failover net_failover virtio_net; do
  src=$(find "$MODDIR" -name "$name.ko*" | head -1 || true)
  [ -n "$src" ] || continue
  case "$src" in
    *.ko.gz) gunzip -c "$src" > "$ROOT/lib/modules/llok/$name.ko" ;;
    *)       cp "$src" "$ROOT/lib/modules/llok/$name.ko" ;;
  esac
  echo "/lib/modules/llok/$name.ko" >> "$ROOT/etc/llok/modules"
done
log "modules: $(tr '\n' ' ' < "$ROOT/etc/llok/modules")"

# Pack as a newc cpio, owned by root.
(cd "$ROOT" && find . | bsdtar --format newc --uid 0 --gid 0 -cf - -T - ) | gzip -6 > "$OUT/initrd.cpio.gz"
log "initrd -> $OUT/initrd.cpio.gz ($(du -h "$OUT/initrd.cpio.gz" | cut -f1))"
log "done"
