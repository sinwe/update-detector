package companion

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOutputSinkPushNeverBlocksWhenFull(t *testing.T) {
	sink := NewOutputSink(2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			sink.push(fmt.Sprintf("line%d", i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push blocked -- OutputSink must never block regardless of buffer fullness")
	}
}

func TestOutputSinkDeliversQueuedLinesInOrder(t *testing.T) {
	sink := NewOutputSink(10)
	sink.push("a")
	sink.push("b")
	sink.push("c")
	sink.Close()

	var got []string
	for line := range sink.Lines() {
		got = append(got, line)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestOutputSinkDropMarkerAppearsOnceBufferFreesUp drives push and drain
// by hand, synchronously, to deterministically exercise the exact
// recovery path: a marker can only ever be flushed *from inside a push
// call*, once draining has freed a slot -- so this interleaves the two
// explicitly rather than relying on a concurrent drainer's scheduling.
func TestOutputSinkDropMarkerAppearsOnceBufferFreesUp(t *testing.T) {
	sink := NewOutputSink(1)
	sink.push("a") // fills the only slot
	sink.push("b") // dropped -- buffer full
	sink.push("c") // dropped -- buffer full

	if got := <-sink.Lines(); got != "a" {
		t.Fatalf("got %q, want %q", got, "a")
	}

	// The slot just freed by draining "a" -- this push's first order of
	// business is flushing the 2-drop marker into it (its own line, "d",
	// then finds the buffer full again and is itself counted as a new
	// drop, same as "b"/"c" were).
	sink.push("d")
	marker := <-sink.Lines()
	if !strings.Contains(marker, "2") || !strings.Contains(marker, "dropped") {
		t.Fatalf("got %q, want a marker mentioning 2 dropped lines", marker)
	}
}

// TestOutputSinkNeverBlocksEvenUnderSustainedConcurrentLoad is a
// higher-level sanity check alongside the deterministic trace above:
// pushing far faster than a slow drain can keep up must still complete
// promptly, and everything actually received must be either a real
// pushed line or a recognizable drop marker -- never garbage.
func TestOutputSinkNeverBlocksEvenUnderSustainedConcurrentLoad(t *testing.T) {
	sink := NewOutputSink(4)
	var got []string
	drainDone := make(chan struct{})
	go func() {
		for line := range sink.Lines() {
			got = append(got, line)
		}
		close(drainDone)
	}()

	pushDone := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			sink.push(fmt.Sprintf("line%d", i))
		}
		close(pushDone)
	}()

	select {
	case <-pushDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pushing under sustained load blocked -- OutputSink must never block")
	}
	sink.Close()
	<-drainDone

	for _, line := range got {
		if strings.HasPrefix(line, "line") || strings.Contains(line, "dropped") {
			continue
		}
		t.Fatalf("got unexpected line %q", line)
	}
}
