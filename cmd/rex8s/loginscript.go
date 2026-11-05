package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A login page such as dex-k8s-authenticator prints a small shell script:
//
//	mkdir -p ~/.kube/certs/prod/ && cat << EOF > ~/.kube/certs/prod/k8s-ca.crt
//	-----BEGIN CERTIFICATE-----
//	…
//	EOF
//
//	kubectl config set-cluster prod --certificate-authority=${HOME}/.kube/certs/prod/k8s-ca.crt --server=…
//	kubectl config set-credentials me-prod \
//	    --auth-provider=oidc --auth-provider-arg='refresh-token=…' …
//	kubectl config set-context me-prod --cluster=prod --user=me-prod
//	kubectl config use-context me-prod
//
// Instead of running it through a shell, kauth understands exactly these
// forms — mkdir and file writes under ~/.kube, and `kubectl config` — and
// refuses anything else.

type stepKind int

const (
	stepMkdir   stepKind = iota // mkdir -p PATH
	stepWrite                   // cat << EOF > PATH … EOF
	stepKubectl                 // kubectl config …
	stepSkip                    // kubectl config use-context: leaves your default context alone
)

type step struct {
	kind    stepKind
	path    string
	content string
	argv    []string
}

func (s step) String() string {
	switch s.kind {
	case stepMkdir:
		return "mkdir " + tildePath(s.path)
	case stepWrite:
		return "write " + tildePath(s.path)
	case stepSkip:
		return "skipped " + strings.Join(s.argv[1:], " ") + " (your default context stays as it is)"
	}
	if len(s.argv) > 3 {
		return strings.Join(s.argv[:4], " ")
	}
	return strings.Join(s.argv, " ")
}

func tildePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

var (
	heredocRe  = regexp.MustCompile(`^cat\s+(?:<<-?\s*['"]?(\w+)['"]?\s*>\s*(\S+)|>\s*(\S+)\s*<<-?\s*['"]?(\w+)['"]?)$`)
	useCtxRe   = regexp.MustCompile(`^kubectl\s+config\s+use-context\b`)
	heredocBeg = regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
)

// readPaste reads a pasted script from r. It stops at `kubectl config
// use-context` (the last line such pages print), at EOF, or at two empty
// lines in a row — blank lines between commands are part of the paste.
func readPaste(r io.Reader) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var lines []string
	delim, blanks, content := "", 0, false
	for sc.Scan() {
		line := sc.Text()
		lines = append(lines, line)
		t := strings.TrimSpace(line)
		if delim != "" { // inside a heredoc
			if t == delim {
				delim = ""
			}
			continue
		}
		if m := heredocBeg.FindStringSubmatch(t); m != nil {
			delim, content = m[1], true
			continue
		}
		if t == "" {
			blanks++
			if blanks >= 2 && content {
				break
			}
			continue
		}
		blanks, content = 0, true
		if useCtxRe.MatchString(t) && !strings.HasSuffix(t, `\`) {
			break
		}
	}
	text := strings.Join(lines, "\n")
	// in case the terminal still wrapped the paste in bracketed-paste markers
	return strings.NewReplacer("\x1b[200~", "", "\x1b[201~", "", "\r", "").Replace(text)
}

// parseScript turns a pasted login script into steps, refusing anything it
// doesn't recognize. Paths must stay under ~/.kube.
func parseScript(text, home string) ([]step, error) {
	kubeDir := filepath.Join(home, ".kube")
	expand := func(s string) string {
		s = strings.ReplaceAll(s, "${HOME}", home)
		s = strings.ReplaceAll(s, "$HOME", home)
		if s == "~" || strings.HasPrefix(s, "~/") {
			s = home + s[1:]
		}
		return s
	}
	safePath := func(p string) (string, error) {
		p = filepath.Clean(expand(strings.Trim(p, `'"`)))
		if p != kubeDir && !strings.HasPrefix(p, kubeDir+string(filepath.Separator)) {
			return "", fmt.Errorf("refusing to touch %s: only paths under ~/.kube are allowed", p)
		}
		return p, nil
	}

	lines := strings.Split(text, "\n")
	var steps []step
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) { // continuation
			i++
			line = strings.TrimSuffix(line, `\`) + " " + strings.TrimSpace(lines[i])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, stmt := range strings.Split(line, "&&") {
			stmt = strings.TrimSpace(stmt)
			switch {
			case stmt == "":
			case heredocRe.MatchString(stmt):
				m := heredocRe.FindStringSubmatch(stmt)
				delim, dst := m[1], m[2]
				if delim == "" {
					delim, dst = m[4], m[3]
				}
				p, err := safePath(dst)
				if err != nil {
					return nil, err
				}
				var body []string
				closed := false
				for i+1 < len(lines) {
					i++
					if strings.TrimSpace(lines[i]) == delim {
						closed = true
						break
					}
					body = append(body, lines[i])
				}
				if !closed {
					return nil, fmt.Errorf("heredoc for %s is missing its closing %s", tildePath(p), delim)
				}
				steps = append(steps, step{kind: stepWrite, path: p, content: strings.Join(body, "\n") + "\n"})
			default:
				argv, err := shellSplit(stmt)
				if err != nil {
					return nil, err
				}
				switch {
				case len(argv) >= 3 && argv[0] == "mkdir" && argv[1] == "-p":
					for _, d := range argv[2:] {
						p, err := safePath(d)
						if err != nil {
							return nil, err
						}
						steps = append(steps, step{kind: stepMkdir, path: p})
					}
				case len(argv) >= 3 && argv[0] == "kubectl" && argv[1] == "config":
					for j := range argv {
						argv[j] = expand(argv[j])
						if k, v, ok := strings.Cut(argv[j], "="); ok && strings.HasPrefix(k, "--") {
							argv[j] = k + "=" + expand(v)
						}
					}
					kind := stepKubectl
					if argv[2] == "use-context" {
						kind = stepSkip
					}
					steps = append(steps, step{kind: kind, argv: argv})
				default:
					return nil, fmt.Errorf("refusing to run %q: only mkdir/cat under ~/.kube and `kubectl config` are accepted", truncate(stmt, 60))
				}
			}
		}
	}
	if len(steps) == 0 {
		return nil, errors.New("nothing pasted")
	}
	return steps, nil
}
