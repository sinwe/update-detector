package linetee

import (
	"bytes"
	"reflect"
	"testing"
)

func TestWriterEmitsCompleteLinesAcrossArbitraryChunking(t *testing.T) {
	var buf bytes.Buffer
	var lines []string
	w := New(&buf, func(line string) { lines = append(lines, line) })

	// Deliberately split mid-line across writes, at a boundary that
	// doesn't align with any line at all.
	chunks := []string{"first li", "ne\nseco", "nd line\nthir"}
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush() // "thir" has no trailing newline yet

	want := []string{"first line", "second line", "thir"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got lines %v, want %v", lines, want)
	}
	if buf.String() != "first line\nsecond line\nthir" {
		t.Fatalf("got accumulated buffer %q, want unchanged from what was written", buf.String())
	}
}

func TestWriterFlushNoopWhenNoTrailingPartial(t *testing.T) {
	var buf bytes.Buffer
	var lines []string
	w := New(&buf, func(line string) { lines = append(lines, line) })
	if _, err := w.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	want := []string{"one", "two"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got %v, want %v", lines, want)
	}
}

func TestWriterNilOnLineIsSafe(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, nil)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if buf.String() != "hello\n" {
		t.Fatalf("got %q", buf.String())
	}
}
