# rex8s

A Kubernetes toolkit for the [Rex](https://www.superlogical.com/rex) terminal:
isolated cluster panes, multi-cluster views, Flux (GitOps), a production guard
and Claude.

![kview in Rex: a failing pod on every cluster; why (Claude), its logs and a shell open in splits beside it — production asks first](docs/kview-rex.gif)

## Quick start

With [Rex](https://www.superlogical.com/rex) and `kubectl` set up:

```sh
brew install vitalbh/tap/rex8s && rex8s setup    # setup installs it into Rex
rex8s doctor                                     # checks everything
```

Then **⇧⌘J** in Rex: pick a cluster, get a shell on it. Don't remember a
command? **`khelp`** lists every one in a picker; enter runs it.

Without Homebrew, from source, or on a Linux server: [Install](docs/install.md).

### Without Rex

The `k*` commands work in any terminal (bash, zsh, iTerm, a Linux server):
`rex8s setup` installs just them when Rex isn't there, and they use the
machine's current context. `kview`, `kpods`, `kfind`, `kfail`, `kstatus`,
`kwarn`, `klogs`, `kpf`, `kwhy`/`kask`, the Flux commands and the production
guard (with `kunlock`) all work; logs and shells open in the terminal itself
instead of a split.

What needs Rex: one isolated cluster per pane (its own context, the
`PROD use/api` label, the red tint), the shortcuts and palette actions,
`kmulti`/`kall`, `kincident`, splits beside `kview`, and tab status.
Details: [In any shell](docs/configuration.md#in-any-shell).

## What it does

Every pane is **one cluster**: its own context and namespace, a label that
says where you are (`PROD use/api`), production tinted red, and a guard that
asks before `kubectl`/`helm`/`flux` change production — also when an agent or
a script runs them: then the Rex palette asks (on the Mac or the iPhone),
allow once or deny. The guard is a safety net against slips, not a security
boundary: a `kubectl` run by its full path, k9s, Lens or other client-go
programs don't go through it. For a real barrier use RBAC (a read-only
context for daily work, a separate one with write access) or admission
policies on the server. Working on a cluster for a while? `kunlock use 30m`
lets you (and your coding agents) through without asking, until it runs out.
Don't want it? `guard: false` in the
[config](docs/configuration.md#configyaml) turns it off.

| You want to… | Commands |
|---|---|
| **browse like k9s** | `kview` tabs for pods, workloads, services, jobs (grouped by CronJob), nodes, warnings, and `:` for any kind — live from the API server, what just changed lit up, a key per action (`-A`: every cluster) |
| **see what's broken** | `kstatus` one screen of health · `kfail` failing pods with their last logs · `kpods` every cluster's pods · `kwarn -A` warnings of every cluster as they happen |
| **read logs** | `klogs` a workload's pods in one stream (`-A`: mix pods and workloads of any cluster) · `kgrep` search them · cluster URLs in the logs are clickable |
| **ask why** | `kwhy` Claude explains an unhealthy pod · `kask` a Claude conversation about the cluster or a pod · `kincident` a session laid out for an incident |
| **reach a service** | `kpf` port-forward that connects only while in use, with the right client command for databases · `kexec` / `knode` / `knet` shells in a pod, a node, a toolbox pod |
| **ship with Flux** | `fdiff` what your branch would change · `fwatch` follow a push until it rolled out · `kgit` the file that defines a workload · `kflux` reconcile, suspend |
| **work across clusters** | `kfind` an object by name on every cluster · `kmulti` clusters side by side, `kall` types in all · `kwhat` what changed · `kver` which version runs where · `kdiff` · `krun` · `kroll` · `kcron` · `kjunk` |
| **keep an eye on things** | Raycast menu bars: Flux status, failing pods, alerts, default context |

Every command, with its options: **[docs/commands.md](docs/commands.md)**.

**⇧⌘J** picks a cluster for an isolated shell, **⇧⌘M** puts several side by side and `kall` types in all of them; **⇧⌘V** opens `kview` (above) on every cluster, and **⇧⌘I** jumps between a cluster shell and its `kview`

![rex8s in Rex: Cmd+Shift+J picks a cluster, kfail, Cmd+Shift+M opens clusters side by side, kall runs the same command in all of them](docs/tour.gif)

In Rex's command palette (⇧⌘P, also on the iPhone): **Cluster Health…**, **Pods…**, **Flux Resources…**, port-forwards, unlocking production — each question asked right there

![the Rex palette: Cluster Health, then a cluster's pods, failing first, then its logs](docs/palette-health.gif)

<details>
<summary><b>See it</b> — recordings of each command</summary>

| Production tab (⇧⌘K): browser · shell · warnings, tinted red | `kflux` on a staging tab |
|---|---|
| ![production layout](docs/rex-prod.png) | ![kflux](docs/rex-kflux.png) |

**`kmulti`** (⇧⌘M): clusters side by side — `kall` types the same command in all of them

![kmulti: three clusters side by side, kall running the same commands in each](docs/kmulti.gif)

| `kview`: a crashing pod lights up, describe, a workload's pods, a CronJob's runs, `:certificates`, warnings, nodes | |
|---|---|
| ![kview](docs/kview.gif) | |
| **`kpods`**: the pods of every cluster, failing first | **`kpf`**: port-forward in the background, the client command for databases |
| ![kpods](docs/kpods.gif) | ![kpf](docs/kpf.gif) |
| **`fwatch`**: follow a commit until it's applied and rolled out | **`kwhy`**: ask Claude why a pod is unhealthy |
| ![fwatch](docs/fwatch.gif) | ![kwhy](docs/kwhy.gif) |
| **`kflux`**: Flux resources, failing first | **`kwarn -A`**: the Warning events of every cluster, live |
| ![kflux](docs/kflux.gif) | ![kwarn](docs/kwarn.gif) |
| **`kfail`**: failing pods with the crashed container's last logs | **`kjobs`**: Jobs and CronJobs with logs and recent runs |
| ![kfail](docs/kfail.gif) | ![kjobs](docs/kjobs.gif) |
| **`kstatus`**: the cluster's health on one screen | **`krun`**: one command, every pod, several clusters |
| ![kstatus](docs/kstatus.gif) | ![krun](docs/krun.gif) |
| **`kver`**: which version runs on which cluster | **`kdiff`**: the same workload on two clusters |
| ![kver](docs/kver.gif) | ![kdiff](docs/kdiff.gif) |
| **`kroll`**: restart workloads and follow the rollouts | **`knode`**: a shell on a node |
| ![kroll](docs/kroll.gif) | ![knode](docs/knode.gif) |
| **`kask`**: talk to Claude about the cluster or a pod | **`kwhat`**: what changed, on every cluster |
| ![kask](docs/kask.gif) | ![kwhat](docs/kwhat.gif) |
| **`klogs`**: a workload's pods in one stream | **`knet`**: network tools inside the namespace |
| ![klogs](docs/klogs.gif) | ![knet](docs/knet.gif) |
| **`kgrep`**: search a workload's logs, matches highlighted | **`kgit`**: from a workload to the file that defines it |
| ![kgrep](docs/kgrep.gif) | ![kgit](docs/kgit.gif) |
| **`kdisk`**: how full the volumes are | **`kreq`**: requests and limits vs real usage |
| ![kdisk](docs/kdisk.gif) | ![kreq](docs/kreq.gif) |
| **`kcron`**: CronJob runs coming up on every cluster | **`kjunk`**: leftovers to clean up |
| ![kcron](docs/kcron.gif) | ![kjunk](docs/kjunk.gif) |
| **`khelp`**: every command, enter runs it | **`ktint`**: pick the production color |
| ![khelp](docs/khelp.gif) | ![ktint](docs/ktint.gif) |
| **Cluster picker** (⇧⌘J): a live preview of each cluster | |
| ![cluster picker](docs/picker.gif) | |

All recordings use the built-in demo mode (fake `example.com` clusters) — see
[Development](docs/development.md#demo-mode).

</details>

## Documentation

- **[Commands](docs/commands.md)** — every command and key binding
- **[Install](docs/install.md)** — Homebrew, release, source, Linux servers, coding agents
- **[Configuration](docs/configuration.md)** — `config.yaml`, the `k*` commands in any shell, integrations
- **[Raycast](docs/raycast.md)** — the extension and its menu bars
- **[Development](docs/development.md)** — how it works, demo mode, known limitations

## License

[MIT](LICENSE)
