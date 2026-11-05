package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kver: which version of each workload runs on each cluster — the image tag
// of its main container, one column per cluster. Rows where the clusters
// differ are highlighted (and listed first with -d).
//
//	kver [-d] [FILTER]   FILTER keeps workloads whose name contains it
func (a *app) kver(args []string) error {
	onlyDiff, filter := false, ""
	for _, arg := range args {
		if arg == "-d" {
			onlyDiff = true
		} else {
			filter = arg
		}
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
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("reading workloads on %d clusters…", len(clusters))))
	ws, skipped := a.findWorkloads(k, clusters, filter)
	var live []kube.Context // clusters that answered: one column each
	down := map[string]bool{}
	for _, s := range skipped {
		down[strings.SplitN(s, ":", 2)[0]] = true
	}
	for _, c := range clusters {
		if !down[c.Short] {
			live = append(live, c)
		}
	}
	rows := versionRows(ws)
	if onlyDiff {
		kept := rows[:0]
		for _, r := range rows {
			if r.differs() {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	fmt.Fprint(os.Stderr, "\033[1A\033[2K") // drop the "reading…" line
	if len(rows) == 0 {
		fmt.Println(tui.Dim.Render("no workloads match"))
	} else {
		fmt.Print(renderVersions(rows, live))
	}
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	return nil
}

// versionRow is one workload name and its tag on each cluster (by short name).
type versionRow struct {
	Name, Kind string
	Tags       map[string]string
}

func (r versionRow) differs() bool {
	seen := map[string]bool{}
	for _, t := range r.Tags {
		seen[t] = true
	}
	return len(seen) > 1
}

// versionRows groups the workloads by name (the same app usually has the same
// name on every cluster); rows that differ come first, then by name.
func versionRows(ws []target) []versionRow {
	byName := map[string]*versionRow{}
	var order []string
	for _, w := range ws {
		r, ok := byName[w.Name]
		if !ok {
			r = &versionRow{Name: w.Name, Kind: kube.ShortKind(w.Kind), Tags: map[string]string{}}
			byName[w.Name] = r
			order = append(order, w.Name)
		}
		r.Tags[w.c.Short] = imageTag(mainImage(w))
	}
	rows := make([]versionRow, 0, len(order))
	for _, n := range order {
		rows = append(rows, *byName[n])
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if di, dj := rows[i].differs(), rows[j].differs(); di != dj {
			return di
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// mainImage: the container named like the workload, else the first one.
func mainImage(t target) string {
	for _, c := range t.Containers {
		if c.Name == t.Name {
			return c.Image
		}
	}
	if len(t.Containers) > 0 {
		return t.Containers[0].Image
	}
	return ""
}

// imageTag: "ghcr.io/acme/api:1.4.2" → "1.4.2"; a digest → "@" + 12 hex digits.
func imageTag(img string) string {
	if img == "" {
		return "?"
	}
	if i := strings.Index(img, "@"); i >= 0 {
		d := strings.TrimPrefix(img[i+1:], "sha256:")
		if len(d) > 12 {
			d = d[:12]
		}
		return "@" + d
	}
	last := img[strings.LastIndex(img, "/")+1:]
	if i := strings.LastIndex(last, ":"); i >= 0 {
		return last[i+1:]
	}
	return "latest"
}

const maxTagWidth = 22

func renderVersions(rows []versionRow, clusters []kube.Context) string {
	nw := len("workload")
	for _, r := range rows {
		nw = max(nw, len(r.Name)+1+len(r.Kind))
	}
	widths := make([]int, len(clusters))
	for i, c := range clusters {
		widths[i] = len(c.Short)
		for _, r := range rows {
			widths[i] = max(widths[i], min(len(r.Tags[c.Short]), maxTagWidth))
		}
	}

	var b strings.Builder
	b.WriteString("  " + tui.Bold.Render(fmt.Sprintf("%-*s", nw, "workload")))
	for i, c := range clusters {
		h := fmt.Sprintf("  %-*s", widths[i], c.Short) // production in red
		if c.Prod {
			b.WriteString(tui.Err.Render(h))
		} else {
			b.WriteString(tui.Bold.Render(h))
		}
	}
	b.WriteString("\n")

	differ := 0
	for _, r := range rows {
		d := r.differs()
		pad := nw - len(r.Name) - 1 - len(r.Kind)
		mark, name := "  ", r.Name+" "+tui.Dim.Render(r.Kind)+strings.Repeat(" ", max(pad, 0))
		common := mostCommon(r.Tags)
		if d {
			differ++
			mark = tui.Warn.Render("≠ ")
			name = tui.Warn.Render(r.Name) + " " + tui.Dim.Render(r.Kind) + strings.Repeat(" ", max(pad, 0))
		}
		b.WriteString(mark + name)
		for i, c := range clusters {
			t, ok := r.Tags[c.Short]
			cell := fmt.Sprintf("  %-*s", widths[i], clip(t, maxTagWidth))
			switch {
			case !ok:
				cell = tui.Dim.Render(fmt.Sprintf("  %-*s", widths[i], "—"))
			case d && t != common:
				cell = tui.Warn.Render(cell)
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}
	summary := fmt.Sprintf("\n%d workload(s)", len(rows))
	if differ > 0 {
		summary += tui.Warn.Render(fmt.Sprintf(" · %d differ between clusters", differ))
	} else {
		summary += tui.Ok.Render(" · same version everywhere")
	}
	b.WriteString(summary + "\n")
	return b.String()
}

// mostCommon: the tag most clusters run (ties: the smallest, for stable output).
func mostCommon(tags map[string]string) string {
	n := map[string]int{}
	for _, t := range tags {
		n[t]++
	}
	best, bn := "", 0
	for t, c := range n {
		if c > bn || (c == bn && t < best) {
			best, bn = t, c
		}
	}
	return best
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
