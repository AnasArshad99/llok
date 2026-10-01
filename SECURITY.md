# Security

llok exists to contain coding agents, so its own security matters more than
most tools'. This page says what it defends against, what it does not, and
how to report a problem.

## Reporting a vulnerability

Please do not open a public issue for a security problem. Use GitHub's
private vulnerability reporting on this repository ("Security" tab, "Report
a vulnerability"). Include steps to reproduce and the llok version. You will
get an acknowledgement within a few days, and a fix or a clear answer before
anything is published.

## Threat model

The thing llok protects you from is the agent: a coding model that runs
shell commands, reads untrusted files and web pages, and can be steered by
them. The Mac, the user, and the provider API are trusted. The repository is
trusted as data but not as policy.

### What llok defends against

- **The agent touching your Mac.** It runs in a microVM with no network
  device and exactly two shared folders: the repo, read-write, and a
  read-only folder holding llok's CA certificate. Nothing else on the host
  is reachable.
- **The agent reaching the network.** The only way out is the broker on the
  host, over vsock. It allows the provider's API and the hosts listed in
  `llok.yaml`, refuses everything else, and logs every decision. Allowed
  names that resolve to loopback, private or link-local addresses are
  refused, so an allowlist entry cannot become a door to host-local services.
- **The agent stealing your credentials.** The VM holds a placeholder. The
  real token lives in the macOS Keychain and is injected by the broker on
  the way out. TLS to the provider's hosts is terminated by the broker with
  a local CA only the guest trusts, so injection works for every call.
- **The agent persisting.** The root disk is read-only with a memory
  overlay. Every session starts from the same image; nothing the agent does
  outside the repo survives power-off.
- **The agent escalating inside the guest.** The session runs as an
  unprivileged user. llok's own in-guest agent, which relays the session's
  exit status and handles resize and shutdown, runs as root; the first
  connection it makes to the host is the only one accepted, so a process
  inside the guest cannot impersonate it.
- **The agent programming your terminal.** Outside the full-screen UI, guest
  output is reduced to text and colours. Titles, clipboard writes, cursor
  tricks and terminal queries are dropped. Inside the UI the guest is
  rendered through an emulator and never writes to the host terminal.
- **A repository that rewrites its own policy.** `llok.yaml` travels with
  the repo, so a cloned repo's allowlist and rule changes must be approved
  once per repo before they take effect, and again after any change.
- **Repo files that execute on your Mac later.** The supervisor watches the
  repo from the host and pauses the VM when the agent writes git hooks,
  `.git/config`, or `.envrc`, and notifies on CI, editor and git attribute
  changes.

### What llok does not defend against

- **Bad changes to your code.** The agent can write anything into the repo.
  Review the diff. Working on a branch and returning results for review is
  planned, not built.
- **Content the agent sends to the provider.** The broker forwards the
  agent's API requests; it does not inspect or filter their contents. If
  the agent reads a secret from your repo and includes it in a prompt, that
  reaches the provider.
- **Hosts you allow.** An allowlisted host is reachable on every port and
  path, with nothing injected and nothing inspected.
- **The provider.** Everything the agent sends to the API is visible to the
  provider, as it would be without llok.
- **A compromised Mac.** Another process running as your user can read the
  Keychain entry, the CA key under `~/.llok/ca`, and the guest image. llok
  assumes the host is yours.
- **Hypervisor escapes.** llok relies on Apple's Virtualization framework
  for isolation and adds nothing to it.
- **Denial of service.** An agent can fill the repo, spin the CPU, or flood
  the audit log. Pause and kill are your tools for that.

### Supply chain

- The guest image is built from Alpine packages. The root filesystem
  tarball is verified against the checksum Alpine publishes; packages
  installed inside the builder are verified by `apk`. The kernel package is
  downloaded and unpacked directly and is not signature-checked yet.
- Claude Code is installed by Anthropic's installer script fetched over
  HTTPS. The version can be pinned with `LLOK_CLAUDE_VERSION` at build time,
  and the installed binary's SHA-256 is recorded in the image.
- Go dependencies are pinned in `go.sum`.

## Reporting what you see

`llok audit` summarises what the broker allowed and refused.
`~/.llok/audit/broker.jsonl` and `~/.llok/sessions/<project>/` hold the
full records. They contain hosts, paths and file names, never request
bodies or credentials.
