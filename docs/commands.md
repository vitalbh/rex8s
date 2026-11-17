# Commands

In Rex, **`khelp`** lists all of these in a picker (enter runs one). In
pickers: arrows or `ctrl+p`/`ctrl+n` move, typing filters (fuzzy), `space`
marks several where that makes sense, `enter` selects, `esc` quits.

Most commands work on this pane's cluster; **`-A`** makes them work on every
cluster. Splits you only watch (logs, port-forwards, `kwhy`'s answer) open
without taking the focus, so you keep typing in your shell; the ones you type
in (`kexec`, `knode`, `knet`, `kask`) take it.

- [Keys](#keys) · [Panes](#panes) · [What's broken](#whats-broken) ·
  [Logs](#logs) · [Ask Claude](#ask-claude) · [Reach a service or a pod](#reach-a-service-or-a-pod) ·
  [Flux / GitOps](#flux--gitops) · [Across clusters](#across-clusters)

## Keys

| | |
|---|---|
| `⇧⌘J` | pick cluster + namespace → new tab with an isolated shell |
| `⇧⌘K` | same → new tab with k9s · shell · warnings |
| `⇧⌘M` | pick several clusters → a tab with one pane each, side by side |
| `⇧⌘P` (palette) | **Kubernetes: Flux Monitor**, **Clusters Side by Side…**, **Restore Cluster Tabs** |

Key bindings are at the bottom of `rex/rex8s.lua`.

## Panes

- **Cluster picker** with a live preview of each cluster: namespace count and
  Flux status (ready, failing, suspended, with the error message).
- **Isolated panes**: each pane has its own context and namespace. Switching in
  one pane doesn't affect the others or `~/.kube/config`.
- **Labels** always show where you are: `PROD use/api`, `k8s staging/default`.
- **Every namespace**: pick "* all namespaces" (or `kns all`) and the pane
  shows `PROD use/*`. `kubectl get`/`describe`/`top pods`/`events`, `helm list`
  and `flux get` add `-A` by themselves; a command on one object (`logs`,
  `exec`, `delete`…) asks for `-n` instead of guessing. The `k*` pickers list
  every namespace, and k9s opens in all of them.
- **Production guard**: `kubectl`/`helm`/`flux` commands that change state, and
  `kexec` into a pod, ask you to type the cluster name first. With no terminal
  to ask (an agent, a script) they are refused until re-run with
  `REX8S_FORCE=1`. Production is detected by server URL, so every alias of the
  same cluster is covered.
- **Expired logins** show up as "login expired" everywhere; picking such a
  cluster takes you straight to `kauth`.
- **Tabs come back** after the Rex server restarts: the picker offers to
  restore them.

| | |
|---|---|
| `kctx [CTX]` / `kns [NS\|all]` | switch this pane's cluster / namespace (no argument: picker) |
| `kauth [cluster]` | log in again when the credentials expired: opens the login page and reads the pasted `kubectl config` commands (only those) |
| `ktint [#hex\|off] [--save]` | production background: presets previewed live on the pane (enter saves), or try a color here |
| `khelp [filter]` | every command in a picker, with usage; enter runs it |
| `rex8s use-context CTX` | the machine's default context (what `kubectl` uses outside rex8s panes) |
| `REX8S_FORCE=1 kubectl …` | skip the production confirmation (scripts) |

## What's broken

- **`kstatus [-w]`** — the cluster's health on one screen: nodes (pressure,
  CPU/memory), pods, unavailable workloads, Flux, failed jobs, firing alerts
  (from an Alertmanager or Prometheus inside the cluster), expiring
  certificates (cert-manager) and the last hour's warnings. `-w` refreshes
  every 30s.
- **`kfail [-n|-A] [filter]`** — only the unhealthy pods (all namespaces; `-n`
  this one; `-A` every cluster), with their recent events and last log lines
  in the preview — the crashed container's logs when it restarted. Logs are
  fetched only for the pod under the cursor. Enter: logs, why, ask, shell,
  describe, events.
- **`kpods [failing] [filter]`** — the pods of every cluster in one picker, the
  ones that need attention first; same preview and actions as `kfail`.
  `kpods failing` is `kfail -A`.
- **`kwarn [-n]`** — only Warning events (OOMKilled, BackOff, probes, image
  pulls), deduplicated. Also the bottom pane of the k9s layout.
  **`kwarn -A [cluster…]`**: every cluster's (or the ones named) in one
  stream, each line led by its cluster.
- **`kjobs [-n|-A] [failed|running|done|suspended|cron] [filter]`** — Jobs and
  CronJobs, failed first, with logs and recent runs in the preview. Enter: run
  a CronJob now, rerun, suspend/resume, logs, delete.
- **`kdisk [-A] [-a] [filter]`** — how full the volumes are: each PVC's real
  usage, read from the nodes' kubelets, and the nodes' own disks, fullest first
  (above 70% by default, `-a` all, `-A` every cluster). Local-path/hostPath
  PVCs, which share their node's disk, are listed under the node.
- **`kreq [-n] [filter]`** — requests and limits against real usage (`kubectl
  top`): near the memory limit (OOM risk), reserving far more than it uses
  (cost, with the total), and no requests.

## Logs

- **`klogs [-A] [-g REGEX] [-s DUR] [filter]`** — follow logs in a split. The
  picker lists the namespace's workloads first — all their pods merged into one
  stream, and the new ones join after a rollout — then single pods. `-A` lists
  the workloads and pods of every cluster: mark any mix and they follow in one
  stream, each line led by its cluster and pod. `-g` keeps only matching lines
  (highlighted); `-s 1h` starts an hour back.
- **`kgrep [-A] [pattern]`** — `klogs` that searches: pick a workload or pod,
  then type what to look for; it keeps following, so new matches keep coming.
  `kgrep timeout` skips the question.
- **Cluster URLs in the logs are clickable** from your machine:
  `http://grafana.monitoring.svc.cluster.local:3000/…` opens that service, and a
  pod's own `Listening on http://0.0.0.0:8080` opens that pod. The text stays as
  logged; the link goes to a localhost port the log pane holds, and `kubectl
  port-forward` only starts when you click.

## Ask Claude

- **`kwhy [filter]`** — pick a pod and ask Claude why it is unhealthy: describe
  (env values redacted), events, current and previous logs; Claude's tools are
  off. One answer, in a split.
- **`kask [filter]`** / **`cmd | kask`** — a Claude Code (or Codex) conversation
  in a split, already holding the context. The picker's first row is the whole
  cluster (`kstatus`, broken pods with their last logs, the last hour's
  warnings); below it every pod, broken first. `kubectl logs x | kask` sends
  whatever you pipe in. Read-only `kubectl`/`flux` commands run without asking;
  anything else asks for your approval, and on production the guard refuses
  changes unless re-run with `REX8S_FORCE=1`. The context is saved in
  `~/.cache/rex8s/dumps/`; `kask --dump` only saves it. Logs are sent as they
  are: they may hold sensitive data.
- **`kincident [cluster]`** — a Rex session laid out for an incident:
  `kstatus -w` and the live warnings on the left, what changed lately
  (`kwhat`) and a shell on the right. `--report` writes a Markdown summary for
  the postmortem (timeline from an hour before the start, broken pods with
  logs, warnings); `--ask` opens a `kask` conversation on it.

## Reach a service or a pod

- **`kpf [-n|-A] [filter]`** — port-forward a service (every namespace, this
  pane's first; `-n` only it; `-A` every cluster, and a filter matching one
  service starts it at once). It holds a free local port in the background and
  prints how to use it: the URL, and for Postgres, MySQL, Redis, Mongo or gRPC
  the client command. `kubectl port-forward` only runs while something is
  connected: it starts on the first connection, stops after 10 minutes idle
  and starts again on the next. The forward itself goes away after 8 hours
  without a connection (`forward_expire` in the config). A failed forward
  answers HTTP with a page carrying kubectl's error.
- **`kpf -l`** — the running port-forwards: open, copy the URL, save as a
  favorite, stop. They also show in the Raycast Kube Context menu bar.
- **`kpf NAME`** · **`kpf -f`** — a favorite from the config (`forwards:`):
  starts it in the background when needed and opens the browser (a database
  prints its URL and client command instead). `kpf -f` picks one. To make one,
  start a forward and pick **save** in `kpf -l`.
- **`kexec [-A] [filter]`** — shell into a pod in a split (`-A`: a pod of any
  cluster). Production asks first.
- **`knode [filter]`** — a shell on a node in a split (`kubectl debug node`,
  the node's filesystem at `/host`). The debug pod is removed when you exit;
  production asks first.
- **`knet`** — a throwaway pod with network tools (netshoot: curl, dig, nc,
  tcpdump) in this pane's namespace — "does the service answer from inside
  the cluster?". Removed when you exit; production asks first.

## Flux / GitOps

- **`fdiff [-A] [filter]`** — inside the GitOps repo, before you merge: what
  your working tree would change on the cluster(s) (`flux diff
  kustomization`). Warns when your branch is behind.
- **`fwatch [-A] [--now]`** — after you push or merge: follows HEAD until Flux
  fetched it, every Kustomization applied it, and the Deployments/
  StatefulSets/DaemonSets (and HelmReleases) it changed finished rolling out.
  Progress in the Rex tab header, a notification at the end. Works with forks,
  merge commits, squash and rebase merges.
- **`kgit [filter]`** — where do I change this? Pick a workload and it follows
  Flux's labels — HelmRelease → Kustomization → GitRepository — to the
  repository, the path and, with a local clone (this directory or
  `gitops_repos` in the config), the file and line. Enter opens it in your
  editor (Cursor, VS Code, Zed or `$EDITOR`) or on GitHub.
- **`kflux`** — Flux resources of this pane's cluster, failing first, with
  their events: reconcile and wait, describe, suspend/resume.
- **Flux monitor** (palette) — a dashboard of every cluster, with
  notifications when something starts failing or recovers; the tab name is the
  health summary.

## Across clusters

- **`kmulti [group] [-n ns|-A] [cmd…]`** (⇧⌘M) — a tab with one pane per
  cluster, side by side (a grid from four on), each isolated with its own label
  and tint. Pick the clusters, or name a group from the config (`kmulti prod`).
  `kmulti -n api get pods` has every pane run the command first and stay a
  shell. In that tab, **`kall CMD`** types `CMD` in every cluster pane at once
  (production panes still ask before a change).
- **`kwhat [duration] [filter]`** — what changed? A timeline of every cluster,
  newest first: rollouts with the image change (or "rollout restart"), what
  Flux applied, Helm upgrades and failures, new commits fetched. Default the
  last hour; `kwhat 6h api`. Flux's part comes from events, which the API
  server keeps about an hour.
- **`kver [-d] [filter]`** — which version runs where: the image tag of every
  workload, one column per cluster. Rows that differ come first; `-d` shows
  only those.
- **`kdiff [filter]`** — a workload's spec on two clusters (YAML, without
  status and server-filled fields). Pick one row and it finds the same name on
  the other cluster, or mark two.
- **`krun [-c container] [filter] [-- CMD…]`** — run a command in every pod of
  the workloads you mark, on any cluster. `krun nginx -- nginx -s reload` skips
  the questions when one workload matches. Production asks once (y/N).
- **`kroll [filter]`** — `rollout restart` the workloads you mark and follow
  each rollout, with progress in the tab header. Production asks once (y/N).
- **`kcron [duration] [filter]`** — what CronJobs run next on every cluster
  (default 6h), one timeline with how the last run went; frequent jobs are
  folded. `kcron 24h backup`.
- **`kjunk [debug|pods|jobs|pv] [filter]`** — leftovers on every cluster: ended
  debug pods (`knode`, `knet`, `kubectl debug`), Failed/Evicted pods over a day
  old, Jobs finished over a week ago (not a CronJob's) and Released PVs. Mark
  and delete; production asks once (y/N).
