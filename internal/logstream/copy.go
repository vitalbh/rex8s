package logstream

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vitalbh/rex8s/internal/tui"
)

// Linker makes the cluster URLs in a log line clickable. pod is the pod
// that wrote the line, "" when unknown (then URLs that only make sense
// inside the pod, like localhost, are left alone).
type Linker interface {
	Line(s, pod string) string
}

// Pruner is a Linker that can drop the links of pods that are gone; the
// workload follower calls it when the pod set changes.
type Pruner interface {
	Prune(alive func(pod string) bool)
}

// LinkerFunc adapts a function to Linker.
type LinkerFunc func(s, pod string) string

// Line calls f.
func (f LinkerFunc) Line(s, pod string) string { return f(s, pod) }

// Label is what leads a log line, given the pod and container that wrote it
// ("" when unknown).
type Label func(pod, container string) string

// MaxLine is the longest line printed whole; the rest of a longer one is
// read and dropped, and a marker says how much.
const MaxLine = 64 * 1024

// kubectl logs --prefix: "[pod/NAME/CONTAINER] "
var podPrefixRe = regexp.MustCompile(`^\[pod/([^/\]]+)/([^\]]*)\] `)

// Copy prints the lines of r that pass the grep, with their cluster URLs
// made clickable by links (nil: none). kubectl's "[pod/NAME/CONTAINER] "
// prefix is taken off and handed to label; pod is the pod writing the lines
// when kubectl does not prefix them ("" when not a single known pod). It
// reads until r ends, also past lines of any length, so kubectl never
// blocks writing; the error is r's (nil at EOF).
func (f *Follower) Copy(r io.Reader, links Linker, label Label, pod string) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		b, cut, err := readLine(br, MaxLine)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		raw, p, c := string(b), pod, ""
		if m := podPrefixRe.FindStringSubmatch(raw); m != nil {
			p, c, raw = m[1], m[2], raw[len(m[0]):]
		}
		if f.Grep != nil && !f.Grep.MatchString(Visible(raw)) {
			continue
		}
		// links first: the highlight must not split a URL, and skips the
		// links' targets
		if links != nil {
			raw = links.Line(raw, p)
		}
		line := highlight(raw, f.Grep)
		if cut > 0 {
			line += tui.Dim.Render(fmt.Sprintf(" … [line cut: %d more bytes]", cut))
		}
		lead := ""
		if label != nil {
			lead = label(p, c)
		}
		f.println(lead + line)
	}
}

// readLine reads one line (without its \n or \r\n), keeping at most max
// bytes of it; cut is how many bytes of the line were read and dropped. A
// last line without \n is returned as a line; io.EOF comes after it.
func readLine(br *bufio.Reader, max int) (line []byte, cut int, err error) {
	for {
		chunk, err := br.ReadSlice('\n')
		if room := max - len(line); room > 0 {
			take := min(room, len(chunk))
			line = append(line, chunk[:take]...)
			cut += len(chunk) - take
		} else {
			cut += len(chunk)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err == nil: // the \n is the last byte read
			if cut > 0 {
				cut--
			} else {
				line = line[:len(line)-1]
			}
		case len(line) == 0 && cut == 0:
			return nil, 0, err
		}
		if cut > 0 {
			n := len(line)
			line = cleanCut(line)
			cut += n - len(line)
		}
		if n := len(line); cut == 0 && n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		return line, cut, nil
	}
}

// cleanCut drops what the cut left broken at the end of a line: a partial
// UTF-8 character or escape sequence.
func cleanCut(b []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 || b[len(b)-1] < 0x80 {
			break
		}
		b = b[:len(b)-1]
	}
	if i := strings.LastIndexByte(string(b), 0x1b); i >= 0 {
		if _, complete := escEnd(string(b), i); !complete {
			b = b[:i]
		}
	}
	return b
}
