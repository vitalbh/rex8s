package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"sigs.k8s.io/yaml"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kdiff compares the same workload on two clusters: its spec as YAML, minus
// status and the fields the API server fills in.
//
//	kdiff [FILTER]   mark two rows (space), or pick one and kdiff finds the
//	                 workload with the same name on the other cluster
func (a *app) kdiff(args []string) error {
	filter := ""
	if len(args) > 0 {
		filter = args[0]
	}
	k, err := a.kubectl()
	if err != nil {
		return err
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := uniqueClusters(kube.Contexts(raw, a.cfg))
	var skipped []string
	chosen, all, err := a.pickTargets(k, clusters, filter, "Compare which workloads? (space marks two)", &skipped)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	if err != nil || len(chosen) == 0 {
		return err
	}
	var left, right target
	switch len(chosen) {
	case 2:
		left, right = chosen[0], chosen[1]
	case 1:
		left = chosen[0]
		var others []target
		for _, t := range all {
			if t.Name == left.Name && t.c.Name != left.c.Name {
				others = append(others, t)
			}
		}
		switch len(others) {
		case 0:
			return fmt.Errorf("%s exists only on %s; mark two rows (space) to compare different names", left.Name, left.c.Short)
		case 1:
			right = others[0]
		default:
			byKey := map[string]target{}
			items := make([]tui.Item, 0, len(others))
			for _, t := range others {
				byKey[t.key()] = t
				items = append(items, tui.Item{Key: t.key(), Label: fmt.Sprintf("%-10s %s/%s", t.c.Short, t.Namespace, t.Name), Badge: prodBadgeBool(t.c.Prod)})
			}
			it, err := tui.Run(tui.Options{Title: fmt.Sprintf("Compare %s (%s) with", left.Name, left.c.Short), Items: items})
			if err != nil || it == nil {
				return err
			}
			right = byKey[it.Key]
		}
	default:
		return fmt.Errorf("mark two workloads to compare (%d marked)", len(chosen))
	}

	ly, err := specYAML(k, a.src, left)
	if err != nil {
		return err
	}
	ry, err := specYAML(k, a.src, right)
	if err != nil {
		return err
	}
	out, same, err := unifiedDiff(label(left), ly, label(right), ry)
	if err != nil {
		return err
	}
	if same {
		fmt.Printf("%s and %s: %s\n", label(left), label(right), tui.Ok.Render("same spec"))
		return nil
	}
	return page(colorUnified(out))
}

func label(t target) string {
	return fmt.Sprintf("%s %s %s/%s", t.c.Short, kube.ShortKind(t.Kind), t.Namespace, t.Name)
}

func specYAML(k, kubeconfig string, t target) ([]byte, error) {
	out, err := kubectlOut(k, kubeconfig, 30*time.Second, "--context", t.c.Name, "-n", t.Namespace,
		"get", strings.ToLower(t.Kind), t.Name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("%s: %v", label(t), lastLogLine(err.Error()))
	}
	return cleanSpec([]byte(out))
}

// cleanSpec drops what differs on every cluster without meaning anything:
// status, server-set metadata, the last restart and the Flux/kubectl
// bookkeeping annotations.
func cleanSpec(data []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	delete(obj, "status")
	if md, ok := obj["metadata"].(map[string]any); ok {
		for _, f := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields", "selfLink", "ownerReferences"} {
			delete(md, f)
		}
		dropAnnotations(md, "kubectl.kubernetes.io/last-applied-configuration", "deployment.kubernetes.io/revision")
	}
	if spec, ok := obj["spec"].(map[string]any); ok {
		if tpl, ok := spec["template"].(map[string]any); ok {
			if md, ok := tpl["metadata"].(map[string]any); ok {
				dropAnnotations(md, "kubectl.kubernetes.io/restartedAt")
				delete(md, "creationTimestamp")
			}
		}
	}
	j, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(j)
}

func dropAnnotations(md map[string]any, keys ...string) {
	an, ok := md["annotations"].(map[string]any)
	if !ok {
		return
	}
	for _, k := range keys {
		delete(an, k)
	}
	if len(an) == 0 {
		delete(md, "annotations")
	}
}

// unifiedDiff runs diff -u on the two texts; same is true when they match.
func unifiedDiff(lname string, l []byte, rname string, r []byte) (out string, same bool, err error) {
	dir, err := os.MkdirTemp("", "kdiff")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	lp, rp := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	if err := os.WriteFile(lp, l, 0o600); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(rp, r, 0o600); err != nil {
		return "", false, err
	}
	var buf bytes.Buffer
	cmd := exec.Command("diff", "-u", "--label", lname, "--label", rname, lp, rp)
	cmd.Stdout = &buf
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return "", true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return buf.String(), false, nil
	}
	return "", false, fmt.Errorf("diff: %v", err)
}

func colorUnified(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "---"), strings.HasPrefix(l, "+++"):
			lines[i] = tui.Bold.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = tui.Dim.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = tui.Err.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = tui.Ok.Render(l)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// page prints s, through less when it doesn't fit the terminal.
func page(s string) error {
	fd := int(os.Stdout.Fd())
	_, h, err := term.GetSize(fd)
	if err != nil || !term.IsTerminal(fd) || strings.Count(s, "\n") < h-1 {
		fmt.Print(s)
		return nil
	}
	less, err := exec.LookPath("less")
	if err != nil {
		fmt.Print(s)
		return nil
	}
	cmd := exec.Command(less, "-R")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(s), os.Stdout, os.Stderr
	return cmd.Run()
}
