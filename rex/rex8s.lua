-- Kubernetes: one cluster per pane. Logic lives in k8s/bin/rex8s.
local SHELL = "com.superlogical.terminal.shell"
local K8S   = os.getenv("HOME") .. "/.config/rex/rex8s/bin/rex8s"

local function term(argv, label)
  return rex.layout.block{ flavor = SHELL, label = label,
                           options = { command = argv, shell = "none" } }
end
local function check(r, err) if err ~= nil then error(err, 0) end; return r end
local function with_ns(argv, ns) if ns and ns ~= "" then argv[#argv + 1] = ns end; return argv end

-- new tab with the cluster picker; the tab becomes the cluster (or the k9s
-- layout), or closes on esc
local function picker(mode)
  return function(ctx, args)
    local w = check(rex.session.new_window{ layout = term({ K8S, "pick", mode }, "k8s") })
    -- the app shows the new tab only when asked to; works when the action
    -- comes from the app itself (key binding, palette)
    if w and w.window_id and ctx.origin ~= "api" then
      -- A client action queued here runs before the app has received the
      -- new window, so it would be ignored. Queue k8s_focus_window instead:
      -- the app sends it back to the server (a round trip, after the window
      -- reached it), and that action queues the select.
      pcall(rex.client.queue, "k8s_focus_window", { window_id = w.window_id })
    end
  end
end

rex.action{
  name = "k8s_focus_window", title = "Kubernetes: Show Tab (internal)", category = "Kubernetes",
  args = { window_id = "string" },
  run  = function(ctx, args)
    pcall(rex.client.queue, "session.select", { session_id = ctx.session_id, window_id = args.window_id })
  end,
}

rex.action{ name = "k8s_pick", title = "Kubernetes: Open Cluster Shell…",  category = "Kubernetes",
            keywords = "k8s kubectl cluster context", run = picker("tab") }
rex.action{ name = "k8s_pick_layout", title = "Kubernetes: Open Cluster Layout…", category = "Kubernetes",
            keywords = "k8s k9s cluster layout", run = picker("layout") }

-- new tab: isolated shell on the cluster
rex.action{
  name = "k8s_tab", title = "Kubernetes: Cluster Shell (ctx)", category = "Kubernetes",
  args = { ctx = "string", ns = "string?" },
  run  = function(ctx, args)
    check(rex.session.new_window{ layout = term(with_ns({ K8S, "pane", args.ctx }, args.ns), "k8s") })
  end,
}

-- new tab: k9s on the left | shell on top + Warning events below on the right
rex.action{
  name = "k8s_layout", title = "Kubernetes: Cluster Layout (ctx)", category = "Kubernetes",
  args = { ctx = "string", ns = "string?" },
  run  = function(ctx, args)
    check(rex.session.new_window{
      layout = rex.layout.horizontal(0.6,
        term(with_ns({ K8S, "k9s", args.ctx }, args.ns), "k9s"),
        rex.layout.vertical(0.65,
          term(with_ns({ K8S, "pane", "--layout", args.ctx }, args.ns), "k8s"),
          term(with_ns({ K8S, "warnings", args.ctx }, args.ns), "warnings"))),
    })
  end,
}

-- new tab: follow a pod's logs (used by the Raycast extension)
rex.action{
  name = "k8s_logs", title = "Kubernetes: Pod Logs (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string", container = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "logtail", args.ctx, args.ns, args.pod }
    if args.container and args.container ~= "" then argv[#argv + 1] = args.container end
    check(rex.session.new_window{ layout = term(argv, "logs " .. args.pod) })
  end,
}

-- new tab: ask Claude why a pod is unhealthy (kwhy), used by Raycast
rex.action{
  name = "k8s_why", title = "Kubernetes: Explain Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string" },
  run  = function(ctx, args)
    check(rex.session.new_window{ layout = term({ K8S, "whytail", args.ctx, args.ns, args.pod }, "why " .. args.pod) })
  end,
}

-- new tab: shell into a pod's container (kexec), used by Raycast
rex.action{
  name = "k8s_exec", title = "Kubernetes: Shell into Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string", container = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "exectail", args.ctx, args.ns, args.pod }
    if args.container and args.container ~= "" then argv[#argv + 1] = args.container end
    check(rex.session.new_window{ layout = term(argv, "exec " .. args.pod) })
  end,
}

-- new tab: port-forward a service on a free local port (used by Raycast)
rex.action{
  name = "k8s_pf", title = "Kubernetes: Port-forward (ctx, ns, svc, port)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", svc = "string", port = "number" },
  run  = function(ctx, args)
    check(rex.session.new_window{
      layout = term({ K8S, "pftail", args.ctx, args.ns, args.svc, "auto:" .. tostring(math.floor(args.port)) }, "pf " .. args.svc) })
  end,
}

-- new tab: the failing pods of a cluster (kfail), used by the Raycast menu bar
rex.action{
  name = "k8s_fail", title = "Kubernetes: Failing Pods (ctx)", category = "Kubernetes",
  args = { ctx = "string", filter = "string?" },
  run  = function(ctx, args)
    local script = '"$0" kfail --wait --context "$1" "$2" || { printf "\\npress enter to close"; read -r _; }'
    check(rex.session.new_window{
      layout = rex.layout.block{ flavor = SHELL, label = "kfail",
        options = { command = { "/bin/sh", "-c", script, K8S, args.ctx, args.filter or "" }, shell = "none" } } })
  end,
}

-- new tab: log in again to a cluster (kauth), used by the Raycast extension
rex.action{
  name = "k8s_kauth", title = "Kubernetes: Log In (ctx)", category = "Kubernetes",
  args = { ctx = "string" },
  run  = function(ctx, args)
    local script = '"$0" kauth "$1"; printf "\\npress enter to close"; read -r _'
    check(rex.session.new_window{
      layout = rex.layout.block{ flavor = SHELL, label = "login",
        options = { command = { "/bin/sh", "-c", script, K8S, args.ctx }, shell = "none" } } })
  end,
}

-- reopen the k8s tabs lost when the Rex server restarted
rex.action{ name = "k8s_restore", title = "Kubernetes: Restore Cluster Tabs", category = "Kubernetes",
            keywords = "k8s restore reopen tabs session",
            run = function(ctx, args)
              check(rex.session.new_layer{
                bounds = { 0.3, 0.3, 0.4, 0.3 }, focus = true,
                layout = rex.layout.block{ flavor = SHELL, label = "restore",
                  options = { command = { "/bin/sh", "-c", '"$0" restore; sleep 2', K8S }, shell = "none" } } })
            end }

-- new tab: Flux dashboard for every cluster, with notifications
rex.action{ name = "k8s_flux_monitor", title = "Kubernetes: Flux Monitor", category = "Kubernetes",
            keywords = "k8s flux monitor gitops health",
            run = function(ctx, args)
              check(rex.session.new_window{ layout = term({ K8S, "monitor" }, "flux monitor") })
            end }

rex.bind("shift+cmd+j", "k8s_pick")
rex.bind("shift+cmd+k", "k8s_pick_layout")
