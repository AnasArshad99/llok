<p align="center">
  <img src="docs/logo.svg" alt="llok" width="220">
</p>

<p align="center">
  <b>Run coding agents inside locked microVMs.</b><br>
  Your repo goes in. One audited channel comes out. Nothing else.
</p>

<p align="center">
  <a href="https://github.com/AnasArshad99/llok/actions/workflows/ci.yml"><img src="https://github.com/AnasArshad99/llok/actions/workflows/ci.yml/badge.svg" alt="ci"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="license"></a>
  <img src="https://img.shields.io/badge/platform-macOS%2026%20%C2%B7%20Apple%20silicon-lightgrey.svg" alt="platform">
</p>

---

Coding agents run shell commands, install packages, and read whatever a web page tells them to. llok gives each agent session its own throwaway virtual machine with **no network card**, your repository as the **only** shared folder, and a broker on your Mac as the **only** way out. The broker lets the agent reach its provider's API with your credential injected on the way, refuses everything else, and logs every decision.

```
   your Mac                                    microVM (boots in 0.2 s)
   ┌─────────────────────────────┐             ┌──────────────────────────────┐
   │  llok                       │             │  Alpine · read-only root     │
   │   ├─ Keychain ─► broker ◄───┼── vsock ────┼─► agent (Claude Code, …)     │
   │   │        allowlist, audit │             │       ▲                      │
   │   └─ ~/your-repo ───────────┼── virtiofs ─┼─► /repo (the only writable   │
   │                             │             │         thing that persists) │
   └──────────────┬──────────────┘             └──────────────────────────────┘
                  ▼
        api.anthropic.com  (and only what llok.yaml allows)
```

## Install

```bash
git clone https://github.com/AnasArshad99/llok && cd llok
make install        # builds, signs, installs into ~/.local/bin
llok guest build    # builds the VM image into ~/.llok  (once, a few minutes)
```

Requires an Apple silicon Mac on macOS 26 or newer, Go 1.26, and the Xcode command line tools.

## Use

```bash
llok auth claude    # sign in once; the token goes to your Keychain, never the VM
cd your-repo
llok init           # writes llok.yaml
llok up             # boots the VM and takes over the terminal; run `claude` inside
```

`llok up` shows the VM console on the left and a live panel on the right: state, repo mapping, VM size, network policy, uptime, and every request the broker allowed or refused.

```
 llok@project /repo # claude                            │ ● running
 ╭─ Claude Code ─────────────────────────╮              │ project   my-app
 │ › fix the failing test in auth.go     │              │ repo      ~/code/my-app
 ╰───────────────────────────────────────╯              │ network   broker · api.anthropic.com
                                                        │ activity
                                                        │  → POST /v1/messages 200 1.9s
                                                        │  ✗ CONNECT www.google.com denied
```

| Command | What it does |
|---|---|
| `llok up` | Interactive session. `exit` powers off, Ctrl-] twice forces it |
| `llok up -c 'make test'` | Run one command in the VM, return its exit code |
| `llok up --plain` | Raw console, no panel |
| `llok auth claude` | Store your Claude login. `--api-key` stores a key instead |
| `llok doctor` | Check hypervisor support, code signing, guest image |
| `llok guest build` | Rebuild the VM image (for a newer agent version) |

## Policy

`llok init` writes a `llok.yaml` next to your code. It is the whole policy.

```yaml
agent: claude-code
vm:
  cpus: 2
  memory_mb: 1024
mount:
  repo: /repo             # where your folder appears inside the VM
network:
  mode: broker            # or "none" for no way out at all
  allow:                  # extra hosts the agent may reach; everything else is refused
    - registry.npmjs.org
    - "*.github.com"
```

## What the VM can and cannot do

| | |
|---|---|
| Read and write your repo | yes, and only your repo |
| Reach the provider API | yes, through the broker, with your credential injected there |
| Reach any other host | only if listed under `network.allow` |
| See your credentials | no. The VM holds a placeholder; the broker swaps it on the way out |
| Change its own system | only until power-off. The root disk is read-only with a memory overlay |
| Touch your Mac | no. No shared folders beyond the repo, no host network, no host devices |

Every request is appended to `~/.llok/audit/broker.jsonl`.

## How it works

- **Hypervisor.** Apple's Virtualization framework, driven from Go. Boot to a ready shell takes about 0.2 seconds.
- **Guest.** Alpine's `linux-virt` kernel, a 6 MB initramfs with llok's init, and an ext4 root disk holding bash, git, Node, ripgrep and the agent. Sessions mount the disk read-only with a tmpfs overlay.
- **Image build.** `llok guest build` boots a one-off builder VM with NAT to install packages. It is the only VM that ever gets a network device, and it never sees a repo.
- **Broker.** A small agent inside the guest forwards `127.0.0.1:3128` over vsock to llok on the host. Provider API calls are reverse-proxied with the real credential; TLS to the provider's hosts is terminated with a local CA the guest trusts, so injection works there too. Other allowed hosts get a plain tunnel with nothing injected.
- **Control channel.** A second vsock port carries resize and shutdown, so window changes reach the agent's UI and Ctrl-] powers the guest off cleanly.

## Status

Working today: locked VM, full-screen session, root image with Claude Code, broker with credential injection and audit, graceful shutdown and resize.

Planned: agent picker with Codex and opencode, approval prompts for risky actions, results returned as a branch, snapshot warm starts, a Linux backend on Firecracker.

## License

Apache 2.0
