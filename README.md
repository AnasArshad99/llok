<p align="center">
  <img src="docs/logo.svg" alt="llok" width="220">
</p>

<p align="center">
  <b>Run coding agents inside locked microVMs.</b><br>
  Claude Code, Codex, Gemini CLI. Your repo goes in. One audited channel comes out. Nothing else.
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
llok auth anthropic # Claude login (or --api-key); llok auth openai = ChatGPT login (or --api-key)
cd your-repo
llok                # first run asks which agents, writes llok.yaml, then boots
```

Inside the VM, run any agent whose provider you have signed in to:
`claude`, `codex` or `gemini`. Every session carries all your stored
providers, so you can use several agents in one VM; `agent:` in
`llok.yaml` only sets the default, and `llok --agent codex` overrides it
for one run. `llok agents` lists agents and sign-in state. `llok init`
writes the policy file without booting; `llok init --force` regenerates it.

`llok` (or `llok up`) shows the VM console on the left and a live panel on the right: state, repo mapping, VM size, network policy, uptime, and every request the broker allowed or refused.

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
| `llok` | Interactive session (same as `llok up`). `exit` powers off, Ctrl-] twice forces it |
| `llok --agent codex` | Same, set up for a different agent than `llok.yaml` says |
| `llok up -c 'make test'` | Run one command in the VM, return its exit code |
| `llok up --plain` | Raw console, no panel |
| `llok agents` | List agents and which providers are signed in |
| `llok auth <provider>` | Store a credential: `anthropic` (Claude login or key), `openai` (ChatGPT login or key), `google` (key). Use whichever you have |
| `llok doctor` | Check hypervisor support, code signing, guest image |
| `llok guest build` | Rebuild the VM image (for a newer agent version) |

## Policy

`llok init` writes a `llok.yaml` next to your code. It is the only file you
ever edit.

```yaml
agent: [claude-code, codex] # one name or a list; the first is the default
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
| Run as root inside | no. The session runs as an unprivileged user; llok's own guest agent keeps root |
| Touch your Mac | no. No shared folders beyond the repo, no host network, no host devices |
| Program your terminal | no. Output outside the UI is reduced to text and colours |

**Treat a repo an agent has touched as untrusted until you have reviewed the
diff.** The VM stops the agent reaching your Mac; it cannot stop you running
a `Makefile`, `package.json` script or `setup.py` the agent edited. llok
pauses on files that run *automatically* (git hooks, `.git/config`,
`.envrc`, pre-commit config); files that run when *you* act are yours to
read first. A branch-and-review step that shows the diff before anything
reaches your working tree is planned.

Every request is appended to `~/.llok/audit/broker.jsonl`. The threat model,
what llok does not protect against, and how to report a vulnerability are in
[SECURITY.md](SECURITY.md).

## Supervisor

A small rules engine runs on your Mac, outside the VM, over what the Mac can
see for itself: which files in the repo the agent changed, and which hosts
the broker was asked for. It never reads request contents and never records
what runs inside the guest. Three rules ship by default:

| Rule | When | Action |
|---|---|---|
| `git-hooks` | a file under `.git/hooks/` is written | pause the VM and ask |
| `ci-config` | a file under `.github/workflows/` changes | notify |
| `mass-delete` | 50 or more files deleted within 10 seconds | pause the VM and ask |

A pause freezes the VM in place. The panel asks `[r] resume  [K] kill`, and
nothing you type reaches the guest until you answer. In command mode, with
no one to ask, a pause stops the VM instead.

Rules live in `llok.yaml`. Override a default by name, disable one with
`action: off`, or add your own:

```yaml
rules:
  - name: mass-delete
    action: off
  - name: lockfile
    on: file
    path: package-lock.json
    action: notify
    message: lockfile changed
```

An agent llok does not know can be described inline, still in the same file:

```yaml
agent: my-tool
agents:
  my-tool:
    binary: mytool
    install: pip install mytool     # run when the image is built
    providers: [openai]             # which API it talks to; keys via llok auth
```

Because `llok.yaml` travels with the repo, a policy that adds allowed hosts
or changes rules must be approved once per repo (`llok up` asks, or run
`llok trust`). Any later change to those parts asks again.

Matching is indexed, so the cost per event does not grow with the number of
rules. Every event and every rule outcome is written to a per-session ledger
under `~/.llok/sessions/<project>/`.

## How it works

- **Hypervisor.** Apple's Virtualization framework, driven from Go. Boot to a ready shell takes about 0.2 seconds.
- **Guest.** Alpine's `linux-virt` kernel, a 6 MB initramfs with llok's init, and an ext4 root disk holding bash, git, Node, ripgrep and every built-in agent. Sessions mount the disk read-only with a tmpfs overlay and run as an unprivileged user.
- **Image build.** `llok guest build` boots a one-off builder VM with NAT to install packages. It is the only VM that ever gets a network device, and it never sees a repo. Every input is pinned in `internal/guest/versions.yaml` (Alpine release and kernel package with their SHA-256, each agent's version), so a build is reproducible, and the build writes a manifest with the hash of every artifact. `llok up` refuses an image that does not match its manifest; `llok doctor` hashes everything and lists what is inside.
- **Broker.** A small agent inside the guest forwards `127.0.0.1:3128` over vsock to llok on the host. Each provider the chosen agent uses is a route: its hosts are terminated with a local CA the guest trusts and the real credential is injected in the header that provider expects. Other allowed hosts get a plain tunnel with nothing injected.
- **Control channel.** A second vsock port carries resize and shutdown, so window changes reach the agent's UI and Ctrl-] powers the guest off cleanly.
- **Supervisor.** A file watcher on the repo (macOS FSEvents) and the broker's decisions feed an indexed rules engine on the host, which can pause or stop the VM through the Virtualization framework.

## Status

Working today: locked VM, full-screen session, root image with Claude Code, broker with credential injection and audit, graceful shutdown and resize, supervisor rules with pause prompts and a session ledger.

Planned: agent picker with Codex and opencode, approval prompts for risky actions, results returned as a branch, snapshot warm starts, a Linux backend on Firecracker.

## License

Apache 2.0
