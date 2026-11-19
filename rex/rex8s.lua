-- Kubernetes: one cluster per pane. Logic lives in ~/.config/rex/rex8s/bin/rex8s.
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

-- show the tab an action just opened. The app shows a new tab only when
-- asked to; this works when the action comes from the app itself (key
-- binding, palette), not from the API (Raycast shows it itself).
local function show(ctx, w)
  if w and w.window_id and ctx.origin ~= "api" then
    -- A client action queued here runs before the app has received the
    -- new window, so it would be ignored. Queue k8s_focus_window instead:
    -- the app sends it back to the server (a round trip, after the window
    -- reached it), and that action queues the select.
    pcall(rex.client.queue, "k8s_focus_window", { window_id = w.window_id })
  end
  return { window_id = w and w.window_id }
end

-- new tab with the cluster picker; the tab becomes the cluster (or the kview
-- layout), or closes on esc
local function picker(mode)
  return function(ctx, args)
    return show(ctx, check(rex.session.new_window{ layout = term({ K8S, "pick", mode }, "k8s") }))
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
            keywords = "k8s k9s kview cluster layout", run = picker("layout") }

-- new tab: isolated shell on the cluster
rex.action{
  name = "k8s_tab", title = "Kubernetes: Cluster Shell (ctx)", category = "Kubernetes",
  args = { ctx = "string", ns = "string?" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ layout = term(with_ns({ K8S, "pane", args.ctx }, args.ns), "k8s") })
  end,
}

-- new tab: kview (or k9s, cluster_browser) on the left | shell on top +
-- Warning events below on the right
rex.action{
  name = "k8s_layout", title = "Kubernetes: Cluster Layout (ctx)", category = "Kubernetes",
  args = { ctx = "string", ns = "string?" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{
      layout = rex.layout.horizontal(0.6,
        term(with_ns({ K8S, "browser", args.ctx }, args.ns), "kview"),
        rex.layout.vertical(0.65,
          term(with_ns({ K8S, "pane", "--layout", args.ctx }, args.ns), "k8s"),
          term(with_ns({ K8S, "_warnings", args.ctx }, args.ns), "warnings"))),
    })
  end,
}

-- new tab: follow a pod's logs (used by the Raycast extension)
rex.action{
  name = "k8s_logs", title = "Kubernetes: Pod Logs (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string", container = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "_logs", args.ctx, args.ns, args.pod }
    if args.container and args.container ~= "" then argv[#argv + 1] = args.container end
    return opened(rex.session.new_window{ layout = term(argv, "logs " .. args.pod) })
  end,
}

-- new tab: ask Claude why a pod is unhealthy (kwhy), used by Raycast
rex.action{
  name = "k8s_why", title = "Kubernetes: Explain Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ layout = term({ K8S, "_why", args.ctx, args.ns, args.pod }, "why " .. args.pod) })
  end,
}

-- new tab: talk to Claude about a pod (kask), used by Raycast
rex.action{
  name = "k8s_ask", title = "Kubernetes: Ask Claude about Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string" },
  run  = function(ctx, args)
    return opened(rex.session.new_window{ layout = term({ K8S, "_askpod", args.ctx, args.ns, args.pod }, "ask " .. args.pod) })
  end,
}

-- new tab: shell into a pod's container (kexec), used by Raycast
rex.action{
  name = "k8s_exec", title = "Kubernetes: Shell into Pod (ctx, ns, pod)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", pod = "string", container = "string?" },
  run  = function(ctx, args)
    local argv = { K8S, "_exec", args.ctx, args.ns, args.pod }
    if args.container and args.container ~= "" then argv[#argv + 1] = args.container end
    return opened(rex.session.new_window{ layout = term(argv, "exec " .. args.pod) })
  end,
}

-- run rex8s in the background (no tab); its error becomes the action's
local function background(argv, env)
  local r, err = rex.exec(argv, { timeout = 60, env = env })
  if not r then error(err, 0) end
  if not r.ok then error(((r.stderr ~= "" and r.stderr or r.stdout):gsub("%s+$", "")), 0) end
  return { output = (r.stdout:gsub("%s+$", "")) }
end

