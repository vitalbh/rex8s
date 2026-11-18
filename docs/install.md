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
upgrade, say): it puts `rex8s` and `rex8s-guard` in `~/.config/rex/rex8s/bin`
with the `k*` commands and the `kubectl`/`helm`/`flux` guard, writes
`~/.config/rex/rex8s.lua`, adds `require("rex8s")` to Rex's `init.lua` and
reloads Rex's config.

- **`rex8s doctor`** checks the installation and what rex8s uses: the commands,
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
  click: bring the port over with Rex, e.g. for a `kpf` on port 54321,
  `rex -S SERVER forward new -L 54321:127.0.0.1:54321` on the Mac.
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
