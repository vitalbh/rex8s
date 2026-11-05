import { Color, Icon, MenuBarExtra, showHUD } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { Cluster, list, rexDo, useContext } from "./rex";

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(() => list<Cluster[]>("clusters"), [], {
    keepPreviousData: true,
  });
  const clusters = data ?? [];
  const current = clusters.find((c) => c.current);
  // aliases of the same cluster share a short name: show the context to tell them apart
  const dup = (c: Cluster) => clusters.filter((o) => o.short === c.short).length > 1;

  async function choose(c: Cluster) {
    await useContext(c.context);
    await showHUD(`Default context: ${c.short}${c.prod ? " (PROD)" : ""}`);
    revalidate();
  }

  return (
    <MenuBarExtra
      isLoading={isLoading}
      icon={{ source: Icon.Globe, tintColor: current?.prod ? Color.Red : undefined }}
      title={current ? `${current.prod ? "PROD " : ""}${current.short}` : "no context"}
      tooltip={current ? `kubectl default context: ${current.context}` : "No current-context in the kubeconfig"}
    >
      <MenuBarExtra.Section title="Default context (outside rex8s panes)">
        {clusters.map((c) => (
          <MenuBarExtra.Item
            key={c.context}
            icon={c.current ? { source: Icon.CheckCircle, tintColor: c.prod ? Color.Red : Color.Green } : Icon.Circle}
            title={c.short}
            subtitle={[c.prod ? "PROD" : "", dup(c) ? c.context : ""].filter(Boolean).join(" · ") || undefined}
            tooltip={c.context}
            onAction={() => choose(c)}
          />
        ))}
      </MenuBarExtra.Section>
      {current && (
        <MenuBarExtra.Section>
          <MenuBarExtra.Item
            title={`Open ${current.short} in Rex`}
            icon={Icon.Terminal}
            onAction={() => rexDo("k8s_tab", { ctx: current.context })}
          />
        </MenuBarExtra.Section>
      )}
      <MenuBarExtra.Section>
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
