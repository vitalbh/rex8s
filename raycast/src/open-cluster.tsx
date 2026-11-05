import { Action, ActionPanel, Icon, List } from "@raycast/api";
import { usePromise } from "@raycast/utils";
import { ErrorView, LoginAction, open, prodTag, useClusters } from "./common";
import { Cluster, list } from "./rex";

function OpenActions({ c, ns }: { c: Cluster; ns?: string }) {
  const args = ns ? { ctx: c.context, ns } : { ctx: c.context };
  const where = ns ? `${c.short}/${ns}` : c.short;
  return (
    <>
      <Action title="Open Shell" icon={Icon.Terminal} onAction={() => open("k8s_tab", args, `Opening ${where}…`)} />
      <Action
        title="Open K9s Layout"
        icon={Icon.AppWindowGrid3x3}
        shortcut={{ modifiers: ["cmd"], key: "l" }}
        onAction={() => open("k8s_layout", args, `Opening ${where} layout…`)}
      />
    </>
  );
}

function Namespaces({ c }: { c: Cluster }) {
  const { data, isLoading, error } = usePromise(() => list<string[]>("namespaces", c.context));
  return (
    <List isLoading={isLoading} navigationTitle={`Namespaces in ${c.short}`} searchBarPlaceholder="Filter namespaces">
      {error ? (
        <ErrorView error={error} ctx={c.context} short={c.short} />
      ) : (
        (data ?? []).map((ns) => (
          <List.Item
            key={ns}
            title={ns}
            icon={Icon.Folder}
            accessories={prodTag(c)}
            actions={
              <ActionPanel>
                <OpenActions c={c} ns={ns} />
              </ActionPanel>
            }
          />
        ))
      )}
    </List>
  );
}

export default function Command() {
  const { data, isLoading, error } = useClusters();
  return (
    <List isLoading={isLoading} searchBarPlaceholder="Open a cluster in Rex">
      {error && <List.EmptyView icon={Icon.ExclamationMark} title="rex8s failed" description={error.message} />}
      {(data ?? []).map((c) => (
        <List.Item
          key={c.context}
          title={c.short}
          subtitle={c.context}
          keywords={[c.context, c.server]}
          icon={Icon.Cloud}
          accessories={[...(c.namespace ? [{ text: c.namespace }] : []), ...prodTag(c)]}
          actions={
            <ActionPanel>
              <OpenActions c={c} />
              <Action.Push
                title="Choose Namespace…"
                icon={Icon.Folder}
                shortcut={{ modifiers: ["cmd"], key: "n" }}
                target={<Namespaces c={c} />}
              />
              <LoginAction ctx={c.context} short={c.short} />
              <Action.CopyToClipboard title="Copy Context Name" content={c.context} />
            </ActionPanel>
          }
        />
      ))}
    </List>
  );
}
