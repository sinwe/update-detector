package checker

import "context"

type lineSinkKey struct{}

// WithLineSink attaches sink to ctx so a Checker's command-running code
// can tap real command stdout/stderr as it's produced (e.g. apt-get,
// apt-check, winget, powershell), without threading an extra parameter
// through every Check/checkPackages/checkUpgradable call in every
// platform package. Optional: its absence must never change behavior --
// see LineSinkFromContext. Intended for a caller that wants a live,
// real-command-output view of one specific Check call (e.g. a UI-
// triggered "verbose" recheck), not for the normal periodic detection
// cycle, which never attaches one.
func WithLineSink(ctx context.Context, sink func(string)) context.Context {
	return context.WithValue(ctx, lineSinkKey{}, sink)
}

// LineSinkFromContext returns the sink attached via WithLineSink, or nil
// if none -- callers must treat nil as "no live tap," not an error.
func LineSinkFromContext(ctx context.Context) func(string) {
	sink, _ := ctx.Value(lineSinkKey{}).(func(string))
	return sink
}
