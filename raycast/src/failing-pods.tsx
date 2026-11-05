import { Color, Icon, MenuBarExtra } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ClusterFailing, list, Pod, rexDo } from "./rex";

const MAX_PODS = 8; // per cluster; the rest is one "open kfail" item

function podIcon(p: Pod) {
  return p.badge === "FAIL"
    ? { source: Icon.XMarkCircle, tintColor: Color.Red }
    : { source: Icon.Clock, tintColor: Color.Orange };
}

function podSubtitle(p: Pod) {
  const parts = [p.status];
  if (p.restarts > 0) parts.push(`${p.restarts} restarts`);
  return parts.join(" · ");
}

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(() => list<ClusterFailing[]>("failing"), [], {
    keepPreviousData: true,
  });
  const clusters = data ?? [];
  const total = clusters.reduce((n, c) => n + c.pods.length, 0);
  const prodFailing = clusters.some((c) => c.prod && c.pods.some((p) => p.badge === "FAIL"));
  const unreachable = clusters.filter((c) => c.error).length;

  const tint = total === 0 ? (unreachable ? Color.Orange : Color.Green) : prodFailing ? Color.Red : Color.Orange;
  return (
    <MenuBarExtra
      isLoading={isLoading}
      icon={{ source: total > 0 ? Icon.Heartbeat : Icon.CheckCircle, tintColor: tint }}
      title={total > 0 ? String(total) : undefined}
      tooltip={total > 0 ? `${total} pod(s) failing` : "No failing pods"}
    >
      {clusters.map((c) => (
        <MenuBarExtra.Section key={c.context} title={`${c.short}${c.prod ? " · PROD" : ""}`}>
          {c.error ? (
            <MenuBarExtra.Item
              icon={{ source: Icon.QuestionMarkCircle, tintColor: Color.Orange }}
              title={c.loginExpired ? "login expired" : "no access"}
              tooltip={c.error}
              onAction={() => (c.loginExpired ? rexDo("k8s_kauth", { ctx: c.context }) : undefined)}
            />
          ) : c.pods.length === 0 ? (
            <MenuBarExtra.Item icon={{ source: Icon.CheckCircle, tintColor: Color.Green }} title="all pods healthy" />
          ) : (
            [
              ...c.pods.slice(0, MAX_PODS).map((p) => (
                <MenuBarExtra.Item
                  key={`${p.namespace}/${p.name}`}
                  icon={podIcon(p)}
                  title={`${p.namespace}/${p.name}`}
                  subtitle={podSubtitle(p)}
                  tooltip="Open kfail in Rex on this pod"
                  onAction={() => rexDo("k8s_fail", { ctx: c.context, filter: p.name })}
                />
              )),
              <MenuBarExtra.Item
                key="all"
                icon={Icon.List}
                title={c.pods.length > MAX_PODS ? `All ${c.pods.length} in kfail…` : "Open kfail…"}
                onAction={() => rexDo("k8s_fail", { ctx: c.context })}
              />,
            ]
          )}
        </MenuBarExtra.Section>
      ))}
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
