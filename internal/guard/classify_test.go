package guard

import (
	"strings"
	"testing"
)

// One real command line per row: what the guard must make of it. Add a row
// for every new case.
var classifyCases = []struct {
	cmd  string // tool and arguments, split on spaces
	want Risk
}{
	// reads: never ask
	{"kubectl get pods", Safe},
	{"kubectl get pods -A -o wide", Safe},
	{"kubectl -n api get pods", Safe},
	{"kubectl -v 6 get pods", Safe},
	{"kubectl -v=6 get pods", Safe},
	{"kubectl -v6 get pods", Safe},
	{"kubectl --v 8 describe pod web-1", Safe},
	{"kubectl --context use -n api logs -f web-1", Safe},
	{"kubectl logs -f web-1 -c app --tail 100", Safe},
	{"kubectl describe deploy/web", Safe},
	{"kubectl top pods", Safe},
	{"kubectl top nodes", Safe},
	{"kubectl events -A", Safe},
	{"kubectl rollout status deploy/web", Safe},
	{"kubectl rollout history deploy/web", Safe},
	{"kubectl -n api rollout status deploy/web", Safe},
	{"kubectl auth can-i delete pods", Safe},
	{"kubectl auth whoami", Safe},
	{"kubectl diff -f a.yaml", Safe},
	{"kubectl wait --for condition=Ready pod/web-1", Safe},
	{"kubectl port-forward svc/web 8080:80", Safe},
	{"kubectl config get-contexts", Safe},
	{"kubectl config use-context use", Safe},
	{"kubectl version", Safe},
	{"kubectl api-resources", Safe},
	{"kubectl explain pods", Safe},
	{"kubectl", Safe},
	{"kubectl --help", Safe},
	{"kubectl delete --help", Safe},
	{"kubectl apply -f a.yaml --dry-run=client", Safe},
	{"kubectl apply -f a.yaml --dry-run=server", Safe},
	{"kubectl --request-timeout 5s get ns", Safe},
	{"kubectl --insecure-skip-tls-verify get pods", Safe},
	{"kubectl ctx", Safe},
	{"helm list -A", Safe},
	{"helm status web", Safe},
	{"helm history web", Safe},
	{"helm --kube-context use get values web", Safe},
	{"helm repo update", Safe},
	{"helm template web ./chart", Safe},
	{"helm diff upgrade web ./chart", Safe},
	{"helm upgrade web ./chart --dry-run", Safe},
	{"flux get ks -A", Safe},
	{"flux logs --kind Kustomization", Safe},
	{"flux diff kustomization apps --path ./apps", Safe},
	{"flux create source git web --url https://example.com/x --export", Safe},
	{"flux --context use get hr -A", Safe},

	// changes
	{"kubectl delete pod web-1", Change},
	{"kubectl -n api delete pod web-1", Change},
	{"kubectl --context use apply -f a.yaml", Change},
	{"kubectl rollout restart deploy/web", Change},
	{"kubectl rollout undo deploy/web", Change},
	{"kubectl scale deploy/web --replicas 0", Change},
	{"kubectl apply -f a.yaml --dry-run=none", Change},
	{"kubectl exec -it web-1 -- sh", Change},
	{"kubectl exec web-1 -- rm -rf /data", Change},
	{"kubectl attach -it web-1", Change},
	{"kubectl debug -it web-1 --image busybox", Change},
	{"kubectl debug node/n1 -it --image busybox", Change},
	{"kubectl cp a.txt api/web-1:/tmp/a.txt", Change},
	{"kubectl certificate approve csr-1", Change},
	{"kubectl certificate deny csr-1", Change},
	{"kubectl auth reconcile -f rbac.yaml", Change},
	{"kubectl cordon n1", Change},
	{"kubectl drain n1 --ignore-daemonsets", Change},
	{"kubectl run tmp --image busybox -it --rm", Change},
	// global flags with a value before the verb (review B1)
	{"kubectl -v 6 delete pod x", Change},
	{"kubectl --as-group system:masters delete ns x", Change},
	{"kubectl --token abc delete pod x", Change},
	{"kubectl --cache-dir /tmp/x apply -f a.yaml", Change},
	{"kubectl --as admin --as-group g --as-uid 1 delete pod x", Change},
	{"kubectl --certificate-authority ca.crt --client-certificate c.crt --client-key c.key delete pod x", Change},
	{"kubectl --tls-server-name k8s.example.com delete pod x", Change},
	{"kubectl --username u --password p delete pod x", Change},
	{"kubectl --profile cpu --profile-output p.out delete pod x", Change},
	{"kubectl --log-flush-frequency 5s --vmodule x=2 delete pod x", Change},
	{"kubectl --cluster prod-cluster delete pod x", Change},
	{"kubectl -s https://k8s.example.com delete pod x", Change},
	{"kubectl --user admin delete pod x", Change},
	{"kubectl -o yaml delete pod x", Change},
	{"helm --kube-as-user admin upgrade x ./c", Change},
	{"helm --kube-as-group g --kube-apiserver https://k8s.example.com --kube-token t upgrade x ./c", Change},
	{"helm --kube-ca-file ca.crt --registry-config r.json --repository-config r.yaml uninstall x", Change},
	{"helm --repository-cache /tmp/c --burst-limit 100 --qps 50 rollback x 1", Change},
	{"helm --kube-context use upgrade x .", Change},
	{"helm install web ./chart", Change},
	{"helm uninstall web", Change},
	{"helm del web", Change},
	{"helm test web", Change},
	{"flux suspend hr api", Change},
	{"flux resume ks apps", Change},
	{"flux reconcile ks apps", Change},
	{"flux reconcile source git flux-system", Change},
	{"flux --timeout 5m reconcile ks apps --with-source", Change},
	{"flux delete ks apps", Change},
	{"flux create source git web --url https://example.com/x", Change},
	{"flux bootstrap github --owner me", Change},

	// can't tell: asks on production (fail closed)
	{"kubectl --some-new-flag delete pod x", Change}, // either reading is a change
	{"kubectl --some-new-flag 6 delete pod x", Change},
	{"kubectl --some-new-flag get pods", Unknown}, // "pods" may be the verb
	{"kubectl --a --b get pods", Unknown},
	{"kubectl frobnicate pods", Unknown}, // a plugin or a typo
	{"kubectl alpha something", Unknown},
	{"kubectl rollout --unknown x status deploy/web", Change}, // only status/history read
	{"kubectl auth --unknown x can-i get pods", Change},
	{"kubectl -- delete pod x", Unknown},
	{"helm secrets upgrade x ./c", Unknown},
	{"flux frobnicate", Unknown},
	{"k9s", Unknown}, // a tool the guard doesn't know
}

