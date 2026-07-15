package companion

import (
	"bytes"
	"io"
)

// lineTee wraps dst, forwarding every byte written to it (so callers that
// only care about the final accumulated buffer see no behavior change) while
// also invoking onLine once per complete '\n'-terminated line as it's
// written, so a caller can tap the stream incrementally without changing
// what's ultimately captured.
//
// Safe to use as both exec.Cmd.Stdout and exec.Cmd.Stderr simultaneously via
// the same *lineTee value -- os/exec guarantees at most one goroutine writes
// to a shared writer value at a time when Stdout and Stderr are the same
// comparable value, and this type relies on that guarantee rather than
// duplicating its own locking.
type lineTee struct {
	dst     io.Writer
	onLine  func(string)
	partial []byte
}

func newLineTee(dst io.Writer, onLine func(string)) *lineTee {
	return &lineTee{dst: dst, onLine: onLine}
}

func (t *lineTee) Write(p []byte) (int, error) {
	n, err := t.dst.Write(p)
	if err == nil && t.onLine != nil {
		t.emit(p)
	}
	return n, err
}

func (t *lineTee) emit(p []byte) {
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		line := string(t.partial[:i])
		t.partial = t.partial[i+1:]
		t.onLine(line)
	}
}

// flush emits any trailing partial line that never got a terminating '\n'
// -- e.g. a script's last line, or output still mid-line when the process
// exited. Call once after the command has finished producing output.
func (t *lineTee) flush() {
	if t.onLine != nil && len(t.partial) > 0 {
		t.onLine(string(t.partial))
		t.partial = nil
	}
}
