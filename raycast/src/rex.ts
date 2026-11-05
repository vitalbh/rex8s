import { execFile } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { getPreferenceValues } from "@raycast/api";

type Prefs = { rexK8s?: string; rexApp?: string };

const expand = (p: string) => (p.startsWith("~/") ? homedir() + p.slice(1) : p);

function prefs() {
  const p = getPreferenceValues<Prefs>();
  return {
    rexK8s: expand(p.rexK8s || "~/.config/rex/rex8s/bin/rex8s"),
    rexApp: p.rexApp || "Rex Beta",
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

/** Runs a rex8s action in Rex (rex do NAME --args JSON) and brings Rex to the front. */
export async function rexDo(action: string, args: Record<string, unknown>): Promise<void> {
  await run(rexBin(), ["do", action, "--args", JSON.stringify(args)]);
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

/** Stops a running port-forward (its Rex pane closes). */
export async function stopPortForward(pid: number): Promise<void> {
  await run(prefs().rexK8s, ["kpf", "--stop", String(pid)]);
}
