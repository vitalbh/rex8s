package logstream

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// PrefixWriter writes each line with a prefix, one line at a time under a
// lock shared by every stream, so lines of different clusters never mix.
// A partial line waits for the rest.
type PrefixWriter struct {
	prefix string
	out    io.Writer
	mu     *sync.Mutex
	buf    []byte
}

// NewPrefixWriter writes to out, holding mu for each line.
func NewPrefixWriter(prefix string, out io.Writer, mu *sync.Mutex) *PrefixWriter {
	return &PrefixWriter{prefix: prefix, out: out, mu: mu}
}

func (p *PrefixWriter) Write(b []byte) (int, error) {
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
