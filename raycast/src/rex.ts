import { execFile } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { getPreferenceValues } from "@raycast/api";

type Prefs = { rexK8s?: string; rexApp?: string; rexSession?: string };

const expand = (p: string) => (p.startsWith("~/") ? homedir() + p.slice(1) : p);

function prefs() {
  const p = getPreferenceValues<Prefs>();
  return {
    rexK8s: expand(p.rexK8s || "~/.config/rex/rex8s/bin/rex8s"),
    rexApp: p.rexApp || "Rex Beta",
    rexSession: (p.rexSession || "k8s").trim(),
  };
}

const REX_CANDIDATES = [
  "/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
  "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
];

function rexBin(): string {
  return REX_CANDIDATES.find((p) => existsSync(p)) ?? "rex";
}

export type Cluster = {
  context: string;
  short: string;
  prod: boolean;
  server: string;
  namespace: string;
  user: string;
  current: boolean;
};

export type Pod = {
  namespace: string;
  name: string;
  status: string;
  badge: "ok" | "WAIT" | "FAIL" | "done";
  ready: number;
  total: number;
  restarts: number;
  age: string;
  containers: string[];
};

export type Service = {
  namespace: string;
  name: string;
  type: string;
  ports: { name: string; port: number }[];
};

export class ListError extends Error {
  constructor(
    message: string,
    public loginExpired: boolean,
    public cluster: string,
  ) {
    super(message);
  }
}

function run(bin: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(bin, args, { maxBuffer: 64 * 1024 * 1024, timeout: 30_000 }, (err, stdout, stderr) => {
      if (err) {
        // rex8s list reports errors as JSON on stdout
        try {
          const e = JSON.parse(stdout);
          if (e?.error) return reject(new ListError(e.error, !!e.loginExpired, e.cluster ?? ""));
        } catch {
          /* not JSON */
        }
        return reject(new Error((stderr || stdout || err.message).trim()));
      }
      resolve(stdout);
    });
  });
}

export async function list<T>(...args: string[]): Promise<T> {
  return JSON.parse(await run(prefs().rexK8s, ["list", ...args])) as T;
}

/** Sets the machine's default context (current-context of the master kubeconfig). */
export async function useContext(ctx: string): Promise<void> {
  await run(prefs().rexK8s, ["use-context", ctx]);
}

/**
 * The session Raycast opens tabs in (preference, label or ID). Rex can't tell
 * which session the app shows, and new_window needs one when there are
 * several, so tabs go to a known session, created when missing.
 */
async function rexSession(): Promise<string> {
  const want = prefs().rexSession;
  const { sessions } = JSON.parse(await run(rexBin(), ["ls", "--json"])) as {
    sessions: { session_id: string; label?: string }[];
  };
  const found = sessions.find((s) => s.session_id === want || s.label === want);
  if (found) return found.session_id;
  const created = JSON.parse(await run(rexBin(), ["new", want, "--json"])) as { session_id: string };
  return created.session_id;
}

/** The Rex app's client ID (kind "app"), to send it client actions directly. */
async function appClient(): Promise<string | undefined> {
  const out = JSON.parse(await run(rexBin(), ["do", "-e", 'return rex.call("client.list")', "--json"])) as {
    clients: { client_id: string; info?: { kind?: string } }[];
  };
  return out.clients.find((c) => c.info?.kind === "app")?.client_id;
}

/**
 * Runs a rex8s action in Rex (rex do NAME --args JSON) in the Raycast session,
 * then has the app show that session on the tab the action opened.
 */
export async function rexDo(
  action: string,
  args: Record<string, unknown>,
  opts: { show?: boolean } = {},
): Promise<void> {
  const session = await rexSession();
  const out = await run(rexBin(), ["-s", session, "do", action, "--args", JSON.stringify(args), "--json"]);
  let windowId: string | undefined;
  try {
    windowId = (JSON.parse(out) as { window_id?: string })?.window_id ?? undefined;
  } catch {
    /* an action without a result */
  }
  if (opts.show === false) return; // runs in the background (e.g. a port-forward)
  const client = await appClient().catch(() => undefined);
  const select = ["do", "session.select", `session_id=${session}`];
  if (windowId) select.push(`window_id=${windowId}`);
  await run(rexBin(), client ? ["-C", client, ...select] : select).catch(() => undefined);
  await run("/usr/bin/open", ["-a", prefs().rexApp]).catch(() => undefined);
}

export type FluxItem = {
  kind: string;
  namespace: string;
  name: string;
  state: "ready" | "failing" | "suspended" | "reconciling";
  message: string;
  revision: string;
  source: string;
};

export type ClusterHealth = {
  context: string;
  short: string;
  prod: boolean;
  error?: string;
  loginExpired?: boolean;
  installed: boolean;
  ready: number;
  failing: number;
  reconciling: number;
  suspended: number;
  problems: FluxItem[];
};

/** Asks Flux to reconcile and waits for the result; returns rex8s's last line. */
export async function reconcile(ctx: string, it: FluxItem): Promise<string> {
  // eslint-disable-next-line no-control-regex
  const out = (await run(prefs().rexK8s, ["freconcile", ctx, it.kind, it.namespace, it.name])).replace(/\x1b\[[0-9;]*m/g, "");
  return out.trim().split("\n").pop() ?? "";
}

export type ClusterFailing = {
  context: string;
  short: string;
  prod: boolean;
  error?: string;
  loginExpired?: boolean;
  pods: Pod[];
};

export type PortForward = {
  pid: number;
  context: string;
  short: string;
  prod: boolean;
  namespace: string;
  service: string;
  local: number;
  remote: number;
  started: string;
};

/** Saves a running port-forward as a favorite (config forwards); returns its name. */
export async function saveFavorite(pid: number): Promise<string> {
  return (await run(prefs().rexK8s, ["kpf", "--save", String(pid)])).trim();
}

/** Stops a running port-forward (its Rex pane closes). */
export async function stopPortForward(pid: number): Promise<void> {
  await run(prefs().rexK8s, ["kpf", "--stop", String(pid)]);
}

export type FavoriteForward = {
  name: string;
  cluster: string;
  prod: boolean;
  namespace: string;
  service: string;
  port: number;
  running: boolean;
  url?: string;
};

export type Workload = {
  context: string;
  short: string;
  prod: boolean;
  kind: string;
  namespace: string;
  name: string;
  selector: string;
  image: string;
  tag: string;
  spec: string;
  target: string;
};

export type ClusterAlerts = {
  context: string;
  short: string;
  prod: boolean;
  error?: string;
  loginExpired?: boolean;
  source: string;
  noRules?: boolean;
  alerts: { name: string; severity: string; summary: string; where: string }[];
};
