package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// prefixWriter writes each line with a prefix, one line at a time under a
// lock shared by every stream, so lines of different clusters never mix.
type prefixWriter struct {
	prefix string
	out    io.Writer
	mu     *sync.Mutex
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		p.mu.Lock()
		fmt.Fprint(p.out, p.prefix+string(p.buf[:i+1]))
		p.mu.Unlock()
		p.buf = p.buf[i+1:]
	}
}

// pickClusters: every cluster, or the ones named (short name or context).
func (a *app) pickClusters(names []string) ([]kube.Context, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, err
	}
	all := uniqueClusters(kube.Contexts(raw, a.cfg))
	if len(names) == 0 {
		return all, nil
	}
	var out []kube.Context
	for _, n := range names {
		found := false
		for _, c := range all {
			if c.Short == n || c.Name == n {
				out, found = append(out, c), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("cluster %q not found", n)
		}
	}
	return out, nil
}

// kwarnAll: kwarn -A [CLUSTER...] — the Warning events of every cluster (or
// the ones named) in one stream, each line led by its cluster. A watch that
// ends (API timeout, lost connection) is started again.
func (a *app) kwarnAll(names []string) error {
	clusters, err := a.pickClusters(names)
	if err != nil {
		return err
	}
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	w, prod := 0, false
	var shorts []string
	for _, c := range clusters {
		w = max(w, len(c.Short))
		prod = prod || c.Prod
		shorts = append(shorts, c.Short)
	}
	rex.Rename("warnings " + strings.Join(shorts, ","))
	if prod {
		a.tint(kube.Context{Prod: true})
	}
	fmt.Println(tui.Bold.Render("Warnings") + tui.Dim.Render(" · "+strings.Join(shorts, " · ")+" · all namespaces · repeats shown at most once a minute · ctrl+c to stop"))

	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, c := range clusters {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(streamColors[i%len(streamColors)]))
		if c.Prod {
			style = style.Bold(true)
		}
		pw := &prefixWriter{prefix: style.Render(fmt.Sprintf("%-*s", w, c.Short)) + " ", out: os.Stdout, mu: &mu}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				start := time.Now()
				cmd := exec.Command(bin, "--context", c.Name, "get", "events", "-A",
					"--field-selector", "type=Warning", "-w", "-o", "json")
				cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
				var stderr strings.Builder
				cmd.Stderr = &stderr
				out, err := cmd.StdoutPipe()
				if err == nil {
					err = cmd.Start()
				}
				if err == nil {
					streamWarnings(out, pw, true)
					err = cmd.Wait()
				}
				if kube.AuthExpired(fmt.Errorf("%s", stderr.String())) {
					fmt.Fprintln(pw, tui.Err.Render("login expired: kauth "+c.Short+" — this cluster stops here"))
					return
				}
				if time.Since(start) < 10*time.Second { // failing fast: say why, wait
					why := lastLogLine(strings.TrimSpace(stderr.String()))
					if why == "" && err != nil {
						why = err.Error()
					}
					fmt.Fprintln(pw, tui.Dim.Render("watch failed ("+why+"); retrying in 30s"))
					time.Sleep(30 * time.Second)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Fprint(os.Stderr, tui.Dim.Render("\nwatch ended · press enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}
