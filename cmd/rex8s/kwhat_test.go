package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRolloutChanges(t *testing.T) {
	now := time.Now()
	rs := func(name string, rev int, ago time.Duration, img string) string {
		return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"api","creationTimestamp":%q,
		  "annotations":{"deployment.kubernetes.io/revision":"%d"},"ownerReferences":[{"kind":"Deployment","name":"api"}]},
		  "spec":{"template":{"spec":{"containers":[{"name":"api","image":%q}]}}}}`, name, now.Add(-ago).UTC().Format(time.RFC3339), rev, img)
	}
	data := `{"items":[` + rs("api-1", 11, 48*time.Hour, "acme/api:1.41.0") + "," + rs("api-2", 12, 20*time.Minute, "acme/api:1.42.0") + `]}`
	cs := rolloutChanges([]byte(data), now.Add(-time.Hour))
	if len(cs) != 1 || cs[0].what != "deploy api/api" || cs[0].detail != "rev 12 · api 1.41.0 → 1.42.0" {
		t.Fatalf("changes = %+v", cs)
	}
}

func TestFluxChanges(t *testing.T) {
	ts := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	ev := func(typ, kind, reason, msg string) string {
		b, _ := json.Marshal(msg)
		return fmt.Sprintf(`{"type":%q,"reason":%q,"message":%s,"involvedObject":{"kind":%q,"name":"apps","namespace":"flux-system"},"lastTimestamp":%q}`, typ, reason, b, kind, ts)
	}
	data := `{"items":[` + strings.Join([]string{
		ev("Normal", "Kustomization", "ReconciliationSucceeded", "Reconciliation finished in 2s"),
		ev("Normal", "Kustomization", "Progressing", "Deployment/api/api configured\nService/api/api unchanged"),
		ev("Normal", "HelmRelease", "UpgradeSucceeded", "Helm upgrade succeeded for release payments"),
		ev("Warning", "HelmRelease", "UpgradeFailed", "Helm upgrade failed: timeout"),
		ev("Normal", "GitRepository", "NewArtifact", "stored artifact for commit 'bump api'"),
		ev("Normal", "Pod", "Pulled", "pulled"),
	}, ",") + `]}`
	cs := fluxChanges([]byte(data), time.Now().Add(-time.Hour))
	got := []string{}
	for _, c := range cs {
		got = append(got, c.kind+":"+c.detail)
	}
	want := "flux:Deployment/api/api configured|helm:Helm upgrade succeeded for release payments|helm:Helm upgrade failed: timeout|git:stored artifact for commit 'bump api'"
	if strings.Join(got, "|") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
}
