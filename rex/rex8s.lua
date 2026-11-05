-- Kubernetes: one cluster per pane. Logic lives in k8s/bin/rex8s.
local SHELL = "com.superlogical.terminal.shell"
local K8S   = os.getenv("HOME") .. "/.config/rex/rex8s/bin/rex8s"

local function term(argv, label)
  return rex.layout.block{ flavor = SHELL, label = label,
                           options = { command = argv, shell = "none" } }
end
local function check(r, err) if err ~= nil then error(err, 0) end; return r end
-- result of an action that opened a tab: the window, so a launcher (Raycast)
-- can show it
local function opened(w, err) w = check(w, err); return { window_id = w and w.window_id } end
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
    return opened(rex.session.new_window{ layout = term(with_ns({ K8S, "pane", args.ctx }, args.ns), "k8s") })
  end,
}

-- new tab: k9s on the left | shell on top + Warning events below on the right
rex.action{
  name = "k8s_layout", title = "Kubernetes: Cluster Layout (ctx)", category = "Kubernetes",
  args = { ctx = "string", ns = "string?" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{
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
    return opened(rex.session.new_window{ layout = term(argv, "logs " .. args.pod) })
  end,
}

-- new tab: ask Claude why a pod is unhealthy (kwhy), used by Raycast
rex.action{
  name = "k8s_why", title = "Kubernetes: Explain Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ layout = term({ K8S, "whytail", args.ctx, args.ns, args.pod }, "why " .. args.pod) })
  end,
}

-- new tab: talk to Claude about a pod (kask), used by Raycast
rex.action{
  name = "k8s_ask", title = "Kubernetes: Ask Claude about Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ layout = term({ K8S, "askpod", args.ctx, args.ns, args.pod }, "ask " .. args.pod) })
  end,
}

-- new tab: shell into a pod's container (kexec), used by Raycast
rex.action{
  name = "k8s_exec", title = "Kubernetes: Shell into Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string", container = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "exectail", args.ctx, args.ns, args.pod }
    if args.container and args.container ~= "" then argv[#argv + 1] = args.container end
    return opened(rex.session.new_window{ layout = term(argv, "exec " .. args.pod) })
  end,
}

-- new tab: port-forward a service on a free local port (used by Raycast)
rex.action{
  name = "k8s_pf", title = "Kubernetes: Port-forward (ctx, ns, svc, port)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", svc = "string", port = "number" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{
      layout = term({ K8S, "pftail", args.ctx, args.ns, args.svc, "auto:" .. tostring(math.floor(args.port)) }, "pf " .. args.svc) })
  end,
}

-- new tab: kstatus -w of a cluster, used by the Raycast alerts menu bar
rex.action{
  name = "k8s_status", title = "Kubernetes: Cluster Status (ctx)", category = "Kubernetes",
  args = { ctx = "string" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ layout = term({ K8S, "kstatus", "-w", "--context", args.ctx }, "status") })
  end,
}

-- new tab: the failing pods of a cluster (kfail), used by the Raycast menu bar
rex.action{
  name = "k8s_fail", title = "Kubernetes: Failing Pods (ctx)", category = "Kubernetes",
  args = { ctx = "string", filter = "string?" },
  run  = function(ctx, args)
    local script = '"$0" kfail --wait --context "$1" "$2" || { printf "\\npress enter to close"; read -r _; }'
    return opened(rex.session.new_window{
      layout = rex.layout.block{ flavor = SHELL, label = "kfail",
        options = { command = { "/bin/sh", "-c", script, K8S, args.ctx, args.filter or "" }, shell = "none" } } })
  end,
}