func TestClassify(t *testing.T) {
	for _, c := range classifyCases {
		f := strings.Fields(c.cmd)
		if got := Classify(f[0], Parse(f[0], f[1:])); got != c.want {
			t.Errorf("%s: %v, want %v", c.cmd, got, c.want)
		}
	}
}

func (r Risk) String() string {
	return [...]string{"Safe", "Unknown", "Change"}[r]
}

func TestParseFlags(t *testing.T) {
	cases := []struct {
		cmd  string
		want Parsed
	}{
		{"kubectl --context=use --kubeconfig /k -n api delete pod x",
			Parsed{Verb: "delete", Sub: "pod", Context: "use", Kubeconfig: "/k", Namespace: "api"}},
		{"kubectl --cluster c1 -s https://k8s.example.com --user admin get pods",
			Parsed{Verb: "get", Sub: "pods", Cluster: "c1", Server: "https://k8s.example.com", User: "admin"}},
		{"kubectl -napi get pods", Parsed{Verb: "get", Sub: "pods", Namespace: "api"}},
		{"kubectl -n=api get pods", Parsed{Verb: "get", Sub: "pods", Namespace: "api"}},
		{"kubectl get pods --all-namespaces=false", Parsed{Verb: "get", Sub: "pods"}},
		{"helm --kube-context use --kube-apiserver https://k8s.example.com list",
			Parsed{Verb: "list", Context: "use", Server: "https://k8s.example.com"}},
		{"flux --context use --server https://k8s.example.com get ks",
			Parsed{Verb: "get", Sub: "ks", Context: "use", Server: "https://k8s.example.com"}},
	}
	for _, c := range cases {
		f := strings.Fields(c.cmd)
		p := Parse(f[0], f[1:])
		p.Rest = nil
		if p.Verb != c.want.Verb || p.Sub != c.want.Sub || p.Context != c.want.Context ||
			p.Kubeconfig != c.want.Kubeconfig || p.Namespace != c.want.Namespace || p.Cluster != c.want.Cluster ||
			p.Server != c.want.Server || p.User != c.want.User || p.AllNS != c.want.AllNS {
			t.Errorf("%s: %+v", c.cmd, p)
		}
	}
}
