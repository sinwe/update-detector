package companion

import (
	"context"
	"strings"
	"testing"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/checker"
)

func TestApplyTapsOutputToAttachedSink(t *testing.T) {
	writeFakeAptGet(t, `
if [ "$1" = "update" ]; then exit 0; fi
echo "line one"
echo "line two"
exit 0
`)
	srv := statusServer(t, checker.Status{})

	sink := NewOutputSink(10)
	ctx := WithOutputSink(context.Background(), sink)
	result := Apply(ctx, srv.URL, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	sink.Close()
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}

	found := map[string]bool{}
	for line := range sink.Lines() {
		found[line] = true
	}
	if !found["line one"] || !found["line two"] {
		t.Fatalf("expected the sink to have tapped the command's output lines, got %v", found)
	}
}

// TestApplyWithoutSinkUnchangedFromBefore locks in that attaching no sink
// (the default -- every pre-existing caller) behaves exactly as it did
// before OutputSink existed: runCapped's returned/reported output is
// identical either way.
func TestApplyWithoutSinkUnchangedFromBefore(t *testing.T) {
	writeFakeAptGet(t, `if [ "$1" = "update" ]; then exit 0; fi; echo "hello"; exit 0`)
	srv := statusServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if !result.Success || !strings.Contains(result.Message, "hello") {
		t.Fatalf("got %#v, want success mentioning the command's own output", result)
	}
}

// TestApplyTapsOutputIncrementallyWhileRunning is the regression test for
// "incrementally, not just at the end": the fake command sleeps between
// two lines, and the first must be observable on the sink well before the
// (still-sleeping) command, and therefore Apply itself, finishes.
func TestApplyTapsOutputIncrementallyWhileRunning(t *testing.T) {
	writeFakeAptGet(t, `
if [ "$1" = "update" ]; then exit 0; fi
echo "first"
sleep 0.2
echo "second"
exit 0
`)
	srv := statusServer(t, checker.Status{})

	sink := NewOutputSink(10)
	ctx := WithOutputSink(context.Background(), sink)

	resultCh := make(chan aggregator.ActionResult, 1)
	go func() {
		resultCh <- Apply(ctx, srv.URL, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	}()

	select {
	case line := <-sink.Lines():
		if line != "first" {
			t.Fatalf("got first tapped line %q, want %q", line, "first")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first line to be tapped before the command finished")
	}

	select {
	case <-resultCh:
		t.Fatal("Apply returned before the sleeping command finished -- test setup is broken, this would not actually exercise incremental tapping")
	default:
	}

	result := <-resultCh
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
}
