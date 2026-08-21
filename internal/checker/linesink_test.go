package checker

import (
	"context"
	"testing"
)

func TestLineSinkRoundTrip(t *testing.T) {
	var got []string
	ctx := WithLineSink(context.Background(), func(line string) { got = append(got, line) })

	sink := LineSinkFromContext(ctx)
	if sink == nil {
		t.Fatal("expected a non-nil sink after WithLineSink")
	}
	sink("hello")

	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %v, want [hello]", got)
	}
}

func TestLineSinkFromContextNilWhenAbsent(t *testing.T) {
	if sink := LineSinkFromContext(context.Background()); sink != nil {
		t.Fatal("expected nil sink when none attached")
	}
}
