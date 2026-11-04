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
  <img src="https://img.shields.io/badge/status-early-orange.svg" alt="status">
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#why">Why</a> ·
  <a href="#features">Features</a> ·
  <a href="#usage">Usage</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#security-model">Security model</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#roadmap">Roadmap</a>
</p>

---

## Quick start

```bash
git clone https://github.com/AnasArshad99/llok && cd llok
make install          # builds, signs, installs into ~/.local/bin
llok guest build      # builds the VM image into ~/.llok (once, a few minutes)

llok auth anthropic   # sign in once (Claude login, or --api-key)
cd your-repo
llok                  # first run asks which agents, writes llok.yaml, boots
```

Inside the VM, type `claude` (or `codex`, `gemini`). Your folder is at `/<repo name>`.

> Requires an Apple silicon Mac on macOS 26 or newer, Go 1.26 and the Xcode command line tools. Early software: expect rough edges, and read [Security model](#security-model) before trusting it with anything that matters.

## Why

Coding agents run shell commands, install packages, and do whatever a web page tells them to. Today they do it on your machine, as you, with your keys.

llok gives each agent session its own throwaway virtual machine with **no network card**, your repository as the **only** shared folder, and a broker on your Mac as the **only** way out. The broker lets the agent reach its provider's API with your credential injected on the way, refuses everything else, and logs every decision. The agent never holds a real key and never sees your Mac.

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

## Features

- **Locked VM per session.** Apple's Virtualization framework, Alpine guest, read-only root with a memory overlay, no network device, unprivileged user. Boots in about 0.2 seconds.
- **Credentials never enter the VM.** The agent gets a placeholder; the broker on your Mac swaps in the real key from the Keychain on the way out. Sign in once with `llok auth`.
- **One audited exit.** Provider hosts are allowed with credential injection; anything else needs a line in `llok.yaml`. Every decision lands in `~/.llok/audit/broker.jsonl`.
- **Secrets stay invisible.** `mount.exclude` hides `.env`, `*.pem`, `*.key` and anything you name: empty and read-only inside the VM, untouched on your Mac.
- **Read-only sessions.** `llok --ro` shares the repo read-only; the agent still builds and edits, into a scratch layer that vanishes at power-off. For "explain this codebase" and review runs where nothing should change.
- **A supervisor outside the VM.** Rules over what the host can see (repo file changes, broker decisions) pause or stop the VM: git hooks written, mass deletes, CI config changes.
- **Sessions that outlive the terminal.** Detach, close the tab, come back from another terminal; the agent keeps working. Switch between sessions with one key.
- **A live event feed.** Every session streams JSON events on a local socket and accepts pause, resume and kill from outside, so other tools can watch and act.
- **Three agents, one policy file.** Claude Code, Codex and Gemini CLI ship in the image; any agent can be described inline in `llok.yaml`. Image inputs are pinned by hash.

## Usage

`llok` takes over the terminal: the VM console on the left, a live panel on the right.

```
 llok@my-app /my-app $ claude                           │ ● running
 ╭─ Claude Code ─────────────────────────╮              │ session   my-app
 │ › fix the failing test in auth.go     │              │ repo      ~/code/my-app
 ╰───────────────────────────────────────╯              │ network   broker · anthropic
                                                        │ activity
                                                        │  → POST /v1/messages 200 1.9s
                                                        │  ✗ CONNECT www.google.com denied
```

### Sessions

The VM runs in a background llok process; your terminal is attached to it. Press **Ctrl-]** for the session menu:

```
d detach   s switch   n new   K kill   C-] power off   ] send Ctrl-]
```

Detach, or close the tab, and the VM keeps running with whatever the agent was doing. `llok` in the same repo attaches again. Sessions are named after the project (`my-app`, `my-app-2`, …) or with `llok -s <name>`; a session idle for two hours powers itself off (`session.idle`).

The mouse is llok's while a session is open: the wheel scrolls the VM's history, click-drag selects console text and copies it to your clipboard.

