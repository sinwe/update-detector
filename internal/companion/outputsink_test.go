package companion

import (
	"context"
	"testing"
)

func TestEmitFromContextPushesToSink(t *testing.T) {
	sink := NewOutputSink(16)
	ctx := WithOutputSink(context.Background(), sink)
	emit := emitFromContext(ctx)

	emit("hello %s", "world")
	emit("no args")

	sink.Close()

	lines := drainSink(sink)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "hello world" {
		t.Errorf("line 0: got %q, want %q", lines[0], "hello world")
	}
	if lines[1] != "no args" {
		t.Errorf("line 1: got %q, want %q", lines[1], "no args")
	}
}

func TestEmitFromContextNoopWithoutSink(t *testing.T) {
	emit := emitFromContext(context.Background())
	// Must not panic
	emit("should not panic")
}

func drainSink(sink *OutputSink) []string {
	var lines []string
	for line := range sink.Lines() {
		lines = append(lines, line)
	}
	return lines
}
