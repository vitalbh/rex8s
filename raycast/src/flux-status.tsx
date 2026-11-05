import { Color, Icon, launchCommand, LaunchType, MenuBarExtra } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ClusterHealth, list, rexDo } from "./rex";

function clusterIcon(c: ClusterHealth) {
  if (c.error) return { source: Icon.QuestionMarkCircle, tintColor: Color.Orange };
  if (c.failing > 0) return { source: Icon.XMarkCircle, tintColor: Color.Red };
  return { source: Icon.CheckCircle, tintColor: Color.Green };
}

function summary(c: ClusterHealth) {
  if (c.loginExpired) return "login expired";
  if (c.error) return "no access";
  if (!c.installed) return "flux not installed";
  const parts = [`${c.ready} ready`];
  if (c.failing) parts.push(`${c.failing} failing`);
  if (c.reconciling) parts.push(`${c.reconciling} reconciling`);
  if (c.suspended) parts.push(`${c.suspended} suspended`);
  return parts.join(" · ");
}

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(() => list<ClusterHealth[]>("flux"), [], {
    keepPreviousData: true,
  });
  const clusters = data ?? [];
  const failing = clusters.reduce((n, c) => n + c.failing, 0);
  const unreachable = clusters.filter((c) => c.error).length;

  const tint = failing > 0 ? Color.Red : unreachable > 0 ? Color.Orange : Color.Green;
  return (
    <MenuBarExtra
      isLoading={isLoading}
      icon={{ source: failing > 0 ? Icon.XMarkCircleFilled : Icon.CheckCircle, tintColor: tint }}
      title={failing > 0 ? String(failing) : undefined}
      tooltip={failing > 0 ? `${failing} Flux resource(s) failing` : "Flux healthy"}
    >
      {clusters.map((c) => (
        <MenuBarExtra.Section key={c.context} title={`${c.short}${c.prod ? " · PROD" : ""}`}>
          <MenuBarExtra.Item
            icon={clusterIcon(c)}
            title={summary(c)}
            onAction={() =>
              c.loginExpired
                ? rexDo("k8s_kauth", { ctx: c.context })
                : rexDo("k8s_tab", { ctx: c.context })
            }
          />
          {c.problems.map((p) => (
            <MenuBarExtra.Item
              key={`${p.kind}/${p.namespace}/${p.name}`}
              icon={{ source: Icon.XMarkCircle, tintColor: Color.Red }}
              title={`${p.kind} ${p.namespace}/${p.name}`}
              subtitle={p.message.length > 60 ? p.message.slice(0, 59) + "…" : p.message}
              tooltip={p.message}
              onAction={() => rexDo("k8s_tab", { ctx: c.context, ns: p.namespace })}
            />
          ))}
        </MenuBarExtra.Section>
      ))}
      <MenuBarExtra.Section>
        <MenuBarExtra.Item title="Open Flux Monitor in Rex" icon={Icon.Monitor} onAction={() => rexDo("k8s_flux_monitor", {})} />
        <MenuBarExtra.Item
          title="Search Flux…"
          icon={Icon.MagnifyingGlass}
          onAction={() => launchCommand({ name: "search-flux", type: LaunchType.UserInitiated })}
        />
        <MenuBarExtra.Item
          title="Refresh"
          icon={Icon.ArrowClockwise}
          shortcut={{ modifiers: ["cmd"], key: "r" }}
          onAction={() => revalidate()}
        />
      </MenuBarExtra.Section>
    </MenuBarExtra>
  );
}
