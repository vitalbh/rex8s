package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Native prompts: a question in the Rex command palette (every connected Rex
// app, the iPhone's too; the first answer wins). Go can't show one itself:
// rex.ui only works inside an action the server loaded, so Ask runs the
// k8s_ui action of rex8s.lua with rex do and waits for the answer.
//
// Ask returns ErrNoPrompt when nobody could be asked: Rex not installed, no
// server, an old server or app (API revision < 17), rex8s.lua without
// k8s_ui, or every app already showing a question. Callers then do what they
// did before prompts existed (a TUI at a terminal, a refusal without one).

// ErrNoPrompt: no Rex app could show the question.
var ErrNoPrompt = errors.New("no Rex app can show the question")

// PromptAPIRevision is the first control API revision with prompts.
const PromptAPIRevision = 17

// Item is one row of a pick prompt.
type Item struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Selected bool   `json:"selected,omitempty"`
}

// Prompt is a question: Items set makes it a pick, otherwise a line of text.
type Prompt struct {
	Title       string `json:"title"`
	Placeholder string `json:"placeholder,omitempty"`
	Value       string `json:"value,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
	Items       []Item `json:"items,omitempty"`
	Multiple    bool   `json:"multiple,omitempty"`
	AllowOther  bool   `json:"allow_other,omitempty"`
	// Timeout: seconds to wait for an answer (Rex: 300 by default, 3600 at
	// most).
	Timeout int `json:"timeout,omitempty"`
}

// Answer is what the person did. Reason is "" when they answered, else
// cancelled or timeout. Value is the chosen item's ID (pick) or the text
// (input, or typed with AllowOther, then Other is true); Values the ticked
// IDs with Multiple.
type Answer struct {
	Value  string
	Values []string
	Other  bool
	Reason string
}

// Answered reports whether the person answered (not cancelled, not timed out).
func (a Answer) Answered() bool { return a.Reason == "" }

// what k8s_ui returns
type uiResult struct {
	OK     bool   `json:"ok"`
	Value  string `json:"value"`
	Values string `json:"values"` // newline-separated (multiple)
	Multi  bool   `json:"multi"`
	Other  bool   `json:"other"`
	Reason string `json:"reason"`
}

// installed and runDo: tests replace them.
var installed = Installed

// runDo runs rex do and returns its stdout; tests replace it.
var runDo = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, Bin(), args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
	}
	return out, err
}

// Ask shows p in the Rex apps and waits for the answer. ErrNoPrompt (wrapped)
// when no app could show it.
func Ask(p Prompt) (Answer, error) {
	if !installed() {
		return Answer{}, ErrNoPrompt
	}
	if p.Timeout <= 0 {
		p.Timeout = 300
	}
	kind := "input"
	if p.Items != nil {
		kind = "pick"
		if len(p.Items) == 0 {
			p.Items = []Item{}
		}
	}
	spec, err := json.Marshal(struct {
		Kind string `json:"kind"`
		Prompt
	}{kind, p})
	if err != nil {
		return Answer{}, err
	}
	args, _ := json.Marshal(map[string]string{"spec": string(spec)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.Timeout+15)*time.Second)
	defer cancel()
	out, err := runDo(ctx, "--autostart=false", "do", "k8s_ui", "--args", string(args), "--json")
	if err != nil {
		if ctx.Err() != nil {
			return Answer{Reason: "timeout"}, nil
		}
		return Answer{}, fmt.Errorf("%w (%v)", ErrNoPrompt, err)
	}
	var r uiResult
	if err := json.Unmarshal(out, &r); err != nil {
		return Answer{}, fmt.Errorf("%w (k8s_ui answered %q)", ErrNoPrompt, strings.TrimSpace(string(out)))
	}
	if !r.OK {
		switch r.Reason {
		case "cancelled", "timeout":
			return Answer{Reason: r.Reason}, nil
		}
		return Answer{}, fmt.Errorf("%w (%s)", ErrNoPrompt, r.Reason)
	}
	a := Answer{Value: r.Value, Other: r.Other}
	if r.Multi {
		a.Values = []string{}
		for v := range strings.SplitSeq(r.Values, "\n") {
			if v != "" {
				a.Values = append(a.Values, v)
			}
		}
	}
	return a, nil
}

// APIRevision is the running server's control API revision, 0 when it can't
// be asked.
func APIRevision() int {
	out, err := exec.Command(Bin(), "--timeout", "5s", "--autostart=false", "whoami", "--json").Output()
	if err != nil {
		return 0
	}
	var w struct {
		APIRevision int `json:"api_revision"`
	}
	if json.Unmarshal(out, &w) != nil {
		return 0
	}
	return w.APIRevision
}
