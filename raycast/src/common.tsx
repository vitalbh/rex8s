import { Action, ActionPanel, Color, Icon, List, showToast, Toast } from "@raycast/api";
import { useCachedPromise, useCachedState } from "@raycast/utils";
import { Cluster, list, ListError, rexDo } from "./rex";

export function useClusters() {
  return useCachedPromise(() => list<Cluster[]>("clusters"), [], { keepPreviousData: true });
}

/** Remembers the selected cluster across commands. */
export function useSelectedContext(clusters: Cluster[] | undefined) {
  const [ctx, setCtx] = useCachedState<string>("rex8s-context", "");
  const valid = clusters?.some((c) => c.context === ctx);
  return [valid ? ctx : (clusters?.[0]?.context ?? ""), setCtx] as const;
}

export function ClusterDropdown(props: { clusters?: Cluster[]; value: string; onChange: (ctx: string) => void }) {
  return (
    <List.Dropdown tooltip="Cluster" value={props.value} onChange={props.onChange}>
      {(props.clusters ?? []).map((c) => (
        <List.Dropdown.Item
          key={c.context}
          value={c.context}
          title={`${c.short}${c.prod ? "  (PROD)" : ""}`}
          icon={{ source: Icon.CircleFilled, tintColor: c.prod ? Color.Red : Color.Green }}
        />
      ))}
    </List.Dropdown>
  );
}

export function prodTag(c?: Cluster): List.Item.Accessory[] {
  if (!c) return [];
  return [c.prod ? { tag: { value: "PROD", color: Color.Red } } : { tag: { value: "dev", color: Color.Green } }];
}

/** Runs a Rex action, reporting failures as a toast. */
export async function open(action: string, args: Record<string, unknown>, title: string) {
  const toast = await showToast({ style: Toast.Style.Animated, title });
  try {
    await rexDo(action, args);
    toast.style = Toast.Style.Success;
  } catch (e) {
    toast.style = Toast.Style.Failure;
    toast.title = "Rex: " + title;
    toast.message = e instanceof Error ? e.message : String(e);
  }
}

export function LoginAction({ ctx, short }: { ctx: string; short: string }) {
  return (
    <Action
      title={`Log in to ${short}`}
      icon={Icon.Key}
      onAction={() => open("k8s_kauth", { ctx }, `Opening login for ${short}…`)}
    />
  );
}

/** Empty view for a failed listing; offers to log in when the login expired. */
export function ErrorView({ error, ctx, short }: { error: Error; ctx: string; short: string }) {
  const expired = error instanceof ListError && error.loginExpired;
  return (
    <List.EmptyView
      icon={expired ? Icon.Key : Icon.ExclamationMark}
      title={expired ? `Login expired on ${short}` : `Can't reach ${short}`}
      description={expired ? "Press enter to log in again (opens a Rex tab running kauth)." : error.message}
      actions={expired ? <ActionPanel>{<LoginAction ctx={ctx} short={short} />}</ActionPanel> : undefined}
    />
  );
}
