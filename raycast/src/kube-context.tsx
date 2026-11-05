import { Clipboard, Color, Icon, MenuBarExtra, open, showHUD } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { Cluster, list, PortForward, rexDo, stopPortForward, useContext } from "./rex";

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(() => list<Cluster[]>("clusters"), [], {
    keepPreviousData: true,
  });
  const clusters = data ?? [];
  const pf = useCachedPromise(() => list<PortForward[]>("pf"), [], { keepPreviousData: true });
  const forwards = pf.data ?? [];
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
      {forwards.length > 0 && (
        <MenuBarExtra.Section title="Port-forwards">
          {forwards.map((f) => (
            <MenuBarExtra.Submenu
              key={f.pid}
              icon={{ source: Icon.Plug, tintColor: f.prod ? Color.Red : Color.Green }}
              title={`localhost:${f.local} → ${f.short} ${f.namespace}/${f.service}:${f.remote}`}
            >
              <MenuBarExtra.Item title="Open in Browser" icon={Icon.Globe} onAction={() => open(`http://localhost:${f.local}`)} />
              <MenuBarExtra.Item
                title="Copy URL"
                icon={Icon.Clipboard}
                onAction={async () => {
                  await Clipboard.copy(`http://localhost:${f.local}`);
                  await showHUD(`Copied http://localhost:${f.local}`);
                }}
              />
              <MenuBarExtra.Item
                title="Stop"
                icon={Icon.Stop}
                onAction={async () => {
                  await stopPortForward(f.pid);
                  await showHUD(`Stopped ${f.service} (${f.short})`);
                  pf.revalidate();
                }}
              />
            </MenuBarExtra.Submenu>
          ))}
        </MenuBarExtra.Section>
      )}
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
          onAction={() => {
            revalidate();
            pf.revalidate();
          }}
        />
      </MenuBarExtra.Section>
    </MenuBarExtra>
  );
}