### Commands

| Command | What it does |
|---|---|
| `llok` | Start a session, or attach to the one running for this repo. `exit` powers off |
| `llok --agent codex` | Same, set up for a different default agent than `llok.yaml` says |
| `llok -s review` | Start or attach to a session by name; several can run on one repo |
| `llok --ro` | Read-only session: the agent's writes stay inside the VM and are discarded at power-off |
| `llok sessions` / `attach` / `kill` | List, join, or stop sessions (`kill --all` stops every one) |
| `llok up -c 'make test'` | Run one command in the VM and return its exit code |
| `llok up --plain` | Raw console in the current terminal, no panel |
| `llok auth <provider>` | Store a credential: `anthropic` (Claude login or key), `openai` (ChatGPT login or key), `google` (key) |
| `llok agents` | List agents and which providers are signed in |
| `llok events` | Follow a session's feed (`--json` for programs, `--send kill` to stop it) |
| `llok audit` | Summarise what the broker allowed and refused |
| `llok rules` / `trust` | Show the active supervisor rules; approve a repo's policy |
| `llok doctor` | Check hypervisor support, code signing and the guest image |
| `llok guest build` | Rebuild the VM image (for a newer agent version) |

## Configuration

`llok init` writes a `llok.yaml` next to your code. It is the only file you edit.

```yaml
agent: [claude-code, codex]   # one name or a list; the first is the default

vm:
  cpus: 2
  memory_mb: 1024

mount:
  repo: /my-app               # where your folder appears inside the VM (default: its name)
  readonly: false             # true: writes go to a scratch layer discarded at power-off
  exclude:                    # repo paths the agent must not see (shell globs)
    - .env                    # defaults: .env, .env.*, *.pem, *.key
    - secrets

network:
  mode: broker                # or "none": no way out at all
  allow:                      # extra hosts the agent may reach; everything else is refused
    - registry.npmjs.org
    - "*.github.com"

session:
  idle: 2h                    # a detached session this quiet powers itself off (off disables)

rules:                        # supervisor rules: override a default by name, or add your own
  - name: mass-delete
    action: off
  - name: lockfile
    on: file
    path: package-lock.json
    action: notify
    message: lockfile changed

agents:                       # an agent llok does not know, described inline
  my-tool:
    binary: mytool
    install: pip install mytool   # run when the image is built
    providers: [openai]           # which API it talks to; keys via llok auth
```

Because `llok.yaml` travels with the repo, a policy that widens access (extra hosts, rule changes, fewer excluded paths) must be approved once per repo: `llok` asks, or run `llok trust`. Any later change to those parts asks again.

### Supervisor rules

A small rules engine runs on your Mac over what the Mac can see: which repo files changed, and which hosts the broker was asked for. It never reads request contents and never records what runs inside the guest. Defaults:

| Rule | When | Action |
|---|---|---|
| `git-hooks` | a file under `.git/hooks/` is written | pause and ask |
| `git-config`, `direnv`, `pre-commit`, `policy-file` | `.git/config`, `.envrc`, pre-commit config or `llok.yaml` changes | pause and ask |
| `mass-delete` | 50 or more files deleted within 10 seconds | pause and ask |
| `event-storm` | the watcher is overwhelmed and events were dropped | pause and ask |
| `ci-config`, `git-attributes`, `git-info`, `editor-tasks`, `idea-config` | CI, git attribute or editor config changes | notify |

A pause freezes the VM in place and the panel asks `[r] resume  [K] kill`. With nobody attached the VM stays paused until someone answers; in command mode it stops. Every event and outcome goes to a per-session ledger under `~/.llok/sessions/<project>/`.

## Security model

