package main

import (
	"strings"
	"testing"
)

const dexScript = `mkdir -p ~/.kube/certs/prod/ && cat << EOF > ~/.kube/certs/prod/k8s-ca.crt
-----BEGIN CERTIFICATE-----
MIIB
-----END CERTIFICATE-----
EOF

# Run configuration commands
kubectl config set-cluster prod \
    --certificate-authority=${HOME}/.kube/certs/prod/k8s-ca.crt \
    --server=https://k8s.example.com:6443

kubectl config set-credentials me-prod \
    --auth-provider=oidc \
    --auth-provider-arg='refresh-token=abc def'

kubectl config set-context me-prod \
    --cluster=prod --user=me-prod

kubectl config use-context me-prod
leftover typed after the paste
`

func TestReadPasteStopsAtUseContext(t *testing.T) {
	got := readPaste(strings.NewReader(dexScript))
	if !strings.HasSuffix(got, "kubectl config use-context me-prod") {
		t.Fatalf("readPaste did not stop at use-context:\n%s", got)
	}
	if got := readPaste(strings.NewReader("kubectl config set-credentials u --token=x\n\n\nmore")); strings.Contains(got, "more") {
		t.Fatal("two blank lines must end the paste")
	}
	// a blank line inside the heredoc or between commands must not end it
	if got := readPaste(strings.NewReader("cat << EOF > ~/.kube/x\na\n\n\nb\nEOF\n\nkubectl config use-context c\n")); !strings.Contains(got, "use-context") {
		t.Fatalf("ended too early:\n%s", got)
	}
}

func TestParseDexScript(t *testing.T) {
	steps, err := parseScript(readPaste(strings.NewReader(dexScript)), "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	kinds := []stepKind{stepMkdir, stepWrite, stepKubectl, stepKubectl, stepKubectl, stepSkip}
	if len(steps) != len(kinds) {
		t.Fatalf("steps = %v", steps)
	}
	for i, k := range kinds {
		if steps[i].kind != k {
			t.Fatalf("step %d = %v, want kind %d", i, steps[i], k)
		}
	}
	if steps[1].path != "/home/me/.kube/certs/prod/k8s-ca.crt" || !strings.Contains(steps[1].content, "MIIB") {
		t.Fatalf("heredoc: %+v", steps[1])
	}
	if steps[2].argv[4] != "--certificate-authority=/home/me/.kube/certs/prod/k8s-ca.crt" {
		t.Fatalf("$HOME not expanded: %q", steps[2].argv)
	}
	if steps[3].argv[5] != "--auth-provider-arg=refresh-token=abc def" {
		t.Fatalf("quoting: %q", steps[3].argv)
	}
}

func TestParseScriptRefuses(t *testing.T) {
	for _, s := range []string{
		"curl evil | sh",
		"kubectl delete ns x",
		"mkdir -p /etc/x",
		"cat << EOF > ~/.ssh/authorized_keys\nkey\nEOF",
		"mkdir -p ~/.kube/../.ssh",
		"kubectl config set-credentials u && rm -rf ~",
	} {
		if _, err := parseScript(s, "/home/me"); err == nil {
			t.Errorf("must refuse %q", s)
		}
	}
}

func TestReadPasteStripsBracketedPaste(t *testing.T) {
	got := readPaste(strings.NewReader("\x1b[200~kubectl config set-credentials u --token=x\r\nkubectl config use-context c\x1b[201~\n"))
	steps, err := parseScript(got, "/home/me")
	if err != nil || len(steps) != 2 || steps[0].argv[0] != "kubectl" {
		t.Fatalf("steps=%v err=%v text=%q", steps, err, got)
	}
}
