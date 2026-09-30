# llok

Run coding agents inside locked microVMs.

Every agent run gets a throwaway VM with your repo inside and nothing else:
no network device, no host filesystem, no credentials. The only way out is a
single audited channel to a broker on the host, and that broker enforces the
policy in your repo's `llok.yaml`.

Status: early. Step 1 (boot a locked VM with the repo mounted) works on
Apple silicon Macs: guest init runs ~120 ms after start, a full boot, command,
and power-off cycle takes ~250 ms. The broker, egress policy, and
Linux/Firecracker backend come next.

## Requirements

- Apple silicon Mac on macOS 26 or newer
- Go 1.26 or newer
- Xcode command line tools (for `codesign`)

## Quick start

```bash
make guest        # builds ./llok and downloads the Alpine kernel + rootfs into ~/.llok
llok doctor       # checks hypervisor support, code signing, guest image
cd your-repo
llok init         # interactive setup, writes llok.yaml (use --yes for defaults)
llok up           # shell inside the VM, repo at /repo; `exit` powers off
llok up -c 'ls /repo && ip link'   # run one command and power off
```

Inside the VM you get a banner and a `llok@project /repo #` prompt so it is
always obvious you are in the sandbox. Press Ctrl-] twice to force the VM off
from the host if the guest is wedged.

## How it works

- Host: Go + Apple's Virtualization framework via Code-Hex/vz.
- Guest: Alpine's `linux-virt` kernel (unwrapped from its EFI zboot stub, since
  the Virtualization framework only loads a raw arm64 Image) and an initramfs
  built from Alpine's minirootfs with a tiny `/init`.
- Repo sharing: virtiofs, read-write, mounted at `/repo`.
- Network: no virtio-net device is attached, so the guest has only `lo`.
- vsock: attached now, used by the broker in a later step.

## License

Apache 2.0.
