import { Action, ActionPanel, Color, Icon, List, showToast, Toast } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ClusterDropdown, ErrorView, open, useClusters, useSelectedContext } from "./common";
import { FluxItem, list, reconcile } from "./rex";

const stateColor: Record<FluxItem["state"], Color> = {
  failing: Color.Red,
  suspended: Color.Orange,
  reconciling: Color.Yellow,
  ready: Color.Green,
};

const order: Record<FluxItem["state"], number> = { failing: 0, suspended: 1, reconciling: 2, ready: 3 };

export default function Command() {
  const clusters = useClusters();
  const [ctx, setCtx] = useSelectedContext(clusters.data);
  const cluster = clusters.data?.find((c) => c.context === ctx);
  const short = cluster?.short ?? ctx;
  const flux = useCachedPromise((c: string) => list<FluxItem[]>("flux", c), [ctx], {
    execute: !!ctx,
    keepPreviousData: false,
  });
  const items = [...(flux.data ?? [])].sort((a, b) => order[a.state] - order[b.state]);

  async function doReconcile(it: FluxItem) {
    const toast = await showToast({ style: Toast.Style.Animated, title: `Reconciling ${it.name}…` });
    try {
      const result = await reconcile(ctx, it);
      toast.style = result.startsWith("ready") ? Toast.Style.Success : Toast.Style.Failure;
      toast.title = `${it.name}: ${result}`;
      flux.revalidate();
    } catch (e) {
      toast.style = Toast.Style.Failure;
      toast.title = `Reconcile ${it.name} failed`;
      toast.message = e instanceof Error ? e.message : String(e);
    }
  }

  return (
    <List
      isLoading={clusters.isLoading || flux.isLoading}
      isShowingDetail={items.length > 0}
      searchBarPlaceholder={`Search Flux resources in ${short || "…"}`}
      searchBarAccessory={<ClusterDropdown clusters={clusters.data} value={ctx} onChange={setCtx} />}
    >
      {flux.error ? (
        <ErrorView error={flux.error} ctx={ctx} short={short} />
      ) : (
        items.map((it) => (
          <List.Item
            key={`${it.kind}/${it.namespace}/${it.name}`}
            title={it.name}
            keywords={[it.namespace, it.kind, it.state]}
            icon={{ source: Icon.CircleFilled, tintColor: stateColor[it.state] }}
            detail={
              <List.Item.Detail
                markdown={it.message ? "```\n" + it.message + "\n```" : undefined}
                metadata={
                  <List.Item.Detail.Metadata>
                    <List.Item.Detail.Metadata.Label title="Kind" text={it.kind} />
                    <List.Item.Detail.Metadata.Label title="Namespace" text={it.namespace} />
                    <List.Item.Detail.Metadata.TagList title="State">
                      <List.Item.Detail.Metadata.TagList.Item text={it.state} color={stateColor[it.state]} />
                    </List.Item.Detail.Metadata.TagList>
                    <List.Item.Detail.Metadata.Label title="Cluster" text={`${short}${cluster?.prod ? " (PROD)" : ""}`} />
                    {it.source && <List.Item.Detail.Metadata.Label title="Source" text={it.source} />}
                    {it.revision && <List.Item.Detail.Metadata.Label title="Applied" text={it.revision} />}
                  </List.Item.Detail.Metadata>
                }
              />
            }
            actions={
              <ActionPanel>
                <Action title="Reconcile Now" icon={Icon.ArrowClockwise} onAction={() => doReconcile(it)} />
                <Action
                  title="Open Shell in Namespace"
                  icon={Icon.Terminal}
                  shortcut={{ modifiers: ["cmd"], key: "t" }}
                  onAction={() => open("k8s_tab", { ctx, ns: it.namespace }, `Opening ${short}/${it.namespace}…`)}
                />
                <Action.CopyToClipboard title="Copy Name" content={it.name} />
                {it.message && <Action.CopyToClipboard title="Copy Message" content={it.message} />}
                <Action
                  title="Refresh"
                  icon={Icon.ArrowClockwise}
                  shortcut={{ modifiers: ["cmd"], key: "r" }}
                  onAction={() => flux.revalidate()}
                />
              </ActionPanel>
            }
          />
        ))
      )}
    </List>
  );
}
