import { Action, ActionPanel, Color, Icon, List } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ClusterDropdown, ErrorView, open, prodTag, useClusters, useSelectedContext } from "./common";
import { list, Pod } from "./rex";

const badgeColor: Record<Pod["badge"], Color> = {
  FAIL: Color.Red,
  WAIT: Color.Orange,
  ok: Color.Green,
  done: Color.SecondaryText,
};

export default function Command() {
  const clusters = useClusters();
  const [ctx, setCtx] = useSelectedContext(clusters.data);
  const cluster = clusters.data?.find((c) => c.context === ctx);
  const short = cluster?.short ?? ctx;
  const pods = useCachedPromise((c: string) => list<Pod[]>("pods", c), [ctx], {
    execute: !!ctx,
    keepPreviousData: false,
  });

  return (
    <List
      isLoading={clusters.isLoading || pods.isLoading}
      searchBarPlaceholder={`Search pods in ${short || "…"}`}
      searchBarAccessory={<ClusterDropdown clusters={clusters.data} value={ctx} onChange={setCtx} />}
    >
      {pods.error ? (
        <ErrorView error={pods.error} ctx={ctx} short={short} />
      ) : (
        (pods.data ?? []).map((p) => (
          <List.Item
            key={`${p.namespace}/${p.name}`}
            title={p.name}
            subtitle={p.namespace}
            keywords={[p.namespace, p.status]}
            icon={{ source: Icon.CircleFilled, tintColor: badgeColor[p.badge] }}
            accessories={[
              { tag: { value: p.status, color: badgeColor[p.badge] } },
              { text: `${p.ready}/${p.total}`, tooltip: "ready containers" },
              ...(p.restarts > 0 ? [{ text: `↻ ${p.restarts}`, tooltip: "restarts" }] : []),
              { text: p.age, tooltip: "age" },
              ...prodTag(cluster),
            ]}
            actions={
              <ActionPanel>
                <Action
                  title="Follow Logs"
                  icon={Icon.Document}
                  onAction={() =>
                    open("k8s_logs", { ctx, ns: p.namespace, pod: p.name }, `Opening logs of ${p.name}…`)
                  }
                />
                {p.containers.length > 1 && (
                  <ActionPanel.Submenu title="Follow Logs of Container…" icon={Icon.Layers}>
                    <Action
                      title="All Containers"
                      onAction={() =>
                        open("k8s_logs", { ctx, ns: p.namespace, pod: p.name, container: "*" }, "Opening logs…")
                      }
                    />
                    {p.containers.map((cn) => (
                      <Action
                        key={cn}
                        title={cn}
                        onAction={() =>
                          open("k8s_logs", { ctx, ns: p.namespace, pod: p.name, container: cn }, "Opening logs…")
                        }
                      />
                    ))}
                  </ActionPanel.Submenu>
                )}
                <Action
                  title="Explain with Claude"
                  icon={Icon.Stars}
                  shortcut={{ modifiers: ["cmd"], key: "e" }}
                  onAction={() => open("k8s_why", { ctx, ns: p.namespace, pod: p.name }, `Asking Claude about ${p.name}…`)}
                />
                <Action
                  title="Shell into Container"
                  icon={Icon.Terminal}
                  shortcut={{ modifiers: ["cmd"], key: "s" }}
                  onAction={() => open("k8s_exec", { ctx, ns: p.namespace, pod: p.name }, `Opening a shell in ${p.name}…`)}
                />
                <Action
                  title="Open Shell in Namespace"
                  icon={Icon.Terminal}
                  shortcut={{ modifiers: ["cmd"], key: "t" }}
                  onAction={() => open("k8s_tab", { ctx, ns: p.namespace }, `Opening ${short}/${p.namespace}…`)}
                />
                <Action.CopyToClipboard title="Copy Pod Name" content={p.name} />
                <Action.CopyToClipboard
                  title="Copy Logs Command"
                  content={`kubectl --context ${JSON.stringify(ctx)} -n ${p.namespace} logs -f ${p.name}`}
                />
                <Action
                  title="Refresh"
                  icon={Icon.ArrowClockwise}
                  shortcut={{ modifiers: ["cmd"], key: "r" }}
                  onAction={() => pods.revalidate()}
                />
              </ActionPanel>
            }
          />
        ))
      )}
    </List>
  );
}
