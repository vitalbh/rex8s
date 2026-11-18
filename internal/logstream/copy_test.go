package logstream

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is an io.Writer that tests may read while streams write.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

// A 2 MB line must not stop the stream: it is cut, a marker says how much
// went, and the lines after it still come (B10). The writer is an io.Pipe,
// like kubectl's stdout: if Copy stopped reading, the writer would block.
func TestCopyLongLine(t *testing.T) {
	pr, pw := io.Pipe()
	wrote := make(chan error, 1)
	go func() {
		long := strings.Repeat("x", 2<<20)
		_, err := fmt.Fprintf(pw, "first\n%s\nafter\nlast without newline", long)
		pw.Close()
		wrote <- err
	}()
	var out syncBuffer
	f := &Follower{Out: &out}
	done := make(chan error, 1)
	go func() { done <- f.Copy(pr, nil, nil, "") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Copy stuck on a long line")
	}
	if err := <-wrote; err != nil {
		t.Fatalf("writer: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(plain(out.String()), "\n"), "\n")
	if len(lines) != 4 || lines[0] != "first" || lines[2] != "after" || lines[3] != "last without newline" {
		t.Fatalf("lines: %d, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	want := strings.Repeat("x", MaxLine) + fmt.Sprintf(" … [line cut: %d more bytes]", 2<<20-MaxLine)
	if lines[1] != want {
		t.Fatalf("cut line: len %d, ends %q", len(lines[1]), lines[1][max(0, len(lines[1])-40):])
	}
}

func TestReadLine(t *testing.T) {
	read := func(in string, n int) []string {
		br := bufio.NewReaderSize(strings.NewReader(in), 16)
		var got []string
		for {
			l, cut, err := readLine(br, n)
			if err != nil {
				return got
			}
			got = append(got, fmt.Sprintf("%s|%d", l, cut))
		}
	}
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"a\r\nb\n\nc", 100, "a|0 b|0 |0 c|0"},
		{"abcdefghij\nk\n", 4, "abcd|6 k|0"},
		{"abcd\nk", 4, "abcd|0 k|0"},
		{strings.Repeat("y", 40) + "\nz\n", 20, strings.Repeat("y", 20) + "|20 z|0"},
		{"ab\xc3\xa9cd\n", 3, "ab|4"},                   // é cut in half: dropped whole
		{"ab\x1b[31mred\n", 5, "ab|8"},                  // a color code cut in half: dropped
		{"ab\x1b[31mred\x1b[0m\n", 9, "ab\x1b[31mre|5"}, // a whole one stays
	} {
		if got := strings.Join(read(c.in, c.max), " "); got != c.want {
			t.Errorf("readLine(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

type fakeLinker struct{ pods []string }

func (l *fakeLinker) Line(s, pod string) string {
	l.pods = append(l.pods, pod)
	return strings.ReplaceAll(s, "http://web.app.svc", "\x1b]8;;http://localhost:4100\x1b\\http://web.app.svc\x1b]8;;\x1b\\")
}

// kubectl's --prefix goes to the label; the pod handed to the linker is the
// prefix's, or the one given; the grep looks at the visible text only.
func TestCopyPrefixLinksGrep(t *testing.T) {
	in := "[pod/web-1/web] GET http://web.app.svc/health\n" +
		"[pod/web-2/sidecar] \x1b[31mnothing\x1b[0m\n" +
		"no prefix: web is up\n"
	var out syncBuffer
	f := &Follower{Out: &out, Grep: regexp.MustCompile("(?i)web")}
	l := &fakeLinker{}
	label := func(pod, c string) string { return "[" + pod + "|" + c + "] " }
	if err := f.Copy(strings.NewReader(in), l, label, "job-pod"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	want := "[web-1|web] GET \x1b]8;;http://localhost:4100\x1b\\http://<web>.app.svc\x1b]8;;\x1b\\/health\n" +
		"[job-pod|] no prefix: <web> is up\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if strings.Join(l.pods, ",") != "web-1,job-pod" {
		t.Fatalf("linker pods: %v", l.pods)
	}
}

func TestPrefixWriter(t *testing.T) {
	var out strings.Builder
	var mu sync.Mutex
	w := NewPrefixWriter("use ", &out, &mu)
	fmt.Fprint(w, "one\ntw")
	fmt.Fprint(w, "o\n")
	if got := out.String(); got != "use one\nuse two\n" {
		t.Fatalf("got %q", got)
	}
}

func TestStreamLabel(t *testing.T) {
	multi := StreamLabel("euw web", "39", true)
	for _, c := range []struct{ pod, container, want string }{
		{"web-7d9f", "web", "euw web-7d9f "},
		{"web-7d9f", "exporter", "euw web-7d9f/exporter "},
		{"", "", "euw web "},
	} {
		if got := plain(multi(c.pod, c.container)); got != c.want {
			t.Errorf("multi(%q, %q) = %q, want %q", c.pod, c.container, got, c.want)
		}
	}
	one := StreamLabel("euw web", "39", false)
	if got := plain(one("web-7d9f", "web")); got != "web-7d9f " {
		t.Errorf("single = %q", got)
	}
	if one("", "") != "" {
		t.Error("single without pod should be empty")
	}
}

func TestSpec(t *testing.T) {
	s := Spec{"use-admin", "app", "app=web", "use web"}
	got, ok := ParseSpec(s.String())
	if !ok || got != s {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
	if _, ok := ParseSpec("ctx|ns||label"); ok {
		t.Error("spec without selector accepted")
	}
}
