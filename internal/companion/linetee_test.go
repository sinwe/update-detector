package companion

import (
	"bytes"
	"reflect"
	"testing"
)

func TestLineTeeEmitsCompleteLinesAcrossArbitraryChunking(t *testing.T) {
	var buf bytes.Buffer
	var lines []string
	tee := newLineTee(&buf, func(line string) { lines = append(lines, line) })

	// Deliberately split mid-line across writes, at a boundary that
	// doesn't align with any line at all.
	chunks := []string{"first li", "ne\nseco", "nd line\nthir"}
	for _, c := range chunks {
		if _, err := tee.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	tee.flush() // "thir" has no trailing newline yet

	want := []string{"first line", "second line", "thir"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got lines %v, want %v", lines, want)
	}
	if buf.String() != "first line\nsecond line\nthir" {
		t.Fatalf("got accumulated buffer %q, want unchanged from what was written", buf.String())
	}
}

func TestLineTeeFlushNoopWhenNoTrailingPartial(t *testing.T) {
	var buf bytes.Buffer
	var lines []string
	tee := newLineTee(&buf, func(line string) { lines = append(lines, line) })
	if _, err := tee.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	tee.flush()
	want := []string{"one", "two"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %v, want %v", lines, want)
	}
}

func TestLineTeeNilOnLineIsSafe(t *testing.T) {
	var buf bytes.Buffer
	tee := newLineTee(&buf, nil)
	if _, err := tee.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	tee.flush()
	if buf.String() != "hello\n" {
		t.Fatalf("got %q", buf.String())
	}
}
