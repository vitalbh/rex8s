package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kwarn streams only Warning events (OOMKilled, BackOff, failed probes,
// image pulls…), across all namespaces by default.
//
//	kwarn        all namespaces of this pane's cluster
//	kwarn -n     only this pane's namespace
func (a *app) kwarn(args []string) error {
	fs := flag.NewFlagSet("kwarn", flag.ContinueOnError)
	nsOnly := fs.Bool("n", false, "only the pane's namespace")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	ns := ""
	if *nsOnly && !c.AllNS {
		ns = c.Namespace
		if ns == "" {
			ns = "default"
		}
	}
	return a.warnings(c.Name, ns)
}

// warnings streams Warning events of ctxName (ns "" = all namespaces). It is
// also the bottom pane of the k9s layout.
func (a *app) warnings(ctxName, ns string) error {
	if ns == kube.AllNamespaces {
		ns = ""
	}
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	scope, label := "all namespaces", "*"
	if ns != "" {
		scope, label = ns, ns
	}
	rex.Rename("warnings " + c.Short + "/" + label)
	a.tint(c)
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	args := []string{"--context", ctxName, "get", "events", "--field-selector", "type=Warning", "-w", "-o", "json"}
	if ns == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "-n", ns)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Println(tui.Bold.Render("Warnings") + tui.Dim.Render(" · "+c.Short+" · "+scope+" · repeats shown at most once a minute"))
	streamWarnings(out, os.Stdout, ns == "")
	err = cmd.Wait()
	fmt.Fprint(os.Stderr, tui.Dim.Render("\nwatch ended · press enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

type warnEvent struct {
	Metadata struct {
		UID       string `json:"uid"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"involvedObject"`
	Reason        string    `json:"reason"`
	Message       string    `json:"message"`
	Count         int       `json:"count"`
	LastTimestamp time.Time `json:"lastTimestamp"`
	EventTime     time.Time `json:"eventTime"`
	Series        *struct {
		Count            int       `json:"count"`
		LastObservedTime time.Time `json:"lastObservedTime"`
	} `json:"series"`
}

// streamWarnings formats a stream of event JSON objects, showing each event
// at most once a minute (repeating events like BackOff update constantly).
func streamWarnings(r io.Reader, w io.Writer, showNS bool) {
	dec := json.NewDecoder(r)
	shown := map[string]time.Time{}
	for {
		var e warnEvent
		if err := dec.Decode(&e); err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(w, tui.Err.Render("decode: "+err.Error()))
			}
			return
		}
		if last, ok := shown[e.Metadata.UID]; ok && time.Since(last) < time.Minute {
			continue
		}
		shown[e.Metadata.UID] = time.Now()
		when, count := e.LastTimestamp, e.Count
		if when.IsZero() {
			when = e.EventTime
		}
		if e.Series != nil {
			count = e.Series.Count
			if !e.Series.LastObservedTime.IsZero() {
				when = e.Series.LastObservedTime
			}
		}
		obj := kube.ShortKind(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name
		if showNS {
			obj = e.Metadata.Namespace + " " + obj
		}
		reps := ""
		if count > 1 {
			reps = tui.Dim.Render(fmt.Sprintf(" x%d", count))
		}
		fmt.Fprintf(w, "%s %s %s%s %s\n",
			tui.Dim.Render(when.Local().Format("15:04:05")),
			tui.Warn.Render(e.Reason), obj, reps, truncate(oneLine(e.Message), 240))
	}
}
