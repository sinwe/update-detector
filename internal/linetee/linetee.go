// Package linetee provides a writer that tees written bytes through
// unchanged while also invoking a callback once per complete line, so a
// caller can tap a command's output incrementally (e.g. for live
// streaming) without changing what's ultimately captured.
package linetee

import (
	"bytes"
	"io"
)

// Writer wraps Dst, forwarding every byte written to it (so callers that
// only care about the final accumulated buffer see no behavior change)
// while also invoking OnLine once per complete '\n'-terminated line as
// it's written, so a caller can tap the stream incrementally without
// changing what's ultimately captured.
//
// Safe to use as both exec.Cmd.Stdout and exec.Cmd.Stderr simultaneously
// via the same *Writer value -- os/exec guarantees at most one goroutine
// writes to a shared writer value at a time when Stdout and Stderr are
// the same comparable value, and this type relies on that guarantee
// rather than duplicating its own locking.
type Writer struct {
	dst     io.Writer
	onLine  func(string)
	partial []byte
}

// New returns a Writer teeing to dst and invoking onLine per complete line.
func New(dst io.Writer, onLine func(string)) *Writer {
	return &Writer{dst: dst, onLine: onLine}
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if err == nil && w.onLine != nil {
		w.emit(p)
	}
	return n, err
}

func (w *Writer) emit(p []byte) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		line := string(w.partial[:i])
		w.partial = w.partial[i+1:]
		w.onLine(line)
	}
}

// Flush emits any trailing partial line that never got a terminating '\n'
// -- e.g. a script's last line, or output still mid-line when the process
// exited. Call once after the command has finished producing output.
func (w *Writer) Flush() {
	if w.onLine != nil && len(w.partial) > 0 {
		w.onLine(string(w.partial))
		w.partial = nil
	}
}
