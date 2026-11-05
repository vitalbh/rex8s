import {
  Action,
  ActionPanel,
  Alert,
  Color,
  confirmAlert,
  Form,
  Icon,
  List,
  useNavigation,
} from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { open } from "./common";
import { list, Workload } from "./rex";

type Group = { name: string; items: Workload[] };

function groupByName(ws: Workload[]): Group[] {
  const by = new Map<string, Workload[]>();
  for (const w of ws) by.set(w.name, [...(by.get(w.name) ?? []), w]);
  return [...by.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, items]) => ({ name, items }));
}

const kindShort = (k: string) => ({ Deployment: "deploy", StatefulSet: "sts", DaemonSet: "ds" })[k] ?? k;

function differs(g: Group) {
  return new Set(g.items.map((w) => w.tag)).size > 1;
}

function detail(g: Group) {
  const rows = g.items
    .map((w) => `| ${w.short}${w.prod ? " **PROD**" : ""} | ${kindShort(w.kind)} ${w.namespace}/${w.name} | \`${w.tag}\` |`)
    .join("\n");
  return `## ${g.name}\n\n| cluster | workload | version |\n|---|---|---|\n${rows}\n\n${
    differs(g) ? "⚠️ versions differ between clusters" : ""
  }\n\nimage: \`${g.items[0].image}\``;
}

function logs(items: Workload[], grep?: string) {
  const label = grep ? `grep ${items[0].name}` : `logs ${items[0].name}`;
  return open(
    "k8s_wlogs",
    { specs: items.map((w) => w.spec).join("\n"), grep: grep ?? "", label },
    `Following ${items[0].name} on ${items.map((w) => w.short).join(", ")}…`,
  );
}

async function restart(items: Workload[]) {
  const prod = items.filter((w) => w.prod).map((w) => w.short);
  const ok = await confirmAlert({
    title: `Restart ${items[0].name}?`,
    message: `rollout restart on ${items.map((w) => w.short).join(", ")}${
      prod.length ? ` — production: ${prod.join(", ")} (the Rex tab asks again)` : ""
    }`,
    primaryAction: { title: "Restart", style: Alert.ActionStyle.Destructive },
  });
  if (!ok) return;
  await open(
    "k8s_roll",
    { targets: items.map((w) => w.target).join("\n"), label: `restart ${items[0].name}` },
    `Restarting ${items[0].name}…`,
  );
}

function GrepForm({ items }: { items: Workload[] }) {
  const { pop } = useNavigation();
  return (
    <Form
      navigationTitle={`Search the logs of ${items[0].name}`}
      actions={
        <ActionPanel>
          <Action.SubmitForm
            title="Search Logs"
            icon={Icon.MagnifyingGlass}
            onSubmit={async (v: { grep: string }) => {
              if (!v.grep.trim()) return;
              pop();
              await logs(items, v.grep.trim());
            }}
          />
        </ActionPanel>
      }
    >
      <Form.TextField id="grep" title="Pattern" placeholder="error|timeout (regex, case-insensitive)" />
      <Form.Description text={`On ${items.map((w) => w.short).join(", ")} · keeps following: new matches keep coming`} />
    </Form>
  );
}

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(() => list<Workload[]>("workloads"), [], {
    keepPreviousData: true,
  });
  const groups = groupByName(data ?? []);
  return (
    <List isLoading={isLoading} isShowingDetail searchBarPlaceholder="Search workloads on every cluster">
      {groups.map((g) => (
        <List.Item
          key={g.name}
          title={g.name}
          subtitle={[...new Set(g.items.map((w) => w.namespace))].join(", ")}
          icon={{ source: Icon.Box, tintColor: differs(g) ? Color.Orange : undefined }}
          keywords={g.items.flatMap((w) => [w.namespace, w.short, w.tag])}
          detail={<List.Item.Detail markdown={detail(g)} />}
          actions={
            <ActionPanel>
              <Action title="Follow Logs (All Pods)" icon={Icon.Text} onAction={() => logs(g.items)} />
              <Action.Push
                title="Search Logs…"
                icon={Icon.MagnifyingGlass}
                shortcut={{ modifiers: ["cmd"], key: "g" }}
                target={<GrepForm items={g.items} />}
              />
              {g.items.length > 1 && (
                <ActionPanel.Submenu title="Follow Logs on One Cluster…" icon={Icon.Text}>
                  {g.items.map((w) => (
                    <Action key={w.context} title={`${w.short}${w.prod ? " (PROD)" : ""}`} onAction={() => logs([w])} />
                  ))}
                </ActionPanel.Submenu>
              )}
              <ActionPanel.Submenu
                title="Restart…"
                icon={Icon.ArrowClockwise}
                shortcut={{ modifiers: ["cmd", "shift"], key: "r" }}
              >
                {g.items.length > 1 && <Action title="On Every Cluster" onAction={() => restart(g.items)} />}
                {g.items.map((w) => (
                  <Action key={w.context} title={`${w.short}${w.prod ? " (PROD)" : ""}`} onAction={() => restart([w])} />
                ))}
              </ActionPanel.Submenu>
              <Action.CopyToClipboard title="Copy Image" content={g.items[0].image} />
              <Action
                title="Refresh"
                icon={Icon.ArrowClockwise}
                shortcut={{ modifiers: ["cmd"], key: "r" }}
                onAction={() => revalidate()}
              />
            </ActionPanel>
          }
        />
      ))}
    </List>
  );
}
