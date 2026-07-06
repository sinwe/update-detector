package hostflavor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "ubuntu", content: "ID=ubuntu\nVERSION_ID=\"24.04\"\n", want: "ubuntu"},
		{name: "debian", content: "ID=debian\nVERSION_ID=\"13\"\n", want: "debian"},
		{name: "raspbian", content: "ID=raspbian\nVERSION_ID=\"12\"\n", want: "debian"},
		{name: "unrecognized falls back to ubuntu", content: "ID=fedora\n", want: "ubuntu"},
		{name: "missing ID falls back to ubuntu", content: "VERSION_ID=\"1\"\n", want: "ubuntu"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "os-release")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := Detect(path); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectMissingFile(t *testing.T) {
	if got := Detect(filepath.Join(t.TempDir(), "does-not-exist")); got != "ubuntu" {
		t.Fatalf("got %q, want ubuntu (default on read failure)", got)
	}
}
