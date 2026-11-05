import { Action, ActionPanel, Icon, List } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ClusterDropdown, ErrorView, open, prodTag, useClusters, useSelectedContext } from "./common";
import { list, Service } from "./rex";

export default function Command() {
  const clusters = useClusters();
  const [ctx, setCtx] = useSelectedContext(clusters.data);
  const cluster = clusters.data?.find((c) => c.context === ctx);
  const short = cluster?.short ?? ctx;
  const svcs = useCachedPromise((c: string) => list<Service[]>("services", c), [ctx], {
    execute: !!ctx,
    keepPreviousData: false,
  });

  const forward = (s: Service, port: number) =>
    open("k8s_pf", { ctx, ns: s.namespace, svc: s.name, port }, `Port-forwarding ${s.name}:${port}…`);

  return (
    <List
      isLoading={clusters.isLoading || svcs.isLoading}
      searchBarPlaceholder={`Search services in ${short || "…"}`}
      searchBarAccessory={<ClusterDropdown clusters={clusters.data} value={ctx} onChange={setCtx} />}
    >
      {svcs.error ? (
        <ErrorView error={svcs.error} ctx={ctx} short={short} />
      ) : (
        (svcs.data ?? []).map((s) => (
          <List.Item
            key={`${s.namespace}/${s.name}`}
            title={s.name}
            subtitle={s.namespace}
            keywords={[s.namespace, ...s.ports.map((p) => String(p.port))]}
            icon={Icon.Plug}
            accessories={[
              { text: s.ports.map((p) => (p.name ? `${p.port}/${p.name}` : String(p.port))).join("  ") },
              { tag: s.type },
              ...prodTag(cluster),
            ]}
            actions={
              <ActionPanel>
                <Action
                  title={`Port-forward ${s.ports[0].port}`}
                  icon={Icon.Plug}
                  onAction={() => forward(s, s.ports[0].port)}
                />
                {s.ports.length > 1 && (
                  <ActionPanel.Submenu title="Port-forward Port…" icon={Icon.List}>
                    {s.ports.map((p) => (
                      <Action
                        key={p.port}
                        title={p.name ? `${p.port} (${p.name})` : String(p.port)}
                        onAction={() => forward(s, p.port)}
                      />
                    ))}
                  </ActionPanel.Submenu>
                )}
                <Action
                  title="Open Shell in Namespace"
                  icon={Icon.Terminal}
                  shortcut={{ modifiers: ["cmd"], key: "t" }}
                  onAction={() => open("k8s_tab", { ctx, ns: s.namespace }, `Opening ${short}/${s.namespace}…`)}
                />
                <Action.CopyToClipboard title="Copy Service DNS" content={`${s.name}.${s.namespace}.svc.cluster.local`} />
                <Action
                  title="Refresh"
                  icon={Icon.ArrowClockwise}
                  shortcut={{ modifiers: ["cmd"], key: "r" }}
                  onAction={() => svcs.revalidate()}
                />
              </ActionPanel>
            }
          />
        ))
      )}
    </List>
  );
}
