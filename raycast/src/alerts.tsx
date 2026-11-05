import { Color, Icon, MenuBarExtra } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { ClusterAlerts, list, rexDo } from "./rex";

const critical = (s: string) => ["critical", "page"].includes(s.toLowerCase());
const warning = (s: string) => ["warning", "error", "high", "medium"].includes(s.toLowerCase());

function alertIcon(severity: string) {
  if (critical(severity)) return { source: Icon.ExclamationMark, tintColor: Color.Red };
  if (warning(severity)) return { source: Icon.Warning, tintColor: Color.Orange };
  return { source: Icon.Info, tintColor: Color.SecondaryText };
}

function emptyText(c: ClusterAlerts) {
  if (c.loginExpired) return "login expired";
  if (c.error) return "could not read alerts";
  if (!c.source) return "no Alertmanager or Prometheus in the cluster";
  if (c.noRules) return "Prometheus has no alerting rules";
  return "nothing firing";
}

export default function Command() {
  const { data, isLoading, revalidate } = useCachedPromise(() => list<ClusterAlerts[]>("alerts", "--notify"), [], {
    keepPreviousData: true,
  });
  const clusters = data ?? [];
  const all = clusters.flatMap((c) => c.alerts);
  const crit = all.filter((a) => critical(a.severity)).length;
  const warn = all.filter((a) => warning(a.severity)).length;
  const tint = crit > 0 ? Color.Red : warn > 0 ? Color.Orange : Color.Green;
  return (
    <MenuBarExtra
      isLoading={isLoading}
      icon={{ source: crit + warn > 0 ? Icon.Bell : Icon.BellDisabled, tintColor: tint }}
      title={crit + warn > 0 ? String(crit || warn) : undefined}
      tooltip={`${crit} critical · ${warn} warning`}
    >
      {clusters.map((c) => (
        <MenuBarExtra.Section key={c.context} title={`${c.short}${c.prod ? " · PROD" : ""}${c.source ? ` · ${c.source}` : ""}`}>
          {c.alerts.length === 0 ? (
            <MenuBarExtra.Item
              icon={c.error ? { source: Icon.QuestionMarkCircle, tintColor: Color.Orange } : Icon.Circle}
              title={emptyText(c)}
              tooltip={c.error}
              onAction={() =>
                c.loginExpired ? rexDo("k8s_kauth", { ctx: c.context }) : rexDo("k8s_status", { ctx: c.context })
              }
            />
          ) : (
            c.alerts.slice(0, 12).map((a, i) => (
              <MenuBarExtra.Item
                key={`${a.name}-${a.where}-${i}`}
                icon={alertIcon(a.severity)}
                title={a.name}
                subtitle={[a.where, a.summary.length > 50 ? a.summary.slice(0, 49) + "…" : a.summary].filter(Boolean).join(" · ")}
                tooltip={a.summary}
                onAction={() => rexDo("k8s_status", { ctx: c.context })}
              />
            ))
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
