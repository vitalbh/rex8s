# Install

You need [Rex](https://www.superlogical.com/rex) and `kubectl`. Optional:
`k9s`, `helm`, `flux` (for `fdiff`), `claude` (for `kwhy` and `kask`, or
`codex` for `kask`), Node.js + Raycast (for the [Raycast extension](raycast.md)).

- [Homebrew](#homebrew) · [Release](#from-a-release) · [Source](#from-source) ·
  [What setup does](#what-setup-does) · [Linux server](#on-a-linux-server) ·
  [Coding agents](#coding-agents)

## Homebrew

```sh
brew install vitalbh/tap/rex8s && rex8s setup
rex8s doctor
```

Upgrades with `brew upgrade rex8s`; run `rex8s setup` again when `rex8s doctor`
says the Lua file is outdated.

## From a release

The [latest release](https://github.com/vitalbh/rex8s/releases/latest) has
`rex8s_darwin_arm64`, `rex8s_darwin_amd64` (Intel Macs), `rex8s_linux_amd64`
and `rex8s_linux_arm64`:

```sh
curl -sL https://github.com/vitalbh/rex8s/releases/latest/download/rex8s_darwin_arm64.tar.gz | tar xz
./rex8s_darwin_arm64/rex8s setup
```

The binaries aren't signed by Apple. Downloaded with `curl` as above they just
run; downloaded in a browser, macOS quarantines them and refuses to open them —
clear that with `xattr -d com.apple.quarantine rex8s_darwin_arm64/rex8s*`.

## From source

Go 1.26+:

```sh
make install        # builds, then rex8s setup (make check runs the tests)
```

## What setup does

`rex8s setup` does everything, and can be run again any time (after an
upgrade, or a config change): it puts `rex8s` and `rex8s-guard` in
`~/.config/rex/rex8s/bin` (links to Homebrew's `opt` path when installed with
brew, so upgrades apply), links the `k*` commands to `rex8s` (`kview ->
rex8s`) and `kubectl`/`helm`/`flux` to `rex8s-guard`, writes
`~/.config/rex/rex8s.lua`, adds `require("rex8s")` to Rex's `init.lua` and
reloads Rex's config. Without Rex installed it stops after the commands (it
doesn't create a Rex config); run it again once Rex is there.

- **The production guard question.** The first time, at a terminal, setup
  asks *Install the production guard? kubectl/helm/flux ask before changing
  production clusters (recommended)* — Yes by default — and saves the answer
  as `guard:` in `config.yaml`, so it never asks again. Without a terminal
  (`brew` post-install, a script, `curl` on a server) it installs the guard
  and asks nothing.
- **`guard: false`** turns the guard off entirely: setup leaves out (and
  removes) the `kubectl`/`helm`/`flux` links, and `kview`, `kroll`, `krun`…
  change production without asking.
- **`wrappers: false`** installs no `k*` commands (`rex8s kview`, `rex8s logs`
  … still work); **`skip_wrappers: [kns, kctx]`** leaves out just those
  names, when kubectx or an alias of yours already uses them.

Change any of them (`rex8s config set guard false`, `rex8s config add
skip_wrappers kns`) and run `rex8s setup` again to apply it. The `k*`
commands were small `sh` scripts before: setup replaces them with the links.

- **`rex8s doctor`** checks the installation and what rex8s uses: the commands
  (against `guard`, `wrappers` and `skip_wrappers`; it says `guard: off` when
  it is),
  the Lua file (and whether it matches this version), Rex, kubectl, flux, k9s,
  claude, the config, the kubeconfig, `gitops_repos`, favorite forwards.
- **`rex8s uninstall`** undoes `setup`; your `config.yaml` stays.

`rex8s` is on `PATH` inside rex8s panes; elsewhere it is
`~/.config/rex/rex8s/bin/rex8s` (or see
[In any shell](configuration.md#in-any-shell)).

## On a Linux server

Rex can [run as a server on Linux](https://www.superlogical.com/rex/docs/linux/installation.md)
that the Mac app connects to. rex8s installs there the same way, and its panes
then use that machine's `kubectl` and kubeconfig — handy when the clusters are
only reachable from the server:

```sh
d=$(mktemp -d) && cd $d
curl -sL https://github.com/vitalbh/rex8s/releases/latest/download/rex8s_linux_amd64.tar.gz | tar xz
./rex8s_linux_amd64/rex8s setup      # arm64: rex8s_linux_arm64
echo 'export PATH="$HOME/.config/rex/rex8s/bin:$PATH"' >> ~/.bashrc   # k* in any shell
```

In a Rex session on the server, the browser still opens on the Mac:

- **Links in `klogs`/`kgrep`** and the URLs `kpf` prints point to `localhost`
  on the server; when you click one, Rex forwards that port to the Mac by
  itself.
- **Non-HTTP forwards** (`psql`, `redis-cli` on the Mac) have no link to
  click: bring the port over with Rex. `kpf` (and `kpf NAME` for a database
  favorite) prints the command when it runs in a Rex session on a server,
  e.g. for port 54321, run on the Mac
  `rex -S SERVER forward new -L 54321:127.0.0.1:54321` (it forwards until
  ctrl+c), or use **Forward Port…** in Rex's palette.

  rex8s can't set this forward up by itself: a Rex forward listens on the
  machine of the client that carries it, so `rex forward new -L` run on the
  server would listen on the server, not on the Mac; the forward's tunnel
  block belongs to the client that made it; and the app's Forward Port… is a
  dialog that takes no arguments. Only something running on the Mac can make
  the Mac listen.
- **What opens or copies on the machine itself** doesn't work there: `kpf -l`'s
  open/copy, `kgit` opening a file or GitHub, `kauth` opening the login page
  (it prints the URL), macOS notifications and the Raycast extension.

## Coding agents

```sh
rex8s agents install            # every agent you have: Claude Code, Codex (~/.agents), Cursor
rex8s agents install codex      # or only some: claude, codex, cursor
```

writes a small skill that tells the agent to run `rex8s agent-guide` — the
guide ships in the binary, so it follows rex8s upgrades. With it, an agent
answers "what changed?", "what's broken?", "which version runs where?" with
`kwhat`, `rex8s list failing`, `kver`, `kdisk`… across every cluster, and
knows the production rules: a `kubectl`/`helm`/`flux` command that changes a
production cluster (or one the guard doesn't know), or a rex8s command that
does (`kroll`, `krun`, `kjunk`, `kexec`…), run by an agent or without a
terminal (a script), is refused until you agree and it is
re-run with `REX8S_FORCE=1`. Every decision is logged in
`~/.cache/rex8s/audit.log`.
`rex8s agents status` / `remove` take the same agent names.

`kask` opens Claude Code or Codex: `rex8s setup` asks once when you have both
(with one, it uses that one), and `rex8s config set ask_agent claude|codex`
changes it. Codex asks before each command it runs.
