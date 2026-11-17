# Configuration

- [config.yaml](#configyaml) · [In any shell](#in-any-shell) ·
  [Integrations](#integrations)

## config.yaml

`~/.config/rex/rex8s/config.yaml` (everything is optional). `rex8s config`
lists the settings; `rex8s config set KEY VALUE`, `unset KEY`, and
`add`/`remove KEY VALUE` for lists change them without touching the rest of the
file (comments included) — refusing if you saved the file meanwhile, and undoing
a change that would break it. Clusters and forwards are edited in the file.

```yaml
kubeconfig: ~/.kube/config                 # or a list like KUBECONFIG: ~/.kube/config:~/.kube/lab.yaml
hide: [ "old context" ]                    # hidden from the picker (exact name)
clusters:                                  # match = substring of the server URL
  - { match: k8s.us-east.example.com, name: use,     prod: true }
  - { match: staging.example.com,     name: staging }
prod_background: "#2b1216"                 # tint for production panes
monitor_interval: 2m                       # Flux monitor poll interval
forward_expire: 8h                         # a kpf/Raycast forward unused this long goes away (off: never)
login_url: https://login.example.com       # page kauth opens (per cluster: clusters[].login)
notifications: true                        # macOS notifications (fwatch, monitor, Failing Pods)
ai_command: claude                         # CLI used by kwhy
ai_language: en                            # language kwhy and kask answer in
ask_agent: claude                          # kask opens claude or codex
node_debug_image: busybox:1.36             # image knode runs on the node
net_debug_image: nicolaka/netshoot:v0.13   # image knet runs in the namespace
gitops_repos: [ ~/dev/gitops ]             # local clones of the Flux repos, for kgit
groups: { prod: [ use, euw ] }             # cluster sets for kmulti GROUP
forwards:                                  # favorite port-forwards: kpf NAME, Raycast menu bar
  - { name: grafana, cluster: use, namespace: monitoring, service: grafana, port: 3000, path: /dashboards }
  - { name: prometheus, cluster: staging, namespace: monitoring, service: prometheus, port: 9090 }
```

Without a config file nothing is treated as production and short names come
from the context name.

## In any shell

The `k*` commands live in `~/.config/rex/rex8s/bin`, which rex8s panes put on
`PATH`. To have them in every shell:

```sh
echo 'export PATH="$HOME/.config/rex/rex8s/bin:$PATH"' >> ~/.zshrc   # bash: ~/.bashrc
```

Outside a rex8s pane they use the machine's default context (`kubectl config
use-context`, `rex8s use-context`, or the Raycast Kube Context menu), and the
`kubectl`/`helm`/`flux` wrappers bring the production confirmation along.
Outside Rex, commands that open a split run in the terminal itself.

To see the cluster in the prompt (instant: it reads the kubeconfig, never the
cluster) — `PROD use/ns` in red for production:

```sh
# zsh, with oh-my-zsh or setopt prompt_subst
RPROMPT='$(~/.config/rex/rex8s/bin/rex8s-guard --prompt)'
```

`rex8s-guard --prompt plain` prints it without zsh color codes, for other
shells and prompt tools.

## Integrations

Everything outside Rex is optional. How to turn each one on or off:

| Integration | Used by | On | Off |
|---|---|---|---|
| **Production tint** | every pane | default; color via `prod_background` or `ktint` | `prod_background: off` (or `ktint off --save`) |
| **macOS notifications** | `fwatch`, Flux monitor, Failing Pods menu bar (a pod that newly broke) | default | `notifications: false` |
| **Claude / Codex** | `kwhy`, `kask` | `kwhy`: `claude-auto`, then `claude`, or `ai_command: <cli>`; `kask`: `claude` or `codex` (`ask_agent`) | `ai_command: off` |
| **Login page** | `kauth`, picking a logged-out cluster | `login_url` (or `clusters[].login`) | remove `login_url`; `kauth` still accepts a paste |
| **Flux CLI** (`flux`) | `fdiff` | install it (`brew install fluxcd/tap/flux`) | don't use `fdiff` |
| **k9s** | `⇧⌘K` layout | install it | without it, the layout shows `kstatus -w` in its place |
| **Raycast extension** | Open Cluster, Search Pods/Services/Workloads/Flux, the menu bars | `cd raycast && npm install && npm run dev` (from a clone) | Raycast → Extensions → rex8s → Uninstall (or disable single commands there) |
| **Raycast menu bar** | Flux Status, Failing Pods, Alerts, Kube Context | enable those commands in Raycast | disable them in Raycast settings |
| **Cluster alerts** | `kstatus`, Alerts menu bar | automatic when an Alertmanager (port 9093) or Prometheus (9090) service runs in the cluster; read through the API server proxy | nothing to turn off: no such service, no Alerts line |
| **Production guard** | `kubectl`/`helm`/`flux` in panes (and any shell with the PATH) | default: asks at a terminal; without one (agents, scripts) refuses | `REX8S_FORCE=1` per command |

`kwhy` and `kask` send the pod's describe output (env values redacted), events
and logs to the model behind your `claude` (or `codex`) CLI. Logs can still
contain sensitive data — turn `kwhy` off with `ai_command: off` where that
isn't acceptable.