-- a floating pane in this tab running a rex8s picker; it closes when the
-- picker is done, but stays (until enter) after an error or when what it
-- printed is the point (REX8S_HOLD: a database's URL, a copied URL).
-- hold: it always waits for enter (output to read).
local function floating_pane(hold, label, ...)
  local script = 'REX8S_HOLD=1 "$0" "$@" || { printf "\\npress enter to close"; read -r _; }'
  if hold then script = '"$0" "$@"; printf "\\npress enter to close"; read -r _' end
  local cmd = { "/bin/sh", "-c", script, K8S }
  for _, v in ipairs({ ... }) do cmd[#cmd + 1] = v end
  check(rex.session.new_layer{
    bounds = { 0.15, 0.15, 0.7, 0.6 }, focus = true,
    layout = rex.layout.block{ flavor = SHELL, label = label, options = { command = cmd, shell = "none" } } })
end
local function floating(label, ...) return floating_pane(false, label, ...) end

-- ── native prompts (rex.ui: Rex API revision 17, preview.21) ──────────
-- A question in the command palette of every connected Rex app (the
-- iPhone's too); the first answer wins. The palette actions below ask
-- this way; where Rex is older (no rex.ui) or no app can show it, they
-- fall back to the rex8s pickers in a pane, as before. Prompts only work
-- in an action's run and what it calls directly (not through pcall).

local FALLBACK = { unsupported = true, no_client = true, busy = true, failed = true }

-- ask("pick" | "input", spec): the answer, or nil and whether to fall
-- back (no app could ask; cancelled or timeout: just stop)
local function ask(kind, spec)
  if not rex.ui then return nil, true end
  local v, why
  if kind == "pick" then v, why = rex.ui.pick(spec) else v, why = rex.ui.input(spec) end
  if v == nil then return nil, FALLBACK[why] == true end
  return v
end

-- new tab: log in again to a cluster (kauth)
local function kauth_window(cluster)
  local script = '"$0" kauth "$1"; printf "\\npress enter to close"; read -r _'
  return rex.session.new_window{
    layout = rex.layout.block{ flavor = SHELL, label = "login",
      options = { command = { "/bin/sh", "-c", script, K8S, cluster }, shell = "none" } } }
end

-- a cluster's login expired: ask whether to log in again (kauth in a new
-- tab); true when the login tab opened
local function offer_login(cluster)
  local v = ask("pick", { title = "Login to " .. cluster .. " expired", items = {
    { id = "login", title = "Log in again", subtitle = "kauth " .. cluster .. " in a new tab", icon = "person" },
    { id = "later", title = "Not now", icon = "clock" },
  } })
  if v ~= "login" then return false end
  check(kauth_window(cluster))
  return true
end

-- rex8s list ... (JSON); its {"error": ...} becomes the action's, and an
-- expired login offers to log in again
local function rex8s_json(...)
  local r, err = rex.exec({ K8S, ... }, { timeout = 25 })
  if not r then error(err, 0) end
  local ok, v = pcall(rex.json.decode, r.stdout)
  if not ok or type(v) ~= "table" then
    error(((r.stderr ~= "" and r.stderr or r.stdout):gsub("%s+$", "")), 0)
  end
  if v.error then
    if v.loginExpired and v.cluster and v.cluster ~= "" and rex.ui then
      if offer_login(v.cluster) then error("logging in to " .. v.cluster .. " in a new tab; run this again after", 0) end
      error("login to " .. v.cluster .. " expired: kauth " .. v.cluster, 0)
    end
    error(v.error, 0)
  end
  return v
end

local function int(n) return tostring(math.floor(tonumber(n) or 0)) end

-- "14:30" of an RFC 3339 time
local function hhmm(t) return (tostring(t or ""):match("T(%d%d:%d%d)")) or tostring(t) end

-- context → short name, of the clusters listed last (for titles)
local SHORT = {}
local function short_of(context) return SHORT[context] or context end

-- the clusters (rex8s list clusters) as pick items; only_prod: production ones
local function cluster_items(only_prod)
  local items = {}
  for _, c in ipairs(rex8s_json("list", "clusters")) do
    SHORT[c.context] = c.short
    if not only_prod or c.prod then
      items[#items + 1] = { id = c.context, title = c.short, icon = "server",
                            subtitle = (c.context ~= c.short) and c.context or nil,
                            detail = c.prod and "PROD" or nil }
    end
  end
  return items
end

local function pick_cluster(title)
  return ask("pick", { title = title, placeholder = "Filter clusters", items = cluster_items(false) })
end

-- k8s_ui: a question asked for rex8s itself (the production guard asking
-- to allow an agent's change, internal/rex.Ask): spec is the prompt as JSON
-- with kind "pick" or "input". Returns { ok, value | values (newline-
-- separated, multi), other } or { ok = false, reason }.
rex.action{
  name = "k8s_ui", title = "Kubernetes: Ask (internal)", category = "Kubernetes",
  args = { spec = "string" },
  run  = function(ctx, args)
    if not rex.ui then return { ok = false, reason = "unsupported" } end
    local ok, s = pcall(rex.json.decode, args.spec)
    if not ok or type(s) ~= "table" or type(s.title) ~= "string" then error("k8s_ui: bad spec", 0) end
    local kind = s.kind
    s.kind = nil
    local v, why
    if kind == "pick" then v, why = rex.ui.pick(s) else v, why = rex.ui.input(s) end
    if v == nil then return { ok = false, reason = why } end
    if type(v) == "table" then
      if v.text ~= nil then return { ok = true, value = v.text, other = true } end
      return { ok = true, multi = true, values = table.concat(v, "\n") }
    end
    return { ok = true, value = v }
  end,
}

-- port-forward a service in the background on a free local port, and open
-- it in the browser when it speaks HTTP (used by Raycast)
rex.action{
  name = "k8s_pf", title = "Kubernetes: Port-forward (ctx, ns, svc, port)", category = "Kubernetes",
  args = { ctx = "string", ns = "string", svc = "string", port = "number" },
  run  = function(ctx, args)
    return background({ K8S, "_pfopen", args.ctx, args.ns, args.svc, tostring(math.floor(args.port)) })
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
local function wlogs_window(specs, grep, label)
  local argv = { K8S, "_wlogs" }
  if grep and grep ~= "" then argv[#argv + 1] = "--grep=" .. grep end
  for spec in string.gmatch(specs, "[^\n]+") do argv[#argv + 1] = spec end
  return rex.session.new_window{ layout = term(argv, label or "logs") }
end

rex.action{
  name = "k8s_wlogs", title = "Kubernetes: Workload Logs (specs, grep)", category = "Kubernetes",
  args = { specs = "string", grep = "string?", label = "string?" },
  run  = function(ctx, args) return opened(wlogs_window(args.specs, args.grep, args.label)) end,
}

-- new tab: rollout restart of workloads and follow them (kroll), used by
-- Raycast; production asks y/N in the tab
local function roll_window(targets, label)
  local argv = { K8S, "kroll" }
  for t in string.gmatch(targets, "[^\n]+") do argv[#argv + 1] = "--target"; argv[#argv + 1] = t end
  local script = '"$0" "$@"; printf "\\npress enter to close"; read -r _'
  local cmd = { "/bin/sh", "-c", script }
  for _, v in ipairs(argv) do cmd[#cmd + 1] = v end
  return rex.session.new_window{
    layout = rex.layout.block{ flavor = SHELL, label = label or "restart",
      options = { command = cmd, shell = "none" } } }
end

rex.action{
  name = "k8s_roll", title = "Kubernetes: Restart Workloads (targets)", category = "Kubernetes",
  args = { targets = "string", label = "string?" },
  run  = function(ctx, args) return opened(roll_window(args.targets, args.label)) end,
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

local function multi_window(ctxs, ns, cmd)
  local cells = {}
  for _, c in ipairs(split_list(ctxs)) do
    local argv = { K8S, "pane" }
    if cmd and cmd ~= "" then argv[#argv + 1] = "--run"; argv[#argv + 1] = cmd end
    argv[#argv + 1] = c
    if ns and ns ~= "" then argv[#argv + 1] = ns end
    cells[#cells + 1] = term(argv, "k8s")
  end
  if #cells == 0 then error("no clusters", 0) end
  return rex.session.new_window{ window_label = "multi", layout = grid(cells) }
end

rex.action{
  name = "k8s_multi", title = "Kubernetes: Clusters Side by Side (ctxs)", category = "Kubernetes",
  args = { ctxs = "string", ns = "string?", cmd = "string?" },
  run  = function(ctx, args) return opened(multi_window(args.ctxs, args.ns, args.cmd)) end,
}

-- palette: tick the clusters in the palette, then the side-by-side tab
-- opens (older Rex: kmulti's picker in a new tab)
rex.action{
  name = "k8s_multi_pick", title = "Kubernetes: Clusters Side by Side…", category = "Kubernetes",
  keywords = "k8s multi clusters compare side by side kmulti",
  run  = function(ctx, args)
    local function in_tab()
      return show(ctx, check(rex.session.new_window{ layout = term({ K8S, "kmulti", "--close" }, "kmulti") }))
    end
    if not rex.ui then return in_tab() end
    local v, fallback = ask("pick", { title = "Clusters side by side", placeholder = "Filter clusters",
                                      multiple = true, confirm = "Open", items = cluster_items(false) })
    if v == nil then if fallback then return in_tab() end return end
    if #v == 0 then return end
    return show(ctx, check(multi_window(table.concat(v, ","))))
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
          term({ K8S, "_warnings", args.ctx }, "warnings")),
        rex.layout.vertical(0.5,
          rex.layout.block{ flavor = SHELL, label = "changes",
            options = { command = { "/bin/sh", "-c", changes, K8S, args.ctx }, shell = "none" } },
          term({ K8S, "pane", args.ctx }, "k8s"))),
    })
  end,
}

-- a favorite port-forward (config forwards) in the background, opened in
-- the browser; used by Raycast
rex.action{
  name = "k8s_fwd", title = "Kubernetes: Favorite Port-forward (name)", category = "Kubernetes",
  args = { name = "string" },
  run  = function(ctx, args)
    return background({ K8S, "_fwd", args.name })
  end,
}

-- new tab: log in again to a cluster (kauth), used by the Raycast extension
rex.action{
  name = "k8s_kauth", title = "Kubernetes: Log In (ctx)", category = "Kubernetes",
  args = { ctx = "string" },
  run  = function(ctx, args) return opened(kauth_window(args.ctx)) end,
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
              return show(ctx, check(rex.session.new_window{ layout = term({ K8S, "monitor" }, "flux monitor") }))
            end }

-- new tab: kview on every cluster (k9s-like browser), no picker
rex.action{ name = "k8s_view_all", title = "Kubernetes: All Clusters (kview)", category = "Kubernetes",
            keywords = "k8s kview k9s browse pods workloads every cluster",
            run = function(ctx, args)
              return show(ctx, check(rex.session.new_window{ layout = term({ K8S, "kview", "-A" }, "kview") }))
            end }

-- palette: the favorite port-forwards (config forwards) in a picker; enter
-- starts one in the background and opens it. A picker rather than an action
-- per favorite: favorites saved from kpf -l show up without reloading Rex.
-- In the palette: a web UI opens in the browser, a database's URL is copied.
rex.action{ name = "k8s_fwd_pick", title = "Kubernetes: Favorite Port-forwards…", category = "Kubernetes",
            keywords = "k8s kpf port forward favorite favourite open service",
            run = function(ctx, args)
              if not rex.ui then return floating("kpf favorites", "kpf", "-f") end
              local fs = rex8s_json("list", "fwd")
              if #fs == 0 then error("no favorite forwards: kpf -l › save, or forwards: in the rex8s config", 0) end
              local items = {}
              for _, f in ipairs(fs) do
                items[#items + 1] = { id = f.name, title = f.name, icon = f.web and "globe" or "database",
                  subtitle = f.cluster .. " · " .. f.namespace .. "/" .. f.service .. ":" .. int(f.port),
                  detail = f.running and "running" or nil }
              end
              local v, fallback = ask("pick", { title = "Favorite port-forwards", placeholder = "Filter favorites",
                                                confirm = "Open", items = items })
              if v == nil then if fallback then floating("kpf favorites", "kpf", "-f") end return end
              return background({ K8S, "_fwd", "--copy", v })
            end }

-- palette: the running port-forwards (kpf -l): open, copy, save, stop
rex.action{ name = "k8s_pf_list", title = "Kubernetes: Running Port-forwards (kpf -l)", category = "Kubernetes",
            keywords = "k8s kpf port forward running list stop",
            run = function(ctx, args)
              if not rex.ui then return floating("kpf -l", "kpf", "-l") end
              local es = rex8s_json("list", "pf")
              if #es == 0 then error("no port-forwards running · kpf starts one", 0) end
              local items, by = {}, {}
              for _, e in ipairs(es) do
                local id = int(e.pid)
                by[id] = e
                items[#items + 1] = { id = id, icon = e.web and "globe" or "database",
                  title = e.short .. "  " .. e.namespace .. "/" .. e.service .. ":" .. int(e.remote),
                  subtitle = e.url, detail = e.prod and "PROD" or nil }
              end
              local pid, fallback = ask("pick", { title = "Running port-forwards", placeholder = "Filter port-forwards", items = items })
              if pid == nil then if fallback then floating("kpf -l", "kpf", "-l") end return end
              local e = by[pid]
              local act = ask("pick", { title = e.namespace .. "/" .. e.service .. " on " .. e.short .. " → " .. e.url, items = {
                { id = "open", title = e.web and "Open in the browser" or "Copy the URL", icon = e.web and "globe" or "link" },
                { id = "copy", title = "Copy " .. e.url, icon = "link" },
                { id = "save", title = "Save as a favorite", subtitle = "kpf NAME starts it again (Raycast too)", icon = "star" },
                { id = "stop", title = "Stop", subtitle = "frees the local port :" .. int(e["local"]), icon = "gear" },
              } })
              if act == nil then return end
              if act == "save" then
                local name = ask("input", { title = "Favorite name", value = e.service, confirm = "Save" })
                if name == nil then return end
                return background({ K8S, "kpf", "--save", pid, name })
              end
              return background({ K8S, "kpf", "--" .. act, pid })
            end }

-- palette: kall — asks for a command and types it in every cluster pane of
-- this tab (a kmulti tab); production panes still ask before a change
rex.action{ name = "k8s_kall", title = "Kubernetes: Type in Every Cluster Pane (kall)…", category = "Kubernetes",
            keywords = "k8s kall kmulti all panes broadcast send command",
            run = function(ctx, args)
              if not rex.ui then return floating("kall", "kall") end
              local line, fallback = ask("input", { title = "Type in every cluster pane of this tab",
                                                    placeholder = "get pods -n api", confirm = "Type it" })
              if line == nil then if fallback then floating("kall", "kall") end return end
              local argv = { K8S, "kall" }
              for w in line:gmatch("%S+") do argv[#argv + 1] = w end
              if #argv == 2 then return end
              return background(argv, { REX_BLOCK = ctx.block_id, REX_SESSION = ctx.session_id })
            end }

-- ── palette flows (native prompts; they need Rex with rex.ui) ─────────

local function need_ui(cmd)
  if not rex.ui then error("this needs a newer Rex (prompts, preview.21); in a cluster pane: " .. cmd, 0) end
end

-- palette: unlock production for a while (kunlock): the cluster, then how
-- long. Only from the palette, where a person answers: an agent running
-- this action through rex do gets the same questions in your palette.
rex.action{ name = "k8s_unlock", title = "Kubernetes: Unlock Production for a While…", category = "Kubernetes",
            keywords = "k8s kunlock unlock production guard prod maintenance incident",
            run = function(ctx, args)
              need_ui("kunlock [CLUSTER] [DURATION]")
              local un = rex8s_json("list", "unlocks")
              local items = { { id = "all", title = "Every production cluster", icon = "server",
                                detail = un["*"] and ("until " .. hhmm(un["*"])) or nil } }
              local seen = {}
              for _, c in ipairs(rex8s_json("list", "clusters")) do
                if c.prod and not seen[c.short] then
                  seen[c.short] = true
                  items[#items + 1] = { id = c.short, title = c.short, icon = "server", subtitle = c.context,
                                        detail = un[c.short] and ("until " .. hhmm(un[c.short])) or nil }
                end
              end
              local who = ask("pick", { title = "Unlock which production cluster?", placeholder = "Filter clusters", items = items })
              if who == nil then return end
              local dur = ask("pick", { title = "Unlock " .. who .. " for how long?", placeholder = "or type one: 45m, 3h",
                                        allow_other = true, items = { "15m", "30m", "1h", "2h", "4h" } })
              if dur == nil then return end
              if type(dur) == "table" then dur = dur.text end
              return background({ K8S, "_unlock", who, (dur:gsub("%s", "")) })
            end }

-- palette: lock again what kunlock unlocked
rex.action{ name = "k8s_lock", title = "Kubernetes: Lock Production Again…", category = "Kubernetes",
            keywords = "k8s kunlock off klock lock production guard",
            run = function(ctx, args)
              need_ui("kunlock off")
              local un = rex8s_json("list", "unlocks")
              local items = {}
              for short, until_ in pairs(un) do
                items[#items + 1] = { id = short, title = short == "*" and "Every production cluster" or short,
                                      icon = "server", detail = "until " .. hhmm(until_) }
              end
              if #items == 0 then error("nothing is unlocked", 0) end
              table.sort(items, function(a, b) return a.id < b.id end)
              if #items > 1 then table.insert(items, 1, { id = "*", title = "Lock everything", icon = "gear" }) end
              local who = ask("pick", { title = "Lock again", items = items, confirm = "Lock" })
              if who == nil then return end
              return background({ K8S, "kunlock", "off", who })
            end }

-- the workloads of a cluster (rex8s list workloads CTX) to tick
local function pick_workloads(title, cluster, confirm)
  local ws = rex8s_json("list", "workloads", cluster)
  if #ws == 0 then error("no workloads on " .. short_of(cluster), 0) end
  local items, by = {}, {}
  for _, w in ipairs(ws) do
    by[w.target] = w
    items[#items + 1] = { id = w.target, title = w.namespace .. "/" .. w.name, icon = "package",
                          subtitle = w.kind .. (w.image ~= "" and (" · " .. w.image) or ""),
                          detail = w.tag ~= "" and w.tag or nil }
  end
  local v = ask("pick", { title = title, placeholder = "Filter workloads", multiple = true, confirm = confirm, items = items })
  if v == nil or #v == 0 then return nil end
  local chosen = {}
  for _, id in ipairs(v) do chosen[#chosen + 1] = by[id] end
  return chosen
end

-- palette: rollout restart of workloads you tick (kroll), followed in a new
-- tab; production asks there before restarting
rex.action{ name = "k8s_roll_pick", title = "Kubernetes: Restart Workloads…", category = "Kubernetes",
            keywords = "k8s kroll rollout restart deployment statefulset daemonset",
            run = function(ctx, args)
              need_ui("kroll")
              local cluster = pick_cluster("Restart workloads on which cluster?")
              if cluster == nil then return end
              local ws = pick_workloads("Restart which workloads?", cluster, "Restart")
              if ws == nil then return end
              local targets = {}
              for _, w in ipairs(ws) do targets[#targets + 1] = w.target end
              return show(ctx, check(roll_window(table.concat(targets, "\n"), "restart " .. ws[1].name)))
            end }

-- palette: the logs of workloads you tick, merged, optionally only the
-- lines matching a filter (klogs + kgrep), in a new tab
rex.action{ name = "k8s_logs_pick", title = "Kubernetes: Workload Logs…", category = "Kubernetes",
            keywords = "k8s klogs kgrep logs tail follow workload grep filter",
            run = function(ctx, args)
              need_ui("klogs")
              local cluster = pick_cluster("Logs on which cluster?")
              if cluster == nil then return end
              local ws = pick_workloads("Logs of which workloads?", cluster, "Follow")
              if ws == nil then return end
              local grep = ask("input", { title = "Only lines matching (empty: every line)", placeholder = "error|timeout",
                                          confirm = "Follow" })
              if grep == nil then return end
              local specs = {}
              for _, w in ipairs(ws) do specs[#specs + 1] = w.spec end
              local label = "logs " .. ws[1].name
              if grep ~= "" then label = "grep " .. ws[1].name end
              return show(ctx, check(wlogs_window(table.concat(specs, "\n"), grep, label)))
            end }

-- a Flux object of a cluster (failing first): reconcile, suspend, resume,
-- events, describe — in a floating pane of this tab
local function flux_on(cluster)
  local fs = rex8s_json("list", "flux", cluster)
  if #fs == 0 then error("Flux isn't installed on " .. short_of(cluster), 0) end
  local rank = { failing = 0, reconciling = 1, suspended = 2 }
  table.sort(fs, function(a, b)
    local ra, rb = rank[a.state] or 3, rank[b.state] or 3
    if ra ~= rb then return ra < rb end
    return (a.kind .. a.namespace .. a.name) < (b.kind .. b.namespace .. b.name)
  end)
  local items, by = {}, {}
  for _, f in ipairs(fs) do
    local id = f.kind .. "/" .. f.namespace .. "/" .. f.name
    by[id] = f
    items[#items + 1] = { id = id, title = f.namespace .. "/" .. f.name, icon = "branch",
      subtitle = f.kind .. ((f.state ~= "ready" and f.message ~= "") and (" · " .. f.message) or ""),
      detail = f.state ~= "ready" and f.state or nil }
  end
  local id = ask("pick", { title = "Flux on " .. short_of(cluster), placeholder = "Filter Flux resources", items = items })
  if id == nil then return end
  local f = by[id]
  local toggle = f.state == "suspended" and "resume" or "suspend"
  local act = ask("pick", { title = f.kind .. " " .. f.namespace .. "/" .. f.name, items = {
    { id = "reconcile", title = "Reconcile now", subtitle = "and wait for the result", icon = "branch" },
    { id = toggle, title = toggle == "suspend" and "Suspend" or "Resume", subtitle = "patch spec.suspend", icon = "gear" },
    { id = "events", title = "Events", icon = "clock" },
    { id = "describe", title = "Describe", icon = "document" },
  } })
  if act == nil then return end
  floating_pane(true, "kflux " .. f.name, "_flux", cluster, f.kind, f.namespace, f.name, act)
end

rex.action{ name = "k8s_flux_pick", title = "Kubernetes: Flux Resources…", category = "Kubernetes",
            keywords = "k8s kflux flux reconcile suspend resume helmrelease kustomization gitops",
            run = function(ctx, args)
              need_ui("kflux")
              local items = {}
              for _, h in ipairs(rex8s_json("list", "flux")) do
                SHORT[h.context] = h.short
                if h.installed then
                  items[#items + 1] = { id = h.context, title = h.short, icon = "server",
                    subtitle = (h.prod and "PROD · " or "") .. int(h.ready) .. " ready",
                    detail = (tonumber(h.failing) or 0) > 0 and (int(h.failing) .. " failing") or nil }
                end
              end
              if #items == 0 then error("Flux isn't installed on any cluster", 0) end
              local cluster = ask("pick", { title = "Flux on which cluster?", placeholder = "Filter clusters", items = items })
              if cluster == nil then return end
              return flux_on(cluster)
            end }

-- the pods of a cluster (failing first): logs, shell, why, ask, delete
local function pods_on(ctx, cluster)
  local ps = rex8s_json("list", "pods", cluster)
  if #ps == 0 then error("no pods on " .. short_of(cluster), 0) end
  local function bad(p) return p.badge ~= "ok" and p.badge ~= "done" end
  table.sort(ps, function(a, b)
    local fa, fb = bad(a), bad(b)
    if fa ~= fb then return fa end
    if a.namespace ~= b.namespace then return a.namespace < b.namespace end
    return a.name < b.name
  end)
  local items, by = {}, {}
  for _, p in ipairs(ps) do
    local id = p.namespace .. "/" .. p.name
    by[id] = p
    local restarts = tonumber(p.restarts) or 0
    items[#items + 1] = { id = id, title = p.name, icon = "package",
      subtitle = p.namespace .. " · " .. p.status .. " · " .. int(p.ready) .. "/" .. int(p.total) .. " · " .. p.age,
      detail = (p.badge ~= "ok" and p.badge or "") .. (restarts > 0 and ("  ↻" .. int(restarts)) or "") }
  end
  local id = ask("pick", { title = "Pods on " .. short_of(cluster), placeholder = "Filter pods", items = items })
  if id == nil then return end
  local p = by[id]
  local act = ask("pick", { title = p.namespace .. "/" .. p.name .. " on " .. short_of(cluster), items = {
    { id = "logs", title = "Logs", subtitle = "follow in a new tab", icon = "document" },
    { id = "exec", title = "Shell", subtitle = "kexec in a new tab", icon = "terminal" },
    { id = "why", title = "Why is it unhealthy?", subtitle = "kwhy: Claude reads describe, events and logs", icon = "code" },
    { id = "ask", title = "Ask Claude about it", subtitle = "kask in a new tab", icon = "person" },
    { id = "delete", title = "Delete", subtitle = "its controller makes a new one; production asks first", icon = "gear" },
  } })
  if act == nil then return end
  local containers = p.containers or {}
  local container = nil
  if (act == "logs" or act == "exec") and #containers > 1 then
    container = ask("pick", { title = "Which container?", items = containers })
    if container == nil then return end
  end
  if act == "delete" then
    local ok = ask("pick", { title = "Delete pod " .. p.name .. " on " .. short_of(cluster) .. "?", items = {
      { id = "yes", title = "Delete " .. p.name, icon = "gear" }, { id = "no", title = "Cancel" } } })
    if ok ~= "yes" then return end
    -- in a pane: production asks there (type the cluster name)
    return floating_pane(true, "delete " .. p.name, "guard", "kubectl", "--context", cluster, "-n", p.namespace,
                         "delete", "pod", p.name)
  end
  local sub = ({ logs = "_logs", exec = "_exec", why = "_why", ask = "_askpod" })[act]
  local argv = { K8S, sub, cluster, p.namespace, p.name }
  if container then argv[#argv + 1] = container end
  return show(ctx, check(rex.session.new_window{ layout = term(argv, act .. " " .. p.name) }))
end

rex.action{ name = "k8s_pods_pick", title = "Kubernetes: Pods…", category = "Kubernetes",
            keywords = "k8s kpods pods logs exec shell why delete crashloop failing",
            run = function(ctx, args)
              need_ui("kpods")
              local cluster = pick_cluster("Pods on which cluster?")
              if cluster == nil then return end
              return pods_on(ctx, cluster)
            end }

-- palette: port-forward any service of a cluster (kpf): the service, then
-- the port; a web UI opens in the browser, a database's URL is copied
rex.action{ name = "k8s_pf_pick", title = "Kubernetes: Port-forward a Service…", category = "Kubernetes",
            keywords = "k8s kpf port forward service open browser database",
            run = function(ctx, args)
              need_ui("kpf")
              local cluster = pick_cluster("Port-forward on which cluster?")
              if cluster == nil then return end
              local ss = rex8s_json("list", "services", cluster)
              local items, by = {}, {}
              for _, sv in ipairs(ss) do
                local ports = {}
                for _, pt in ipairs(sv.ports or {}) do ports[#ports + 1] = int(pt.port) end
                if #ports > 0 then
                  local id = sv.namespace .. "/" .. sv.name
                  by[id] = sv
                  items[#items + 1] = { id = id, title = sv.name, subtitle = sv.namespace .. " · " .. sv.type,
                                        detail = table.concat(ports, " "), icon = "globe" }
                end
              end
              if #items == 0 then error("no TCP services on " .. short_of(cluster), 0) end
              local id = ask("pick", { title = "Port-forward on " .. short_of(cluster), placeholder = "Filter services", items = items })
              if id == nil then return end
              local sv = by[id]
              local port = int(sv.ports[1].port)
              if #sv.ports > 1 then
                local pitems = {}
                for _, pt in ipairs(sv.ports) do
                  pitems[#pitems + 1] = { id = int(pt.port), title = int(pt.port), subtitle = pt.name ~= "" and pt.name or nil }
                end
                port = ask("pick", { title = "Port of " .. sv.name, items = pitems })
                if port == nil then return end
              end
              return background({ K8S, "_pfopen", "--copy", cluster, sv.namespace, sv.name, port })
            end }

-- palette: a cluster's incident session (kincident)
rex.action{ name = "k8s_incident_pick", title = "Kubernetes: Incident Session…", category = "Kubernetes",
            keywords = "k8s kincident incident outage war room status warnings",
            run = function(ctx, args)
              need_ui("kincident [CLUSTER]")
              local cluster = pick_cluster("Incident on which cluster?")
              if cluster == nil then return end
              return background({ K8S, "kincident", cluster })
            end }

-- palette: find an object by name on every cluster (kfind) in a floating pane
rex.action{ name = "k8s_find", title = "Kubernetes: Find an Object by Name…", category = "Kubernetes",
            keywords = "k8s kfind find search object name every cluster",
            run = function(ctx, args)
              need_ui("kfind NAME")
              local name = ask("input", { title = "Find on every cluster", placeholder = "a pod, service, deployment… name",
                                          confirm = "Find" })
              if name == nil or name:match("^%s*$") then return end
              floating("kfind", "kfind", (name:gsub("^%s+", ""):gsub("%s+$", "")))
            end }

-- What a pane is, from the label rex8s gives it: a cluster shell ("PROD
-- use/api", "k8s staging/*") or a cluster browser ("kview PROD use/api",
-- "kview use", "k9s k8s use/api"; "kview" before it names itself, "kview
-- all" for -A). nil for anything else. A shell's record (what Restore
-- reads) names its exact context; the label has the short name.
local STATE = (os.getenv("XDG_STATE_HOME") or (os.getenv("HOME") .. "/.local/state")) .. "/rex8s/panes/"

local function pane_of(b)
  local label = b and b.label or ""
  if label == "kview" or label == "k9s" then return { view = true } end
  local rest = label:match("^kview (.+)$") or label:match("^k9s (.+)$")
  local view = rest ~= nil
  if not view then
    rest = label:match("^PROD (.+)$") or label:match("^k8s (.+)$")
    if not rest then return nil end
  end
  rest = rest:gsub("^PROD ", "")
  rest = rest:gsub("^k8s ", "")
  local short, ns = rest:match("^([^/%s]+)/(%S+)$")
  short = short or rest:match("^([^/%s]+)$")
  if not short then return nil end
  -- kview -A: "all", or "all:use,euw" / "all:GROUP" for some of them
  if view and not ns and (short == "all" or short:match("^all:")) then return { view = true, all = true } end
  local p = { view = view, short = short, ns = ns, ctx = short }
  if not view then
    local f = io.open(STATE .. tostring(b.block_id):gsub("^block:", "") .. ".json", "r")
    if f then
      local ok, rec = pcall(rex.json.decode, f:read("*a") or "")
      f:close()
      if ok and type(rec) == "table" and rec.short == short and rec.context then p.ctx = rec.context end
    end
  end
  return p
end

-- the focused block and the other placed blocks of its tab
local function focused_and_tab(ctx)
  local list = check(rex.session.list_blocks{})
  local me
  for _, b in ipairs(list.blocks or {}) do
    if b.block_id == ctx.block_id then me = b end
  end
  if not me then error("no focused pane", 0) end
  local others = {}
  for _, b in ipairs(list.blocks or {}) do
    if b.block_id ~= me.block_id and b.window_id == me.window_id and b.placement == "placed" then
      others[#others + 1] = b
    end
  end
  return me, others
end

-- kview of a cluster in a split to the right of anchor, focused
local function open_kview(anchor, p)
  local argv = { K8S, "browser", p.ctx }
  if p.ns then argv[#argv + 1] = p.ns end
  check(rex.session.new_split{ anchor_id = anchor, direction = "horizontal", side = "after", ratio = 0.45,
                               focus = true, layout = term(argv, "kview") })
end

local function not_cluster()
  error("not a rex8s cluster pane (⇧⌘J opens one)", 0)
end

-- palette: kview of this pane's cluster (and namespace) in a split
rex.action{ name = "k8s_kview_here", title = "Kubernetes: Open This Pane's Cluster in kview", category = "Kubernetes",
            keywords = "k8s kview k9s browse this cluster pane split",
            run = function(ctx, args)
              local me = focused_and_tab(ctx)
              local p = pane_of(me)
              if not p or p.view then not_cluster() end
              open_kview(me.block_id, p)
            end }

-- key: from a cluster shell to the kview of the same cluster in this tab
-- (the ⇧⌘K layout's included), opening one in a split when there is none;
-- from a kview back to a shell of its cluster
rex.action{ name = "k8s_kview_toggle", title = "Kubernetes: Toggle Cluster Shell / kview", category = "Kubernetes",
            keywords = "k8s kview k9s shell switch toggle focus cluster",
            run = function(ctx, args)
              local me, others = focused_and_tab(ctx)
              local p = pane_of(me)
              if not p then not_cluster() end
              for _, b in ipairs(others) do
                local q = pane_of(b)
                local match = q and q.view ~= p.view and (
                  (p.view and (p.all or not p.short or q.short == p.short)) or
                  (not p.view and (not q.short or q.short == p.short)))
                if match then
                  check(rex.session.focus_block{ block_id = b.block_id })
                  return
                end
              end
              if not p.view then return open_kview(me.block_id, p) end
              if p.all or not p.short then error("no cluster shell in this tab", 0) end
              local argv = { K8S, "pane", p.ctx }
              if p.ns then argv[#argv + 1] = p.ns end
              check(rex.session.new_split{ anchor_id = me.block_id, direction = "horizontal", side = "after",
                                           ratio = 0.6, focus = true, layout = term(argv, "k8s") })
            end }

-- palette: switch the focused cluster shell's namespace (types kns NS in it)
rex.action{ name = "k8s_ns_here", title = "Kubernetes: Switch This Pane's Namespace…", category = "Kubernetes",
            keywords = "k8s kns namespace switch pane",
            run = function(ctx, args)
              need_ui("kns")
              local me = focused_and_tab(ctx)
              local p = pane_of(me)
              if not p or p.view then not_cluster() end
              local items = { { id = "all", title = "Every namespace", icon = "globe",
                                detail = (p.ns == "*") and "current" or nil } }
              for _, n in ipairs(rex8s_json("list", "namespaces", p.ctx)) do
                items[#items + 1] = { id = n, title = n, icon = "folder", detail = (n == p.ns) and "current" or nil }
              end
              local ns = ask("pick", { title = "Namespace of " .. p.short, placeholder = "Filter namespaces",
                                       allow_other = true, items = items })
              if ns == nil then return end
              if type(ns) == "table" then ns = ns.text end
              ns = ns:gsub("%s", "")
              if ns == "" then return end
              check(rex.block.call("com.superlogical.terminal", "write", { data = "kns " .. ns .. "\r" }))
            end }

-- run several rex8s list commands at once; their decoded JSON (nil when one
-- failed)
local function rex8s_json_all(lists)
  local hs = {}
  for i, l in ipairs(lists) do
    local argv = { K8S, "list" }
    for _, a in ipairs(l) do argv[#argv + 1] = a end
    hs[i] = rex.exec.start(argv)
  end
  local out = {}
  for i, h in ipairs(hs) do
    if h then
      local r = h:wait(25)
      if r and r.stdout then
        local ok, v = pcall(rex.json.decode, r.stdout)
        if ok and type(v) == "table" and not v.error then out[i] = v end
      end
    end
  end
  return out
end

-- palette: every cluster's health at a glance (failing pods, firing alerts,
-- Flux), then what to open for one of them — made for the iPhone too
rex.action{ name = "k8s_health", title = "Kubernetes: Cluster Health…", category = "Kubernetes",
            keywords = "k8s health status failing alerts flux overview incident kstatus",
            run = function(ctx, args)
              need_ui("kstatus")
              local res = rex8s_json_all({ { "failing" }, { "alerts" }, { "flux" } })
              local by, order = {}, {}
              local function entry(c)
                SHORT[c.context] = c.short
                if not by[c.context] then
                  by[c.context] = { short = c.short, prod = c.prod, failing = 0, alerts = 0, flux = 0 }
                  order[#order + 1] = c.context
                end
                local e = by[c.context]
                if c.loginExpired then e.expired = true end
                return e
              end
              for _, c in ipairs(res[1] or {}) do
                local e = entry(c)
                e.failing = #(c.pods or {})
                if c.error and not c.loginExpired then e.unreachable = true end
              end
              for _, c in ipairs(res[2] or {}) do
                entry(c).alerts = #(c.alerts or {}) -- the firing ones
              end
              for _, c in ipairs(res[3] or {}) do
                local e = entry(c)
                e.flux = (tonumber(c.failing) or 0)
                e.hasflux = c.installed
              end
              if #order == 0 then error("could not read any cluster", 0) end
              local items = {}
              for _, id in ipairs(order) do
                local e = by[id]
                local bits = {}
                if e.expired then bits[#bits + 1] = "login expired"
                elseif e.unreachable then bits[#bits + 1] = "unreachable" end
                if e.failing > 0 then bits[#bits + 1] = e.failing .. " failing" end
                if e.alerts > 0 then bits[#bits + 1] = e.alerts .. (e.alerts == 1 and " alert" or " alerts") end
                if e.flux > 0 then bits[#bits + 1] = e.flux .. " flux" end
                e.bad = #bits > 0
                e.summary = e.bad and table.concat(bits, " · ") or "ok"
                items[#items + 1] = { id = id, title = e.short, icon = "server",
                  subtitle = e.prod and "PROD" or nil, detail = e.summary }
              end
              table.sort(items, function(a, b)
                local ba, bb = by[a.id].bad, by[b.id].bad
                if ba ~= bb then return ba end
                return a.title < b.title
              end)
              local cluster = ask("pick", { title = "Cluster health", placeholder = "Filter clusters", items = items })
              if cluster == nil then return end
              local e = by[cluster]
              local acts = {}
              if e.expired then acts[#acts + 1] = { id = "login", title = "Log in again", subtitle = "kauth " .. e.short, icon = "person" } end
              acts[#acts + 1] = { id = "pods", title = "Pods", subtitle = e.failing > 0 and (e.failing .. " failing first") or nil, icon = "package" }
              acts[#acts + 1] = { id = "status", title = "Status", subtitle = "kstatus -w in a new tab", icon = "server" }
              acts[#acts + 1] = { id = "warnings", title = "Warning events", subtitle = "live, in a new tab", icon = "clock" }
              if e.hasflux then acts[#acts + 1] = { id = "flux", title = "Flux resources", subtitle = e.flux > 0 and (e.flux .. " failing first") or nil, icon = "branch" } end
              acts[#acts + 1] = { id = "incident", title = "Incident session", subtitle = "kincident " .. e.short, icon = "star" }
              local act = ask("pick", { title = e.short .. " · " .. e.summary, items = acts })
              if act == nil then return end
              if act == "login" then return show(ctx, check(kauth_window(cluster))) end
              if act == "pods" then return pods_on(ctx, cluster) end
              if act == "flux" then return flux_on(cluster) end
              if act == "incident" then return background({ K8S, "kincident", cluster }) end
              if act == "status" then
                return show(ctx, check(rex.session.new_window{ layout = term({ K8S, "kstatus", "-w", "--context", cluster }, "status") }))
              end
              return show(ctx, check(rex.session.new_window{ layout = term({ K8S, "_warnings", cluster }, "warnings") }))
            end }

rex.bind("shift+cmd+j", "k8s_pick")
rex.bind("shift+cmd+k", "k8s_pick_layout")
rex.bind("shift+cmd+m", "k8s_multi_pick")
rex.bind("shift+cmd+v", "k8s_view_all")
rex.bind("shift+cmd+i", "k8s_kview_toggle")