-- new tab: every pod of one or more workloads, merged (klogs), optionally
-- only lines matching grep — Raycast's Search Workloads
rex.action{
  name = "k8s_wlogs", title = "Kubernetes: Workload Logs (specs, grep)", category = "Kubernetes",
  args = { specs = "string", grep = "string?", label = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "wlogtail" }
    if args.grep and args.grep ~= "" then argv[#argv + 1] = "--grep=" .. args.grep end
    for spec in string.gmatch(args.specs, "[^\n]+") do argv[#argv + 1] = spec end
    return opened(rex.session.new_window{ layout = term(argv, args.label or "logs") })
  end,
}

-- new tab: rollout restart of workloads and follow them (kroll), used by
-- Raycast; production asks y/N in the tab
rex.action{
  name = "k8s_roll", title = "Kubernetes: Restart Workloads (targets)", category = "Kubernetes",
  args = { targets = "string", label = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "kroll" }
    for t in string.gmatch(args.targets, "[^\n]+") do argv[#argv + 1] = "--target"; argv[#argv + 1] = t end
    local script = '"$0" "$@"; printf "\\npress enter to close"; read -r _'
    local cmd = { "/bin/sh", "-c", script }
    for _, v in ipairs(argv) do cmd[#cmd + 1] = v end
    return opened(rex.session.new_window{
      layout = rex.layout.block{ flavor = SHELL, label = args.label or "restart",
        options = { command = cmd, shell = "none" } } })
  end,
}

-- kmulti: one isolated cluster pane per context, side by side (a grid from
-- four on); each runs cmd first when given, then stays a shell
local function split_list(s)
  local out = {}
  for x in string.gmatch(s or "", "[^,]+") do out[#out + 1] = x end
  return out
end

local function row(cells)
  if #cells == 1 then return cells[1] end
  local rest = {}
  for i = 2, #cells do rest[#rest + 1] = cells[i] end
  return rex.layout.horizontal(1 / #cells, cells[1], row(rest))
end

local function grid(cells)
  if #cells <= 3 then return row(cells) end
  local top, bottom = {}, {}
  local half = math.ceil(#cells / 2)
  for i, c in ipairs(cells) do
    if i <= half then top[#top + 1] = c else bottom[#bottom + 1] = c end
  end
  return rex.layout.vertical(0.5, row(top), row(bottom))
end

rex.action{
  name = "k8s_multi", title = "Kubernetes: Clusters Side by Side (ctxs)", category = "Kubernetes",
  args = { ctxs = "string", ns = "string?", cmd = "string?" },
  run  = function(ctx, args)
    local cells = {}
    for _, c in ipairs(split_list(args.ctxs)) do
      local argv = { K8S, "pane" }
      if args.cmd and args.cmd ~= "" then argv[#argv + 1] = "--run"; argv[#argv + 1] = args.cmd end
      argv[#argv + 1] = c
      if args.ns and args.ns ~= "" then argv[#argv + 1] = args.ns end
      cells[#cells + 1] = term(argv, "k8s")
    end
    if #cells == 0 then error("no clusters", 0) end
    return opened(rex.session.new_window{ window_label = "multi", layout = grid(cells) })
  end,
}

-- palette: pick clusters in a new tab, which then opens the side-by-side tab
rex.action{
  name = "k8s_multi_pick", title = "Kubernetes: Clusters Side by Side…", category = "Kubernetes",
  keywords = "k8s multi clusters compare side by side kmulti",
  run  = function(ctx, args)
    local w = check(rex.session.new_window{ layout = term({ K8S, "kmulti", "--close" }, "kmulti") })
    -- show the picker tab (same round trip as k8s_pick)
    if w and w.window_id and ctx.origin ~= "api" then
      pcall(rex.client.queue, "k8s_focus_window", { window_id = w.window_id })
    end
    return { window_id = w and w.window_id }
  end,
}

-- incident layout (kincident): kstatus -w over the live warnings on the
-- left; what changed lately (kwhat, then a shell) over a cluster shell on
-- the right. Runs in the session kincident creates.
rex.action{
  name = "k8s_incident", title = "Kubernetes: Incident Layout (ctx)", category = "Kubernetes",
  args = { ctx = "string" },
  run  = function(ctx, args)
    local changes = '"$0" kwhat 2h; exec "$0" pane "$1"'
    return opened(rex.session.new_window{
      window_label = "incident",
      layout = rex.layout.horizontal(0.55,
        rex.layout.vertical(0.6,
          term({ K8S, "kstatus", "-w", "--context", args.ctx }, "status"),
          term({ K8S, "warnings", args.ctx }, "warnings")),
        rex.layout.vertical(0.5,
          rex.layout.block{ flavor = SHELL, label = "changes",
            options = { command = { "/bin/sh", "-c", changes, K8S, args.ctx }, shell = "none" } },
          term({ K8S, "pane", args.ctx }, "k8s"))),
    })
  end,
}

-- new tab: a favorite port-forward (config forwards), used by Raycast
rex.action{
  name = "k8s_fwd", title = "Kubernetes: Favorite Port-forward (name)", category = "Kubernetes",
  args = { name = "string" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ focus = false, layout = term({ K8S, "fwd", args.name }, "pf " .. args.name) })
  end,
}

-- new tab: log in again to a cluster (kauth), used by the Raycast extension
rex.action{
  name = "k8s_kauth", title = "Kubernetes: Log In (ctx)", category = "Kubernetes",
  args = { ctx = "string" },
  run  = function(ctx, args)
    local script = '"$0" kauth "$1"; printf "\\npress enter to close"; read -r _'
    return opened(rex.session.new_window{
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
              return opened(rex.session.new_window{ layout = term({ K8S, "monitor" }, "flux monitor") })
            end }

rex.bind("shift+cmd+j", "k8s_pick")
rex.bind("shift+cmd+k", "k8s_pick_layout")
rex.bind("shift+cmd+m", "k8s_multi_pick")