| | |
|---|---|
| Read and write your repo | yes, and only your repo, minus `mount.exclude`. With `--ro`, writes never leave the VM |
| Read `.env` or anything under `mount.exclude` | no. Those paths are empty and read-only inside the VM |
| Reach the provider API | yes, through the broker, with your credential injected there |
| Reach any other host | only if listed under `network.allow` |
| See your credentials | no. The VM holds a placeholder; the broker swaps it on the way out |
| Change its own system | only until power-off. The root disk is read-only with a memory overlay |
| Run as root inside | no. The session runs as an unprivileged user |
| Touch your Mac | no. No shared folders beyond the repo, no host network, no host devices |
| Program your terminal | no. Output is rendered through an emulator or reduced to text and colours |

**Treat a repo an agent has touched as untrusted until you have reviewed the diff.** The VM stops the agent reaching your Mac; it cannot stop you running a `Makefile`, `package.json` script or `setup.py` the agent edited. llok pauses on files that run *automatically* (git hooks, `.git/config`, `.envrc`, pre-commit config); files that run when *you* act are yours to read first.

The full threat model, what llok does not protect against, and how to report a vulnerability are in [SECURITY.md](SECURITY.md).

## Event feed

Every session streams what it sees on `~/.llok/run/<session>/events.sock`, one JSON object per line: session start and end, each repo path changed, each host allowed or refused, each rule that fired, each pause, resume or kill. Late clients get the recent history first. Metadata only, never file contents or request bodies.

```bash
llok events --json
nc -U ~/.llok/run/my-app/events.sock | jq 'select(.type=="rule")'
```

```json
{"seq":7,"type":"file","project":"my-app","path":".git/hooks/pre-commit","op":"write"}
{"seq":8,"type":"rule","project":"my-app","rule":"git-hooks","action":"pause","path":".git/hooks/pre-commit"}
{"seq":9,"type":"vm","project":"my-app","state":"paused","source":"rule","reason":"git-hooks"}
```

The same socket takes `{"op":"pause"}`, `{"op":"resume"}` and `{"op":"kill"}`, so a program watching the feed can act on it, including a central switch across every llok on the machine.

## How it works

| Part | How |
|---|---|
| **Hypervisor** | Apple's Virtualization framework, driven from Go. Boot to a ready shell in about 0.2 s |
| **Guest** | Alpine's `linux-virt` kernel, a 6 MB initramfs with llok's init, an ext4 root disk with bash, git, Node, ripgrep and every built-in agent. Mounted read-only with a tmpfs overlay; the session runs as an unprivileged user |
| **Read-only repo** | The share is read-only at the hypervisor and the guest overlays a tmpfs on it, so the agent's edits exist only in VM memory. The share reports files as root's, so the guest hands the merged tree to the session user (metadata only, about 1.5 s per 10k files) |
| **Image build** | `llok guest build` boots a one-off builder VM with NAT to install packages: the only VM that ever gets a network device, and it never sees a repo. Every input is pinned in `internal/guest/versions.yaml` with its SHA-256; the build writes a manifest and `llok` refuses an image that does not match it |
| **Broker** | A guest-side forwarder sends `127.0.0.1:3128` over vsock to llok. Provider hosts are terminated with a local CA the guest trusts and the real credential is injected in the header each provider expects; other allowed hosts get a plain tunnel |
| **Sessions** | The VM runs in a background llok process that keeps console history, the supervisor and the feed, served on `~/.llok/run/<session>/console.sock`. The terminal is a thin client: attaching replays recent output into a fresh emulator and resizes the guest so full-screen programs redraw |
| **Supervisor** | A file watcher on the repo (FSEvents) and the broker's decisions feed an indexed rules engine that pauses or stops the VM through the Virtualization framework |

## Roadmap

- Session limits: wall-clock, request count, bytes out
- Results returned as a branch for review
- Headless `llok run --json` for CI and scripts
- Signed installer and Homebrew tap
- Snapshot warm starts; a Linux backend on Firecracker
- Organisation policy and central audit across machines

## Contributing

Issues and pull requests are welcome. `make test` runs the unit tests; `make install` builds and installs a local binary. Changes to what the VM can reach or do belong in [SECURITY.md](SECURITY.md) as well as in code.

## License

[Apache 2.0](LICENSE)
