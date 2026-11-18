package kube

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Table is kubectl's printed table (`kubectl get KIND -o wide`, whose
// columns the API server chooses: a CRD's additionalPrinterColumns too).
type Table struct {
	Columns []string
	Rows    [][]string // each as long as Columns ("" for a missing cell)
}

// Col is the index of the column named name, or -1.
func (t Table) Col(name string) int {
	for i, c := range t.Columns {
		if c == name {
			return i
		}
	}
	return -1
}

// ParseTable reads a table kubectl printed. kubectl aligns it with a
// tabwriter (cells padded by at least 3 spaces), so each column starts where
// its title starts in the header; titles have single spaces at most
// ("NOMINATED NODE"). A cell runs up to the next column's start; the last
// one to the end of the line. Widths count runes, as the tabwriter does.
func ParseTable(text string) Table {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimRight(l, " \r"))
		}
	}
	if len(lines) == 0 {
		return Table{}
	}
	var t Table
	var starts []int
	head := []rune(lines[0])
	for i := 0; i < len(head); {
		if head[i] == ' ' {
			i++
			continue
		}
		start := i
		for i < len(head) && !(head[i] == ' ' && (i+1 >= len(head) || head[i+1] == ' ')) {
			i++
		}
		starts = append(starts, start)
		t.Columns = append(t.Columns, string(head[start:i]))
	}
	for _, l := range lines[1:] {
		r := []rune(l)
		row := make([]string, len(starts))
		for c, s := range starts {
			if s >= len(r) {
				break
			}
			end := len(r)
			if c+1 < len(starts) {
				end = min(end, starts[c+1])
			}
			row[c] = strings.TrimSpace(string(r[s:end]))
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// APIResource is a row of `kubectl api-resources`.
type APIResource struct {
	Name       string   // plural: "ingresses"
	Short      []string // "ing"
	Group      string   // "networking.k8s.io" ("" for the core group)
	Namespaced bool
	Kind       string // "Ingress"
}

// Full is the name kubectl can't mistake: "ingresses.networking.k8s.io",
// "configmaps".
func (r APIResource) Full() string {
	if r.Group == "" {
		return r.Name
	}
	return r.Name + "." + r.Group
}

// Matches: name is the resource's plural, singular (kind), a short name, or
// its full name, any case.
func (r APIResource) Matches(name string) bool {
	name = strings.ToLower(name)
	if name == r.Name || name == strings.ToLower(r.Kind) || name == r.Full() || name == strings.ToLower(r.Kind)+"."+r.Group {
		return true
	}
	for _, s := range r.Short {
		if name == s {
			return true
		}
	}
	return false
}

// ParseAPIResources reads `kubectl api-resources` (with its header).
func ParseAPIResources(text string) []APIResource {
	t := ParseTable(text)
	name, short, ver, nsd, kind := t.Col("NAME"), t.Col("SHORTNAMES"), t.Col("APIVERSION"), t.Col("NAMESPACED"), t.Col("KIND")
	if name < 0 || kind < 0 {
		return nil
	}
	var out []APIResource
	for _, row := range t.Rows {
		r := APIResource{Name: row[name], Kind: row[kind]}
		if short >= 0 && row[short] != "" {
			r.Short = strings.Split(row[short], ",")
		}
		if ver >= 0 {
			if g, _, ok := strings.Cut(row[ver], "/"); ok {
				r.Group = g
			}
		}
		r.Namespaced = nsd >= 0 && row[nsd] == "true"
		if r.Name != "" && utf8.ValidString(r.Name) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FindAPIResource is the resource name means (see Matches): a core or
// apps/v1 one first when several match ("events" is core, not
// events.k8s.io).
func FindAPIResource(rs []APIResource, name string) (APIResource, bool) {
	var found []APIResource
	for _, r := range rs {
		if r.Matches(name) {
			found = append(found, r)
		}
	}
	if len(found) == 0 {
		return APIResource{}, false
	}
	sort.SliceStable(found, func(i, j int) bool { return groupRank(found[i].Group) < groupRank(found[j].Group) })
	return found[0], true
}

func groupRank(g string) int {
	switch {
	case g == "":
		return 0
	case g == "apps" || g == "batch":
		return 1
	case strings.HasSuffix(g, ".k8s.io"):
		return 2
	}
	return 3
}

// ParseAge reads kubectl's AGE: "45s", "12m", "5h3m", "4d", "2y110d".
func ParseAge(s string) (time.Duration, bool) {
	units := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'y': 365 * 24 * time.Hour}
	var total time.Duration
	num := ""
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= '0' && ch <= '9':
			num += string(ch)
		case units[ch] != 0 && num != "":
			n, _ := strconv.Atoi(num)
			total += time.Duration(n) * units[ch]
			num = ""
		default:
			return 0, false
		}
	}
	return total, num == "" && s != ""
}
