# Development

- [How it works](#how-it-works) · [Build and test](#build-and-test) ·
  [Demo mode](#demo-mode) · [Known limitations](#known-limitations)

## How it works

```
init.lua ── require("rex8s") ── rex/rex8s.lua     actions + key bindings + layouts (Lua, in the Rex server)
                                   │
                                   ▼  each pane runs
                              rex8s pick|pane|browser|…    (Go + Bubble Tea)
                                   │
                    pane: KUBECONFIG=<pane>.yaml:~/.kube/config
                          PATH=<bin>:$PATH  →  kubectl/helm/flux = rex8s-guard
```

The pane file (`~/.cache/rex8s/<block>.yaml`) holds **only** the
current-context and namespace — no clusters, no credentials. Clusters and users
come from the master kubeconfig. This matters with OIDC: kubectl writes the
refreshed token back to the file the user entry came from, so if each pane had
its own copy of the credentials, the master's refresh token would be
invalidated ("already claimed by another client").

`rex8s-guard` is a separate binary with no TUI dependencies: Bubble Tea's
`init()` queries the terminal background color and can take up to 5s on
terminals that don't answer — unacceptable in a wrapper that runs on every
`kubectl`.

Port-forwards (`kpf`, the links in the logs) are a local listener that starts
`kubectl port-forward` on a port of its own at the first connection and copies
the bytes; `kpf`'s run as a background `rex8s _pf` process registered in
`~/.cache/rex8s/pf/`, the logs' live inside the log pane.

## Build and test

```sh
make check      # fmt + vet (also for macOS and Linux) + test: before a commit
make install    # build both binaries into ~/.config/rex/rex8s/bin and run rex8s setup (no tests)
make build      # bin/rex8s and bin/rex8s-guard, with the version from git describe
make fmt        # gofmt -w cmd internal rex
make test       # go test ./...  (go test -short ./... skips the end-to-end test)
make race       # go test -race ./...
make lint       # the gofmt check + golangci-lint (.golangci.yml)
make raycast    # type-check the Raycast extension (tsc --noEmit)
```

`make lint` needs [golangci-lint](https://golangci-lint.run) v2 built with
the Go in `go.mod` (`go install
github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4`; CI pins the
same version). `.golangci.yml` keeps the standard linters without `errcheck`
and without the style-only staticcheck checks.

**Tests.** Each `foo.go` has its tests in `foo_test.go`. Code that runs
kubectl is tested with `internal/testkube`, a fake kubectl that answers by
context and arguments with a given stdout, stderr, exit code, delay, or a
child process holding the pipe:

```go
k := testkube.New(t).
	On("use", "get pods *", testkube.Reply{Stdout: podsJSON}).
	On("euw", "*", testkube.Reply{Stderr: "error: You must be logged in to the server (Unauthorized)\n", Exit: 1})
cl := kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig}
// … k.Calls() lists what ran
```

`cmd/rex8s/e2e_test.go` builds rex8s and runs `version`, `list
clusters|pods|failing|workloads`, `kver` and `kstatus` in [demo
mode](#demo-mode) (`REX8S_DEMO`, a throwaway HOME): it needs `python3` for the
demo kubectl and is skipped by `go test -short`.

CI (`.github/workflows/ci.yml`) runs the gofmt check, `go vet` for Linux and
macOS, `go test -race`, golangci-lint and the Raycast type check.

A release is a tag: pushing `vX.Y.Z` vets (Linux and macOS) and tests, builds
the binaries, runs the linux/amd64 one (`rex8s version`, `rex8s-guard
--version`) as a smoke test, publishes them and updates the Homebrew tap. Two
runs for the same tag wait for each other.

## Demo mode

`demo/` has a fake kubeconfig (`*.example.com` clusters) and fake `kubectl`,
`flux`, `claude` and `k9s` that answer with made-up data — crashing pods, a
failing HelmRelease, warnings, a commit rolling out. Use it to try rex8s
without a cluster, or to take screenshots without exposing real data:

```sh
rex8s demo on ~/path/to/rex8s/demo   # Rex panes, Raycast and k* commands now see only demo data
rex8s demo off
demo/record.sh                        # re-record docs/*.gif with VHS (demo mode only for the recording)
```

`docs/kmulti.gif`, `docs/tour.gif` and `docs/kview-rex.gif` are screen
recordings of the real app, driven by `demo/kmulti-scene.sh`,
`demo/tour-scene.sh` and `demo/kview-scene.sh` (demo mode on, Remote Control
on). `kview-scene.sh` opens its own window without the sidebar, waits while you
resize it and start recording, then plays.

## Known limitations

- **Early version.** Used daily on one setup; expect rough edges.
- **Made on macOS.** The Linux builds are used on one Ubuntu server as a
  remote Rex host; what needs the Mac itself is listed in
  [On a Linux server](install.md#on-a-linux-server).
- **Tested with Rex Beta 0.1.0 (1829).** rex8s uses the Rex CLI and Lua API, which
  may still change between beta builds.
- **Login was only tested with OIDC (Dex).** Other auth methods (EKS
  `aws eks get-token`, GKE, client certificates) should work for everything
  except `kauth`, and the "login expired" detection may miss their error
  messages. Reports welcome.
- **Flux v2 only** (`kustomize.toolkit.fluxcd.io`, `helm.toolkit.fluxcd.io`,
  `source.toolkit.fluxcd.io`).
