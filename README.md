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

## Install

```bash
git clone https://github.com/muhammadanas/llok && cd llok
make install      # builds, signs, and installs llok into ~/.local/bin
llok guest build  # builds the guest image into ~/.llok (once, a few minutes)
```

`llok guest build` downloads Alpine's kernel, assembles the initramfs, then
boots a one-off builder VM that installs Alpine, git, Node, ripgrep and
Claude Code onto a root disk. That builder is the only VM that ever gets a
network device, and it never sees a repo.

Set `PREFIX=/usr/local` to install system-wide. Homebrew tap and a one-line
installer are planned once there are tagged releases.

## Quick start

```bash
llok doctor       # checks hypervisor support, code signing, guest image
cd your-repo
llok init         # interactive setup, writes llok.yaml (use --yes for defaults)
llok up           # shell inside the VM, repo at /repo; `exit` powers off
llok up -c 'ls /repo && ip link'   # run one command and power off
```

`llok up` takes over the terminal: the guest console on the left, a status
panel on the right (state, repo mapping, VM size, network, uptime, activity).
Inside the VM you get a banner and a `llok@project /repo #` prompt so it is
always obvious you are in the sandbox. Press Ctrl-] twice to force the VM off
from the host. Use `llok up --plain` for a raw console without the panel.

The path `/repo` is where your host folder appears inside the guest. It is
the only writable thing that survives power-off; change it with `mount.repo`
in `llok.yaml`.

## How it works

- Host: Go + Apple's Virtualization framework via Code-Hex/vz.
- Guest: Alpine's `linux-virt` kernel (unwrapped from its EFI zboot stub, since
  the Virtualization framework only loads a raw arm64 Image), an initramfs
  built from Alpine's minirootfs with a tiny `/init`, and an ext4 root disk
  with the toolchain and Claude Code.
- Root disk: attached read-only, with a tmpfs overlay on top. Everything the
  agent changes outside the repo disappears at power-off.
- Repo sharing: virtiofs, read-write, mounted at `/repo`.
- Network: no virtio-net device is attached to session VMs, so the guest has
  only `lo`. Claude Code is installed but cannot reach the API until the
  broker lands (next step).
- vsock: attached now, used by the broker in a later step.

## License

Apache 2.0.
