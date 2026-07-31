package companion

import (
	"context"
	"fmt"
)

// OutputSink is a non-blocking, best-effort buffer of output lines produced
// while an action's command runs, drained by StreamOutput and posted to the
// aggregator live. Attaching one via WithOutputSink is optional, and its
// absence must never change behavior: runCapped works exactly as it did
// before this existed when no sink is in context.
//
// push is only ever called from the single goroutine os/exec guarantees
// when Stdout and Stderr are the same writer value (see lineTee) -- so
// OutputSink needs no locking of its own.
type OutputSink struct {
	ch      chan string
	dropped int
}

// NewOutputSink returns a sink buffering up to bufSize lines before push
// starts dropping the newest ones -- never blocking the command producing
// them, since a full buffer means the aggregator (or the pump goroutine
// draining Lines) isn't keeping up, and dropping a live line is far
// preferable to stalling apt-get/install.sh/docker over it.
func NewOutputSink(bufSize int) *OutputSink {
	return &OutputSink{ch: make(chan string, bufSize)}
}

// push is non-blocking. While the buffer is full it just counts drops;
// once a slot frees up, one synthetic marker line reports how many were
// lost before resuming normal lines -- mirrors runCapped's own
// "...(truncated)..." convention for the same reason (an incomplete live
// view should say so, not silently skip ahead).
func (s *OutputSink) push(line string) {
	if s.dropped > 0 {
		select {
		case s.ch <- fmt.Sprintf("...(%d line(s) dropped)...", s.dropped):
			s.dropped = 0
		default:
			s.dropped++
			return
		}
	}
	select {
	case s.ch <- line:
	default:
		s.dropped++
	}
}

// Lines returns the channel StreamOutput drains. Closed by Close.
func (s *OutputSink) Lines() <-chan string {
	return s.ch
}

// Close signals no more lines are coming -- StreamOutput's pump goroutine
// drains whatever's already buffered, then ends the stream cleanly. Callers
// must not call push after Close, and must call Close at most once (there's
// exactly one call site in this codebase, a single defer, so a sync.Once
// guard would be unused complexity).
func (s *OutputSink) Close() {
	close(s.ch)
}

type sinkKey struct{}

// WithOutputSink attaches sink to ctx so runCapped (via sinkFromContext)
// can tap command output without every function in the Apply/SelfUpdate
// call chain needing an explicit parameter for it -- see runCapped's own
// comment for why context, not a threaded parameter, was chosen here.
func WithOutputSink(ctx context.Context, sink *OutputSink) context.Context {
	return context.WithValue(ctx, sinkKey{}, sink)
}

// sinkFromContext returns the sink attached via WithOutputSink, or nil if
// none -- runCapped must treat nil as "no live tap," not an error.
func sinkFromContext(ctx context.Context) *OutputSink {
	sink, _ := ctx.Value(sinkKey{}).(*OutputSink)
	return sink
}

// emitFromContext returns a function that pushes a formatted progress line
// to the OutputSink in ctx (if any), or a no-op if none. Convenience for
// action handlers that want to emit progress without nil-checking.
func emitFromContext(ctx context.Context) func(string, ...any) {
	sink := sinkFromContext(ctx)
	if sink == nil {
		return func(string, ...any) {}
	}
	return func(format string, args ...any) {
		sink.push(fmt.Sprintf(format, args...))
	}
}
